// Package pipeline wires the stages together: ingest, claims, sandbox, agent, compare.
package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/itskrsna/Crucible/internal/agent"
	"github.com/itskrsna/Crucible/internal/claims"
	"github.com/itskrsna/Crucible/internal/compare"
	"github.com/itskrsna/Crucible/internal/ingest"
	"github.com/itskrsna/Crucible/internal/llm"
	"github.com/itskrsna/Crucible/internal/sandbox"
	"github.com/itskrsna/Crucible/internal/schemas"
)

type Deps struct {
	HTTP       *http.Client
	LLM        llm.Chatter
	Model      string
	Image      string
	Agent      agent.Config
	NewSandbox func(ctx context.Context) (sandbox.Executor, error)
	Log        func(format string, args ...any)
}

func (d Deps) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
	}
}

// Run attempts to reproduce one paper and returns the record plus the full transcript.
func Run(ctx context.Context, d Deps, arxivID string) (schemas.Record, *agent.Outcome, error) {
	d.logf("ingest %s", arxivID)
	paper, latex, err := ingest.Load(ctx, d.HTTP, arxivID)
	if err != nil {
		return schemas.Record{}, nil, err
	}

	d.logf("extracting claims from %q", paper.Title)
	cs, err := claims.Extract(ctx, d.LLM, paper.Title, latex)
	if err != nil {
		return schemas.Record{}, nil, err
	}
	if len(cs) == 0 {
		return schemas.Record{}, nil, fmt.Errorf("pipeline: no quantitative claims found in %s", arxivID)
	}

	d.logf("starting sandbox (%d claims)", len(cs))
	ex, err := d.NewSandbox(ctx)
	if err != nil {
		return schemas.Record{}, nil, err
	}
	defer ex.Close(context.WithoutCancel(ctx))

	outcome, err := agent.Run(ctx, d.LLM, ex, d.Image, paper, cs, d.Agent)
	if err != nil {
		return schemas.Record{}, outcome, err
	}
	return BuildRecord(paper, cs, outcome, d.Model, d.Image), outcome, nil
}

// BuildRecord turns an agent outcome into a record with verdicts.
func BuildRecord(paper schemas.Paper, cs []schemas.Claim, outcome *agent.Outcome, model, image string) schemas.Record {
	byClaim := map[string]agent.Measurement{}
	for _, m := range outcome.Measurements {
		byClaim[m.ClaimID] = m
	}
	rec := schemas.Record{
		SchemaVersion: schemas.SchemaVersion,
		Paper:         paper,
		Dockerfile:    outcome.Dockerfile,
		Model:         model,
		Steps:         len(outcome.Steps),
	}
	for _, c := range cs {
		m := byClaim[c.ID]
		res := compare.Judge(c, m.Value, m.Command, m.Reason)
		res.Pattern = m.Pattern
		if m.Note != "" {
			if res.Note != "" {
				res.Note += "; "
			}
			res.Note += m.Note
		}
		res.BeatsBaselines = compare.BeatsBaseline(c, m.Value)
		rec.Results = append(rec.Results, res)
	}
	return rec
}

// Rerun rebuilds a record's Dockerfile and re-executes its recorded commands
// without any LLM. It returns a fresh judgement for each claim.
func Rerun(ctx context.Context, rec schemas.Record, tag string, newSandbox func(ctx context.Context, image string) (sandbox.Executor, error), cfg agent.Config) ([]schemas.ClaimResult, error) {
	if rec.Dockerfile == "" {
		return nil, fmt.Errorf("pipeline: record has no Dockerfile")
	}
	if err := sandbox.BuildImage(ctx, rec.Dockerfile, tag); err != nil {
		return nil, err
	}
	ex, err := newSandbox(ctx, tag)
	if err != nil {
		return nil, err
	}
	defer ex.Close(context.WithoutCancel(ctx))

	timeout := cfg.RunTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	var out []schemas.ClaimResult
	for _, r := range rec.Results {
		if r.Command == "" || r.Pattern == "" {
			out = append(out, compare.Judge(r.Claim, nil, r.Command, r.Reason))
			continue
		}
		res, err := ex.Exec(ctx, r.Command, timeout)
		if err != nil {
			return out, err
		}
		var measured *float64
		if v, perr := agent.ExtractNumber(res.Output, r.Pattern); perr == nil {
			measured = &v
		}
		reason := schemas.OtherFailure
		if res.TimedOut {
			reason = schemas.NeedsMoreCompute
		}
		j := compare.Judge(r.Claim, measured, r.Command, reason)
		j.Pattern = r.Pattern
		j.BeatsBaselines = compare.BeatsBaseline(r.Claim, measured)
		out = append(out, j)
	}
	return out, nil
}
