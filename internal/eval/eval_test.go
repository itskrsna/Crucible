package eval

import (
	"testing"

	"github.com/itskrsna/Crucible/internal/agent"
	"github.com/itskrsna/Crucible/internal/schemas"
)

func f(v float64) *float64 { return &v }

func TestScore(t *testing.T) {
	task := Task{ID: "t", Claims: []schemas.Claim{
		{ID: "c1", Value: 10}, {ID: "c2", Value: 5}, {ID: "c3", Value: 1},
	}}
	got := Score(task, []agent.Measurement{
		{ClaimID: "c1", Value: f(10.1)}, // within 2%
		{ClaimID: "c2", Value: f(6)},    // 20% off
		// c3 missing
	})
	if got.Correct != 1 || got.Total != 3 || got.Solved {
		t.Errorf("%+v", got)
	}
}

func TestScoreSolved(t *testing.T) {
	task := Task{ID: "t", Claims: []schemas.Claim{{ID: "c1", Value: 0}}}
	if !Score(task, []agent.Measurement{{ClaimID: "c1", Value: f(0.01)}}).Solved {
		t.Error("zero truth uses absolute tolerance")
	}
}
