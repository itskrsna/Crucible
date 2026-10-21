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
