package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/itskrsna/Crucible/internal/agent"
	"github.com/itskrsna/Crucible/internal/discover"
	"github.com/itskrsna/Crucible/internal/eval"
	"github.com/itskrsna/Crucible/internal/llm"
	"github.com/itskrsna/Crucible/internal/pipeline"
	"github.com/itskrsna/Crucible/internal/record"
	"github.com/itskrsna/Crucible/internal/sandbox"
	"github.com/itskrsna/Crucible/internal/site"
)

const usage = `crucible: check whether published computational results reproduce.

Usage:
  crucible discover [--limit N] [--since YYYY-MM-DD] [--triage] [--deep] [--out candidates.json]
  crucible run <arxiv_id>                  attempt one paper, write records/<id>/
  crucible batch --candidates FILE         run many papers (resumable; skips existing records)
  crucible base                         build the sandbox base image (also done automatically on first use)
  crucible rerun <arxiv_id>              rebuild the recorded Dockerfile and re-run, no LLM
  crucible review <arxiv_id> --approve     mark a record as human-reviewed so it can be published
  crucible site [--out public]             generate the static registry and badges
  crucible eval --tasks FILE               measure agent accuracy on tasks with known answers

Model settings (flags or environment):
  --base-url / CRUCIBLE_BASE_URL   OpenAI-compatible endpoint (default http://localhost:11434/v1, Ollama)
  --model    / CRUCIBLE_MODEL      model name (default llama3.1:8b)
  --api-key  / CRUCIBLE_API_KEY    API key for hosted endpoints such as OpenRouter or Groq
`

type common struct {
	baseURL, model, apiKey, cache, image, records string
	maxSteps, cpus, memGB                         int
	stepTimeout, runTimeout                       time.Duration
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func addCommon(fs *flag.FlagSet) *common {
	c := &common{}
	fs.StringVar(&c.baseURL, "base-url", env("CRUCIBLE_BASE_URL", "http://localhost:11434/v1"), "OpenAI-compatible API base URL")
	fs.StringVar(&c.model, "model", env("CRUCIBLE_MODEL", "llama3.1:8b"), "model name")
	fs.StringVar(&c.apiKey, "api-key", os.Getenv("CRUCIBLE_API_KEY"), "API key (prefer the env var)")
	fs.StringVar(&c.cache, "cache", ".cache/llm", "LLM response cache directory")
	fs.StringVar(&c.image, "image", sandbox.BaseImage, "sandbox image (the default is built automatically on first use)")
	fs.StringVar(&c.records, "records", "records", "records directory")
	fs.IntVar(&c.maxSteps, "max-steps", 40, "agent step budget")
	fs.IntVar(&c.cpus, "cpus", 4, "sandbox CPU limit")
	fs.IntVar(&c.memGB, "mem-gb", 8, "sandbox memory limit in GB")
	fs.DurationVar(&c.stepTimeout, "step-timeout", 10*time.Minute, "time limit per agent command")
	fs.DurationVar(&c.runTimeout, "run-timeout", 30*time.Minute, "time limit per measurement run")
	return c
}

func (c *common) llm() *llm.Client { return llm.New(c.baseURL, c.apiKey, c.model, c.cache) }

func (c *common) agentCfg() agent.Config {
	return agent.Config{MaxSteps: c.maxSteps, StepTimeout: c.stepTimeout, RunTimeout: c.runTimeout}
}

// ensureImage builds the default base image on first use. Custom images are
// the user's responsibility.
func (c *common) ensureImage(ctx context.Context) error {
	if c.image != sandbox.BaseImage {
		return nil
	}
	return sandbox.EnsureBase(ctx, logf)
}

func (c *common) newSandbox(ctx context.Context) (sandbox.Executor, error) {
	if err := c.ensureImage(ctx); err != nil {
		return nil, err
	}
	return sandbox.Start(ctx, sandbox.Options{Image: c.image, CPUs: float64(c.cpus), MemoryGB: c.memGB})
}

func (c *common) deps() pipeline.Deps {
	return pipeline.Deps{
		HTTP:       &http.Client{Timeout: 2 * time.Minute},
		LLM:        c.llm(),
		Model:      c.model,
		Image:      c.image,
		Agent:      c.agentCfg(),
		NewSandbox: c.newSandbox,
		Log:        logf,
	}
}

func logf(format string, args ...any) { fmt.Fprintf(os.Stderr, "crucible: "+format+"\n", args...) }

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "crucible:", err)
	os.Exit(1)
}

