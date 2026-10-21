package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/itskrsna/Crucible/internal/llm"
	"github.com/itskrsna/Crucible/internal/sandbox"
	"github.com/itskrsna/Crucible/internal/schemas"
)

type scripted struct {
	replies []string
	i       int
}

func (s *scripted) Chat(context.Context, []llm.Message, bool) (string, error) {
	r := s.replies[s.i]
	s.i++
	return r, nil
}

type fakeExec struct{ ran []string }

func (f *fakeExec) Exec(_ context.Context, cmd string, _ time.Duration) (sandbox.Result, error) {
	f.ran = append(f.ran, cmd)
	if strings.Contains(cmd, "eval.py") {
		return sandbox.Result{Output: "epoch 1\nmse: 0.381\nmae: 0.402\n"}, nil
	}
	return sandbox.Result{}, nil
}

func (f *fakeExec) Close(context.Context) error { return nil }

var claims = []schemas.Claim{{ID: "c1", Metric: "MSE", Dataset: "ETTh1", Value: 0.379}}

func TestRunMeasuresNumbersItself(t *testing.T) {
	ch := &scripted{replies: []string{
		`{"thought":"clone","command":"git clone https://github.com/a/b /work/repo","setup":true}`,
		`{"thought":"look","command":"ls /work/repo","setup":false}`,
		`{"done":true,"runs":[{"claim_id":"c1","command":"cd /work/repo && python eval.py","pattern":"mse:\\s*([0-9.]+)"}]}`,
	}}
	ex := &fakeExec{}
	out, err := Run(context.Background(), ch, ex, "python:3.11", schemas.Paper{}, claims, Config{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.Measurements[0]
	if m.Value == nil || *m.Value != 0.381 {
		t.Fatalf("measurement %+v", m)
	}
	if !strings.Contains(out.Dockerfile, "git clone") || strings.Contains(out.Dockerfile, "ls /work") {
		t.Errorf("dockerfile should keep only setup steps:\n%s", out.Dockerfile)
	}
}

type echoExec struct{ fakeExec }

// Exec behaves like a shell: `echo` prints its argument, eval.py computes 0.381.
func (e *echoExec) Exec(ctx context.Context, cmd string, t time.Duration) (sandbox.Result, error) {
	if strings.HasPrefix(cmd, "echo ") {
		return sandbox.Result{Output: strings.TrimPrefix(cmd, "echo ") + "\n"}, nil
	}
	return e.fakeExec.Exec(ctx, cmd, t)
}

func TestHardCodedNumberIsRejectedThenRetried(t *testing.T) {
	ch := &scripted{replies: []string{
		`{"done":true,"runs":[{"claim_id":"c1","command":"echo mse: 0.381","pattern":"mse:\\s*([0-9.]+)"}]}`,
		`{"done":true,"runs":[{"claim_id":"c1","command":"cd /work/repo && python eval.py","pattern":"mse:\\s*([0-9.]+)"}]}`,
	}}
	out, err := Run(context.Background(), ch, &echoExec{}, "img", schemas.Paper{}, claims, Config{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.Measurements[0]
	if m.Value == nil || *m.Value != 0.381 || !strings.Contains(m.Command, "eval.py") {
		t.Fatalf("expected the computed measurement to win, got %+v", m)
	}
	if ch.i != 2 {
		t.Errorf("agent should have been asked again exactly once, calls=%d", ch.i)
	}
}

func TestHardCodedNumberFailsAfterRepeatedRejections(t *testing.T) {
	echo := `{"done":true,"runs":[{"claim_id":"c1","command":"echo mse: 0.381","pattern":"mse:\\s*([0-9.]+)"}]}`
	ch := &scripted{replies: []string{echo, echo, echo}}
	out, err := Run(context.Background(), ch, &echoExec{}, "img", schemas.Paper{}, claims, Config{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.Measurements[0]
	if m.Value != nil || !strings.Contains(m.Note, "hard-coded") {
		t.Fatalf("a hard-coded number must never become a measurement: %+v", m)
	}
}

func TestCompactHistoryShrinksOnlyOldOutputs(t *testing.T) {
	long := strings.Repeat("x", 3000)
	msgs := []llm.Message{{Role: "system", Content: long}, {Role: "user", Content: long}}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, llm.Message{Role: "assistant", Content: "a"}, llm.Message{Role: "user", Content: long})
	}
	got := compactHistory(msgs)
	if len(got[0].Content) != 3000 || len(got[1].Content) != 3000 {
		t.Error("system prompt and task description must stay intact")
	}
	if len(got[3].Content) >= 3000 {
		t.Error("an old command output should be shortened")
	}
	if len(got[len(got)-1].Content) != 3000 {
		t.Error("the newest messages must stay intact")
	}
	if len(msgs[3].Content) != 3000 {
		t.Error("the original history must not be modified")
	}
}

// recording captures what the model is shown on each call.
type recording struct {
	scripted
	seen [][]llm.Message
}

func (r *recording) Chat(ctx context.Context, msgs []llm.Message, j bool) (string, error) {
	r.seen = append(r.seen, append([]llm.Message{}, msgs...))
	return r.scripted.Chat(ctx, msgs, j)
}

func TestAgentIsToldToWrapUpBeforeBudgetEnds(t *testing.T) {
	var replies []string
	for i := 0; i < 8; i++ {
		replies = append(replies, `{"command":"echo exploring"}`)
	}
	ch := &recording{scripted: scripted{replies: replies}}
	if _, err := Run(context.Background(), ch, &fakeExec{}, "img", schemas.Paper{}, claims, Config{MaxSteps: 8}); err != nil {
		t.Fatal(err)
	}
	notice := func(call int) bool {
		last := ch.seen[call][len(ch.seen[call])-1]
		return last.Role == "user" && strings.Contains(last.Content, "steps remain")
	}
	if notice(3) {
		t.Error("too early: no notice expected 5 steps before the end")
	}
	if !notice(4) {
		t.Error("the agent should be told to wrap up with 4 steps left")
	}
	for i := 1; i < len(ch.seen[7]); i++ {
		if ch.seen[7][i].Role == ch.seen[7][i-1].Role && ch.seen[7][i].Role != "system" {
			t.Fatalf("roles must alternate, got two %q in a row at %d", ch.seen[7][i].Role, i)
		}
	}
}

func TestHardCoded(t *testing.T) {
	if !hardCoded(`echo "MSE = 0.3611617"`, "0.3611617") {
		t.Error("literal number in command should be flagged")
	}
	if hardCoded("python run.py --seq_len 96", "96") {
		t.Error("short numbers collide with flags and must be ignored")
	}
	if hardCoded("python eval.py", "0.381") {
		t.Error("computed number is fine")
	}
}

func TestGiveUpMarksClaimsCouldNotRun(t *testing.T) {
	ch := &scripted{replies: []string{`{"give_up":true,"reason":"missing_data"}`}}
	out, err := Run(context.Background(), ch, &fakeExec{}, "img", schemas.Paper{}, claims, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if m := out.Measurements[0]; m.Value != nil || m.Reason != schemas.MissingData {
		t.Errorf("%+v", m)
	}
}

func TestStepBudgetExhausted(t *testing.T) {
	ch := &scripted{replies: []string{`{"command":"echo 1"}`, `{"command":"echo 2"}`}}
	out, err := Run(context.Background(), ch, &fakeExec{}, "img", schemas.Paper{}, claims, Config{MaxSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.Measurements[0].Note != "step budget exhausted" {
		t.Errorf("%+v", out.Measurements[0])
	}
}

func TestExtractNumber(t *testing.T) {
	if v, err := ExtractNumber("a 1.5\nb 2.5", `b ([0-9.]+)`); err != nil || v != 2.5 {
		t.Errorf("%v %v", v, err)
	}
	if _, err := ExtractNumber("x", `(`); err == nil {
		t.Error("bad regex should error")
	}
	if _, err := ExtractNumber("x", `no group`); err == nil {
		t.Error("missing group should error")
	}
	if _, err := ExtractNumber("x", `y([0-9]+)`); err == nil {
		t.Error("no match should error")
	}
}

func TestDockerfileEscapesCommands(t *testing.T) {
	df := Dockerfile("img", []Step{{Command: "echo \"a\" && cd <x>", Setup: true}})
	want := `RUN ["bash","-lc","echo \"a\" && cd <x>"]`
	if !strings.Contains(df, want) {
		t.Errorf("got:\n%s", df)
	}
}
