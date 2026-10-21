// Package compare checks measured numbers against claimed ones and assigns verdicts.
package compare

import (
	"fmt"
	"math"
	"strings"

	"github.com/itskrsna/Crucible/internal/schemas"
)

// Relative tolerances: within Strict is reproduced, within Loose is partial.
const (
	DefaultStrict = 0.02
	DefaultLoose  = 0.10
)

var lowerIsBetter = []string{"mse", "mae", "rmse", "mape", "smape", "loss", "error", "perplexity"}

// BeatsBaseline reports whether measured beats the claim's best reported
// baseline, or nil when there is nothing to compare.
func BeatsBaseline(claim schemas.Claim, measured *float64) *bool {
	if claim.BestBaseline == nil || measured == nil {
		return nil
	}
	lower := false
	m := strings.ToLower(claim.Metric)
	for _, k := range lowerIsBetter {
		if strings.Contains(m, k) {
			lower = true
		}
	}
	beats := *measured > *claim.BestBaseline
	if lower {
		beats = *measured < *claim.BestBaseline
	}
	return &beats
}

// Judge compares a measurement (nil if the run produced none) with a claim.
func Judge(claim schemas.Claim, measured *float64, command string, reason schemas.FailureReason) schemas.ClaimResult {
	if measured == nil {
		if reason == "" {
			reason = schemas.OtherFailure
		}
		return schemas.ClaimResult{Claim: claim, Verdict: schemas.CouldNotRun, Command: command, Reason: reason}
	}

	denom := math.Abs(claim.Value)
	if denom == 0 {
		denom = 1
	}
	relErr := math.Abs(*measured-claim.Value) / denom

	verdict := schemas.NotReproduced
	switch {
	case relErr <= DefaultStrict:
		verdict = schemas.Reproduced
	case relErr <= DefaultLoose:
		verdict = schemas.Partial
	}
	return schemas.ClaimResult{
		Claim:    claim,
		Verdict:  verdict,
		Measured: measured,
		Command:  command,
		Note:     fmt.Sprintf("relative error %.1f%%", relErr*100),
	}
}