func parse(fs *flag.FlagSet, args []string) {
	// Allow flags after the positional argument (crucible run 2501.1 --model x).
	var pos, rest []string
	for i := 0; i < len(args); i++ {
		if len(args[i]) > 0 && args[i][0] == '-' {
			rest = append(rest, args[i])
			if f := fs.Lookup(trimDashes(args[i])); f != nil && !isBool(f) && i+1 < len(args) && !hasEquals(args[i]) {
				i++
				rest = append(rest, args[i])
			}
		} else {
			pos = append(pos, args[i])
		}
	}
	if err := fs.Parse(append(rest, pos...)); err != nil {
		os.Exit(2)
	}
}

func trimDashes(s string) string {
	for len(s) > 0 && s[0] == '-' {
		s = s[1:]
	}
	for i := range s {
		if s[i] == '=' {
			return s[:i]
		}
	}
	return s
}

func hasEquals(s string) bool {
	for i := range s {
		if s[i] == '=' {
			return true
		}
	}
	return false
}

func isBool(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// loadDotEnv sets variables from a KEY=VALUE file without overriding ones
// already in the environment. A missing file is fine.
func loadDotEnv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if ok && k != "" && os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func main() {
	loadDotEnv(".env")
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	args := os.Args[2:]
	switch os.Args[1] {
	case "discover":
		cmdDiscover(ctx, args)
	case "run":
		cmdRun(ctx, args)
	case "batch":
		cmdBatch(ctx, args)
	case "rerun":
		cmdRerun(ctx, args)
	case "base":
		if err := sandbox.EnsureBase(ctx, logf); err != nil {
			fatal(err)
		}
		logf("base image %s is ready", sandbox.BaseImage)
	case "review":
		cmdReview(args)
	case "site":
		cmdSite(args)
	case "eval":
		cmdEval(ctx, args)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func cmdDiscover(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	c := addCommon(fs)
	limit := fs.Int("limit", 50, "number of candidates to find")
	scan := fs.Int("scan", 600, "max arXiv entries to scan")
	since := fs.String("since", "2025-01-01", "ignore papers older than this date")
	triage := fs.Bool("triage", false, "score runnability of each README with the LLM")
	deep := fs.Bool("deep", false, "also download LaTeX for papers with no repo link in the abstract")
	out := fs.String("out", "candidates.json", "output file")
	parse(fs, args)

	sinceT, err := time.Parse("2006-01-02", *since)
	if err != nil {
		fatal(fmt.Errorf("bad --since: %w", err))
	}
	o := discover.Options{Limit: *limit, Scan: *scan, Since: sinceT, Deep: *deep, Progress: logf}
	if *triage {
		o.Triage = c.llm()
	}
	cands, err := discover.Run(ctx, &http.Client{Timeout: 2 * time.Minute}, o)
	if len(cands) > 0 {
		if werr := writeJSON(*out, cands); werr != nil {
			fatal(werr)
		}
		logf("wrote %d candidates to %s", len(cands), *out)
	}
	if err != nil {
		fatal(err)
	}
}

func runOne(ctx context.Context, c *common, id string) error {
	rec, outcome, err := pipeline.Run(ctx, c.deps(), id)
	if err != nil {
		return err
	}
	if err := record.Write(c.records, rec, outcome); err != nil {
		return err
	}
	logf("%s: %s (record in %s)", id, record.Overall(rec), record.Dir(c.records, id))
	if record.NeedsReview(rec) {
		logf("%s has negative results: review the transcript, then `crucible review %s --approve`", id, id)
	}
	return nil
}

func cmdRun(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	c := addCommon(fs)
	parse(fs, args)
	if fs.NArg() != 1 {
		fatal(fmt.Errorf("usage: crucible run <arxiv_id>"))
	}
	if err := runOne(ctx, c, fs.Arg(0)); err != nil {
		fatal(err)
	}
}

func cmdBatch(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("batch", flag.ExitOnError)
	c := addCommon(fs)
	file := fs.String("candidates", "candidates.json", "candidates file from `crucible discover`")
	workers := fs.Int("concurrency", 1, "papers to run in parallel (each uses its own sandbox)")
	parse(fs, args)

	b, err := os.ReadFile(*file)
	if err != nil {
		fatal(err)
	}
	var cands []discover.Candidate
	if err := json.Unmarshal(b, &cands); err != nil {
		fatal(err)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, max(*workers, 1))
	var mu sync.Mutex
	failed := 0
	for _, cand := range cands {
		if _, err := os.Stat(filepath.Join(record.Dir(c.records, cand.ArxivID), "record.json")); err == nil {
			logf("skip %s: record exists", cand.ArxivID)
			continue
		}
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := runOne(ctx, c, id); err != nil {
				logf("%s failed: %v", id, err)
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}(cand.ArxivID)
	}
	wg.Wait()
	logf("batch finished, %d failed", failed)
}

func cmdRerun(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("rerun", flag.ExitOnError)
	c := addCommon(fs)
	parse(fs, args)
	if fs.NArg() != 1 {
		fatal(fmt.Errorf("usage: crucible rerun <arxiv_id>"))
	}
	id := fs.Arg(0)
	rec, err := record.Load(filepath.Join(record.Dir(c.records, id), "record.json"))
	if err != nil {
		fatal(err)
	}
	if err := sandbox.EnsureBase(ctx, logf); err != nil {
		fatal(err)
	}
	results, err := pipeline.Rerun(ctx, rec, "crucible-rerun-"+record.Slug(id),
		func(ctx context.Context, image string) (sandbox.Executor, error) {
			return sandbox.Start(ctx, sandbox.Options{Image: image, CPUs: float64(c.cpus), MemoryGB: c.memGB})
		}, c.agentCfg())
	if err != nil {
		fatal(err)
	}
	for i, r := range results {
		before := "-"
		if rec.Results[i].Measured != nil {
			before = fmt.Sprint(*rec.Results[i].Measured)
		}
		after := "-"
		if r.Measured != nil {
			after = fmt.Sprint(*r.Measured)
		}
		fmt.Printf("%-4s %-8s recorded=%s rerun=%s verdict=%s\n", r.Claim.ID, r.Claim.Metric, before, after, r.Verdict)
	}
}

func cmdReview(args []string) {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	c := addCommon(fs)
	approve := fs.Bool("approve", false, "mark the record as human-reviewed")
	parse(fs, args)
	if fs.NArg() != 1 || !*approve {
		fatal(fmt.Errorf("usage: crucible review <arxiv_id> --approve"))
	}
	path := filepath.Join(record.Dir(c.records, fs.Arg(0)), "record.json")
	rec, err := record.Load(path)
	if err != nil {
		fatal(err)
	}
	rec.HumanReviewed = true
	if err := writeJSON(path, rec); err != nil {
		fatal(err)
	}
	logf("%s marked as human-reviewed", fs.Arg(0))
}

func cmdSite(args []string) {
	fs := flag.NewFlagSet("site", flag.ExitOnError)
	c := addCommon(fs)
	out := fs.String("out", "public", "output directory")
	parse(fs, args)
	stats, err := site.Generate(c.records, *out)
	if err != nil {
		fatal(err)
	}
	logf("wrote registry to %s (%d papers: %d reproduced, %d partial, %d not reproduced, %d could not run)",
		*out, stats.Total, stats.Reproduced, stats.Partial, stats.NotReproduced, stats.CouldNotRun)
}

func cmdEval(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	c := addCommon(fs)
	tasksFile := fs.String("tasks", "", "tasks JSON file (see README)")
	limit := fs.Int("limit", 0, "run only the first N tasks")
	out := fs.String("out", "eval-report.json", "report file")
	parse(fs, args)
	if *tasksFile == "" {
		fatal(fmt.Errorf("--tasks is required"))
	}
	tasks, err := eval.LoadTasks(*tasksFile)
	if err != nil {
		fatal(err)
	}
	if *limit > 0 && *limit < len(tasks) {
		tasks = tasks[:*limit]
	}
	rep := eval.Run(ctx, c.llm(), c.model, tasks, c.image, c.newSandbox, c.agentCfg(), logf)
	if err := writeJSON(*out, rep); err != nil {
		fatal(err)
	}
	logf("model %s solved %d/%d tasks, claim accuracy %.1f%% (report: %s)",
		rep.Model, rep.TasksSolved, len(rep.Tasks), rep.ClaimAccuracy*100, *out)
}
