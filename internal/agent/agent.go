// Package agent drives an LLM through building an environment for a paper's
// code and finding commands that print its headline numbers.
//
// The model never reports numbers. It proposes commands; the harness runs
// them itself and extracts the values, so results can't be hallucinated.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/itskrsna/Crucible/internal/llm"
	"github.com/itskrsna/Crucible/internal/sandbox"
	"github.com/itskrsna/Crucible/internal/schemas"
)

type Config struct {
	MaxSteps    int
	StepTimeout time.Duration
	RunTimeout  time.Duration
}

func (c Config) withDefaults() Config {
	if c.MaxSteps <= 0 {
		c.MaxSteps = 40
	}
	if c.StepTimeout <= 0 {
		c.StepTimeout = 10 * time.Minute
	}
	if c.RunTimeout <= 0 {
		c.RunTimeout = 30 * time.Minute
	}
	return c
}

type reply struct {
	Thought string    `json:"thought"`
	Command string    `json:"command"`
	Setup   bool      `json:"setup"`
	Done    bool      `json:"done"`
	GiveUp  bool      `json:"give_up"`
	Reason  string    `json:"reason"`
	Runs    []runSpec `json:"runs"`
}

type runSpec struct {
	ClaimID string `json:"claim_id"`
	Command string `json:"command"`
	Pattern string `json:"pattern"`
}

type Step struct {
	Thought  string `json:"thought"`
	Command  string `json:"command"`
	Setup    bool   `json:"setup"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out,omitempty"`
}

type Measurement struct {
	ClaimID  string                `json:"claim_id"`
	Command  string                `json:"command,omitempty"`
	Pattern  string                `json:"pattern,omitempty"`
	Output   string                `json:"output,omitempty"`
	ExitCode int                   `json:"exit_code"`
	Value    *float64              `json:"value,omitempty"`
	Reason   schemas.FailureReason `json:"reason,omitempty"`
	Note     string                `json:"note,omitempty"`
}

type Outcome struct {
	Dockerfile   string        `json:"dockerfile"`
	Steps        []Step        `json:"steps"`
	Measurements []Measurement `json:"measurements"`
}

const systemPrompt = `You are reproducing results from a machine learning paper inside a Linux sandbox (Debian, Python 3.11, bash, git, gcc and make are already installed; CPU only, no GPU).
CPU-only PyTorch, numpy, pandas, scipy, scikit-learn, matplotlib, einops, tqdm and pyyaml are PRE-INSTALLED, so do not reinstall them. When installing a repo's requirements, drop the lines for those packages first, for example:
  grep -viE "^(torch|numpy|pandas|scipy|scikit-learn|matplotlib|einops|tqdm|pyyaml)" requirements.txt > /tmp/req.txt && pip install -r /tmp/req.txt
and never install CUDA or other GPU packages.
You cannot use apt, sudo or any system package installer: all privileges are dropped. Use pip (or conda-free alternatives) for dependencies, and do not try to install git.
Reply with exactly one JSON object each turn, one of:
  {"thought":"why","command":"bash command","setup":true|false}   run a command
  {"done":true,"runs":[{"claim_id":"c1","command":"...","pattern":"..."}]}   finish
  {"give_up":true,"reason":"missing_data|broken_dependencies|needs_more_compute|missing_checkpoint|other"}

Rules:
- Every command runs in a fresh bash starting in /work; only files persist. Use "cd /work/repo && ..." in every command.
- Clone the repository into /work/repo and check out the pinned commit if one is given.
- Set "setup":true for commands that are part of building the environment (clone, install, download data/checkpoints). Exploration like ls or cat is setup:false. Setup commands are replayed in a Dockerfile, so they must be repeatable and non-interactive. IMPORTANT: any command that creates files your final measurement command depends on (training, checkpoints, preprocessing, downloaded data) MUST be setup:true. The record is rebuilt from the Dockerfile in a fresh container, and a measurement that needs a checkpoint nobody can recreate cannot be replayed.
- Install CPU-only builds (for PyTorch use --index-url https://download.pytorch.org/whl/cpu).
- Each command is limited in time; prefer the smallest evaluation (one dataset, one horizon) that prints the claimed metric. Never edit evaluation code to change results. Use the repository's own default configuration, including its default random seed and the hyperparameters in its provided scripts. Do NOT search over seeds or hyperparameters to get closer to the claimed number: choosing the best of several runs is cherry-picking and invalidates the result. If you run more than one seed, the measurement must use the repository's default one.
- When ready, finish with one run per claim: "command" prints the metric, and "pattern" is a Go (RE2) regex with ONE capture group matching the number, e.g. "mse:\\s*([0-9.]+)". The harness runs your command itself and extracts the number; do not state numbers yourself. The command must itself COMPUTE the metric (run the evaluation or training script and print/grep its metric line). A command that echoes or hard-codes a value, or only reads back a result file you wrote, is rejected.
- If the task cannot be done on CPU in this sandbox, give up with the closest reason.`

