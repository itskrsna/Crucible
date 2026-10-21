// Package eval measures how accurate the agent is on tasks with known answers
// (for example tasks converted from CORE-Bench), so claims about the tool
// rest on numbers instead of anecdotes.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/itskrsna/Crucible/internal/agent"
	"github.com/itskrsna/Crucible/internal/llm"
	"github.com/itskrsna/Crucible/internal/sandbox"
	"github.com/itskrsna/Crucible/internal/schemas"
)

// Task is one problem with a known answer. Claim.Value holds the ground truth.
type Task struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	RepoURL   string          `json:"repo_url"`
	Commit    string          `json:"commit,omitempty"`
	Claims    []schemas.Claim `json:"claims"`
	Tolerance float64         `json:"tolerance,omitempty"` // relative; default 2%
}

type TaskResult struct {
	ID       string     `json:"id"`
	Correct  int        `json:"correct"`
	Total    int        `json:"total"`
	Solved   bool       `json:"solved"` // every claim correct
	Error    string     `json:"error,omitempty"`
	Measured []*float64 `json:"measured,omitempty"`
}

type Report struct {
	Model         string       `json:"model"`
	Tasks         []TaskResult `json:"tasks"`
	TasksSolved   int          `json:"tasks_solved"`
	ClaimAccuracy float64      `json:"claim_accuracy"`
}

func LoadTasks(path string) ([]Task, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ts []Task
	if err := json.Unmarshal(b, &ts); err != nil {
		return nil, fmt.Errorf("eval: parse %s: %w", path, err)
	}
	return ts, nil
}

func within(measured, truth, tol float64) bool {
	d := math.Abs(truth)
	if d == 0 {
		d = 1
	}
	return math.Abs(measured-truth)/d <= tol
}

// Score checks measurements against a task's ground truth.
func Score(t Task, ms []agent.Measurement) TaskResult {
	tol := t.Tolerance
	if tol <= 0 {
		tol = 0.02
	}
	byID := map[string]agent.Measurement{}
	for _, m := range ms {
		byID[m.ClaimID] = m
	}
	res := TaskResult{ID: t.ID, Total: len(t.Claims)}
	for _, c := range t.Claims {
		m := byID[c.ID]
		res.Measured = append(res.Measured, m.Value)
		if m.Value != nil && within(*m.Value, c.Value, tol) {
			res.Correct++
		}
	}
	res.Solved = res.Total > 0 && res.Correct == res.Total
	return res
}

// Run executes every task in its own sandbox and aggregates accuracy.
func Run(ctx context.Context, ch llm.Chatter, model string, tasks []Task, image string,
	newSandbox func(ctx context.Context) (sandbox.Executor, error), cfg agent.Config, logf func(string, ...any)) Report {

	rep := Report{Model: model}
	var correct, total int
	for _, t := range tasks {
		if logf != nil {
			logf("task %s", t.ID)
		}
		res := runOne(ctx, ch, t, image, newSandbox, cfg)
		rep.Tasks = append(rep.Tasks, res)
		correct += res.Correct
		total += res.Total
		if res.Solved {
			rep.TasksSolved++
		}
	}
	if total > 0 {
		rep.ClaimAccuracy = float64(correct) / float64(total)
	}
	return rep
}

func runOne(ctx context.Context, ch llm.Chatter, t Task, image string,
	newSandbox func(ctx context.Context) (sandbox.Executor, error), cfg agent.Config) TaskResult {

	ex, err := newSandbox(ctx)
	if err != nil {
		return TaskResult{ID: t.ID, Total: len(t.Claims), Error: err.Error()}
	}
	defer ex.Close(context.WithoutCancel(ctx))

	paper := schemas.Paper{ArxivID: t.ID, Title: t.Title, RepoURL: t.RepoURL, RepoCommit: t.Commit}
	out, err := agent.Run(ctx, ch, ex, image, paper, t.Claims, cfg)
	if err != nil {
		return TaskResult{ID: t.ID, Total: len(t.Claims), Error: err.Error()}
	}
	return Score(t, out.Measurements)
}
