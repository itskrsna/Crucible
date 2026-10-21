package pipeline

import (
	"testing"

	"github.com/itskrsna/Crucible/internal/agent"
	"github.com/itskrsna/Crucible/internal/schemas"
)

func TestBuildRecordJudgesAndFlagsBaselines(t *testing.T) {
	base := 0.390
	cs := []schemas.Claim{
		{ID: "c1", Metric: "MSE", Dataset: "ETTh1", Value: 0.380, BestBaseline: &base},
		{ID: "c2", Metric: "MAE", Dataset: "ETTh1", Value: 0.400},
	}
	v := 0.381
	outcome := &agent.Outcome{
		Dockerfile: "FROM x\n",
		Measurements: []agent.Measurement{
			{ClaimID: "c1", Command: "run", Pattern: "(x)", Value: &v},
			{ClaimID: "c2", Reason: schemas.MissingData, Note: "agent gave up"},
		},
	}
	rec := BuildRecord(schemas.Paper{ArxivID: "1"}, cs, outcome, "m", "img")

	if got := rec.Results[0]; got.Verdict != schemas.Reproduced || got.BeatsBaselines == nil || !*got.BeatsBaselines || got.Pattern != "(x)" {
		t.Errorf("c1: %+v", got)
	}
	if got := rec.Results[1]; got.Verdict != schemas.CouldNotRun || got.Reason != schemas.MissingData {
		t.Errorf("c2: %+v", got)
	}
}