func taskPrompt(paper schemas.Paper, claims []schemas.Claim) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Paper: %s (arXiv %s)\nRepository: %s\n", paper.Title, paper.ArxivID, paper.RepoURL)
	if paper.RepoCommit != "" {
		fmt.Fprintf(&sb, "Pinned commit: %s\n", paper.RepoCommit)
	}
	sb.WriteString("Claims to reproduce:\n")
	for _, c := range claims {
		fmt.Fprintf(&sb, "- %s: %s on %s (%s) = %v (%s)\n", c.ID, c.Metric, c.Dataset, c.Setting, c.Value, c.Source)
	}
	return sb.String()
}

// wrapUpSteps is how many steps before the budget ends the agent is told to
// finish. Without it, a model that keeps exploring can run out of steps with
// good measurements it never reported.
const wrapUpSteps = 4

// addNote appends text to the latest user message, or adds a user message if
// the conversation ends with the assistant. This keeps roles alternating,
// which some providers require.
func addNote(msgs []llm.Message, note string) []llm.Message {
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		msgs[n-1].Content += "\n\n" + note
		return msgs
	}
	return append(msgs, llm.Message{Role: "user", Content: note})
}

const (
	keepRecent = 6   // newest messages stay untouched
	oldMsgMax  = 500 // older command outputs are cut to this many characters
)

// compactHistory shrinks old command outputs before each model call. Every
// step re-sends the whole conversation, so long logs from early steps would
// otherwise be paid for again on every later step. The system prompt, task
// description and the newest messages are left intact.
func compactHistory(msgs []llm.Message) []llm.Message {
	out := make([]llm.Message, len(msgs))
	copy(out, msgs)
	for i := 2; i < len(out)-keepRecent; i++ {
		if out[i].Role == "user" && len(out[i].Content) > oldMsgMax {
			out[i].Content = truncate(out[i].Content, oldMsgMax)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n/2] + "\n[...truncated...]\n" + s[len(s)-n/2:]
}

func reasonFrom(s string) schemas.FailureReason {
	switch r := schemas.FailureReason(strings.TrimSpace(s)); r {
	case schemas.MissingData, schemas.BrokenDependencies, schemas.NeedsMoreCompute, schemas.MissingCheckpoint:
		return r
	}
	return schemas.OtherFailure
}

// Dockerfile replays successful setup steps so anyone can rebuild the environment.
func Dockerfile(image string, steps []Step) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\nWORKDIR /work\n", image)
	for _, s := range steps {
		if !s.Setup || s.ExitCode != 0 || s.TimedOut {
			continue
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode([]string{"bash", "-lc", s.Command})
		fmt.Fprintf(&b, "RUN %s", buf.String())
	}
	return b.String()
}

// ExtractNumber returns the last number captured by pattern in output.
func ExtractNumber(output, pattern string) (float64, error) {
	v, _, err := extract(output, pattern)
	return v, err
}

// extract also returns the exact text that was parsed, so callers can check
// where the number came from.
func extract(output, pattern string) (float64, string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return 0, "", fmt.Errorf("bad pattern: %w", err)
	}
	if re.NumSubexp() < 1 {
		return 0, "", fmt.Errorf("pattern has no capture group")
	}
	ms := re.FindAllStringSubmatch(output, -1)
	if len(ms) == 0 {
		return 0, "", fmt.Errorf("pattern did not match output")
	}
	text := ms[len(ms)-1][1]
	v, err := strconv.ParseFloat(text, 64)
	return v, text, err
}

// hardCoded reports whether a measurement command already contains the number
// it supposedly measured, e.g. `echo "MSE = 0.361"`. Such a command proves
// nothing: the model could write any value. Short numbers are ignored because
// they collide with flags like --seq_len 96.
func hardCoded(command, matched string) bool {
	return len(matched) >= 4 && strings.Contains(command, matched)
}

func allFailed(claims []schemas.Claim, reason schemas.FailureReason, note string) []Measurement {
	ms := make([]Measurement, len(claims))
	for i, c := range claims {
		ms[i] = Measurement{ClaimID: c.ID, Reason: reason, Note: note}
	}
	return ms
}

