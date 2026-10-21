package compare

import (
	"testing"

	"github.com/itskrsna/crucible/internal/schemas"
)

var claim = schemas.Claim{ID: "c1", Metric: "MSE", Dataset: "ETTh1", Setting: "H=96", Value: 0.380, Source: "Table 2"}

func ptr(f float64) *float64 { return &f }

func TestJudge(t *testing.T) {
	tests := []struct {
		name     string
		measured *float64
		want     schemas.Verdict
	}{
		{"close match", ptr(0.381), schemas.Reproduced},
		{"small gap", ptr(0.400), schemas.Partial},
		{"large gap", ptr(0.500), schemas.NotReproduced},
		{"no measurement", nil, schemas.CouldNotRun},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Judge(claim, tt.measured, "", "").Verdict; got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestBeatsBaseline(t *testing.T) {
	c := claim
	if BeatsBaseline(c, ptr(0.4)) != nil {
		t.Error("no baseline should give nil")
	}
	c.BestBaseline = ptr(0.390)
	if got := BeatsBaseline(c, ptr(0.380)); got == nil || !*got {
		t.Error("lower MSE than baseline should win")
	}
	if got := BeatsBaseline(c, ptr(0.400)); got == nil || *got {
		t.Error("higher MSE than baseline should lose")
	}
	acc := schemas.Claim{Metric: "Accuracy", Value: 0.9, BestBaseline: ptr(0.8)}
	if got := BeatsBaseline(acc, ptr(0.85)); got == nil || !*got {
		t.Error("higher accuracy should win")
	}
}

func TestJudgeKeepsFailureReason(t *testing.T) {
	got := Judge(claim, nil, "", schemas.MissingData)
	if got.Reason != schemas.MissingData {
		t.Errorf("got reason %q", got.Reason)
	}
}