// Run lets the model build the environment, then verifies its chosen commands itself.
func Run(ctx context.Context, ch llm.Chatter, ex sandbox.Executor, image string, paper schemas.Paper, claims []schemas.Claim, cfg Config) (*Outcome, error) {
	cfg = cfg.withDefaults()
	out := &Outcome{}
	msgs := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: taskPrompt(paper, claims)},
	}

	const maxRejections = 2
	rejections := 0
	for i := 0; i < cfg.MaxSteps; i++ {
		if cfg.MaxSteps-i == wrapUpSteps && cfg.MaxSteps > wrapUpSteps {
			msgs = addNote(msgs, fmt.Sprintf(
				"NOTICE: only %d steps remain. If you already have metrics from a real run, finish NOW with "+
					`{"done":true,"runs":[...]}. A run that never finishes counts as failed.`, wrapUpSteps))
		}
		var r reply
		if err := llm.ChatJSON(ctx, ch, compactHistory(msgs), &r); err != nil {
			return out, err
		}
		echo, _ := json.Marshal(r)
		msgs = append(msgs, llm.Message{Role: "assistant", Content: string(echo)})

		switch {
		case r.Done:
			ms, bad := verify(ctx, ex, claims, r.Runs, cfg)
			if len(bad) > 0 && rejections < maxRejections && i+1 < cfg.MaxSteps {
				rejections++
				msgs = append(msgs, llm.Message{Role: "user", Content: fmt.Sprintf(
					"Rejected for claims %s: the number you want measured is written inside your command, which proves nothing. "+
						"Give a command that COMPUTES the metric (run the evaluation or training script and print/grep its metric line), "+
						"with a pattern for that output. Do not echo or hard-code values.", strings.Join(bad, ", "))})
				continue
			}
			out.Dockerfile = Dockerfile(image, out.Steps)
			out.Measurements = ms
			return out, nil
		case r.GiveUp:
			out.Dockerfile = Dockerfile(image, out.Steps)
			out.Measurements = allFailed(claims, reasonFrom(r.Reason), "agent gave up")
			return out, nil
		case strings.TrimSpace(r.Command) != "":
			res, err := ex.Exec(ctx, r.Command, cfg.StepTimeout)
			if err != nil {
				return out, err
			}
			out.Steps = append(out.Steps, Step{
				Thought: r.Thought, Command: r.Command, Setup: r.Setup,
				Output: res.Output, ExitCode: res.ExitCode, TimedOut: res.TimedOut,
			})
			status := fmt.Sprintf("exit code %d", res.ExitCode)
			if res.TimedOut {
				status = "timed out"
			}
			msgs = append(msgs, llm.Message{Role: "user", Content: status + "\n" + truncate(res.Output, 4000)})
		default:
			msgs = append(msgs, llm.Message{Role: "user", Content: `Reply with a command, {"done":true,...} or {"give_up":true,...}.`})
		}
	}

	out.Dockerfile = Dockerfile(image, out.Steps)
	out.Measurements = allFailed(claims, schemas.OtherFailure, "step budget exhausted")
	return out, nil
}

// verify runs the model's chosen commands itself. It returns the claim IDs
// whose command was rejected for hard-coding its number, so the agent can retry.
func verify(ctx context.Context, ex sandbox.Executor, claims []schemas.Claim, runs []runSpec, cfg Config) ([]Measurement, []string) {
	byClaim := map[string]runSpec{}
	for _, r := range runs {
		byClaim[r.ClaimID] = r
	}
	var ms []Measurement
	var problems []string
	for _, c := range claims {
		spec, ok := byClaim[c.ID]
		if !ok || strings.TrimSpace(spec.Command) == "" {
			ms = append(ms, Measurement{ClaimID: c.ID, Reason: schemas.OtherFailure, Note: "agent provided no run for this claim"})
			continue
		}
		m := Measurement{ClaimID: c.ID, Command: spec.Command, Pattern: spec.Pattern}
		res, err := ex.Exec(ctx, spec.Command, cfg.RunTimeout)
		m.Output, m.ExitCode = res.Output, res.ExitCode
		switch {
		case err != nil:
			m.Reason, m.Note = schemas.OtherFailure, err.Error()
		case res.TimedOut:
			m.Reason, m.Note = schemas.NeedsMoreCompute, "run exceeded the time limit"
		default:
			v, text, perr := extract(res.Output, spec.Pattern)
			switch {
			case perr != nil:
				m.Reason, m.Note = schemas.OtherFailure, perr.Error()
			case hardCoded(spec.Command, text):
				m.Reason, m.Note = schemas.OtherFailure, "rejected: the measured number is hard-coded in the command"
				problems = append(problems, c.ID)
			default:
				m.Value = &v
			}
		}
		ms = append(ms, m)
	}
	return ms, problems
}
