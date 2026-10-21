// Package schemas defines the typed data passed between pipeline stages
// and stored in reproducibility records.
package schemas

const SchemaVersion = "0.1"

type Verdict string

const (
	Reproduced    Verdict = "reproduced"
	Partial       Verdict = "partial"
	NotReproduced Verdict = "not_reproduced"
	CouldNotRun   Verdict = "could_not_run"
)

type FailureReason string

const (
	MissingData        FailureReason = "missing_data"
	BrokenDependencies FailureReason = "broken_dependencies"
	NeedsMoreCompute   FailureReason = "needs_more_compute"
	MissingCheckpoint  FailureReason = "missing_checkpoint"
	OtherFailure       FailureReason = "other"
)

type Paper struct {
	ArxivID    string `json:"arxiv_id"`
	Title      string `json:"title"`
	RepoURL    string `json:"repo_url,omitempty"`
	RepoCommit string `json:"repo_commit,omitempty"`
}

type Claim struct {
	ID      string  `json:"id"`
	Metric  string  `json:"metric"`
	Dataset string  `json:"dataset"`
	Setting string  `json:"setting,omitempty"`
	Value   float64 `json:"value"`
	Source  string  `json:"source"` // table or line reference in the paper
	// BestBaseline is the best competing number the paper itself reports for
	// the same setting, if shown. Used to check whether the headline win holds.
	BestBaseline *float64 `json:"best_baseline,omitempty"`
}

type ClaimResult struct {
	Claim    Claim         `json:"claim"`
	Verdict  Verdict       `json:"verdict"`
	Measured *float64      `json:"measured,omitempty"`
	Command  string        `json:"command,omitempty"`
	Pattern  string        `json:"pattern,omitempty"` // regex with one capture group that extracts the number from Command's output
	Reason   FailureReason `json:"reason,omitempty"`
	Note     string        `json:"note,omitempty"`
	// BeatsBaselines reports whether the measured number still beats the
	// paper's own best reported baseline. Nil when it can't be judged.
	BeatsBaselines *bool `json:"beats_baselines,omitempty"`
}

type Record struct {
	SchemaVersion string        `json:"schema_version"`
	Paper         Paper         `json:"paper"`
	Results       []ClaimResult `json:"results"`
	Dockerfile    string        `json:"dockerfile,omitempty"`
	Model         string        `json:"model,omitempty"`
	Steps         int           `json:"steps,omitempty"`
	// Limitations are caveats a reader needs to interpret the verdicts, shown
	// on the record's page. A green badge alone would overstate what was checked.
	Limitations []string `json:"limitations,omitempty"`
	// ReplayVerified is true only if `crucible rerun` was run on this record and
	// reproduced its measurements from a fresh container.
	ReplayVerified bool `json:"replay_verified"`
	HumanReviewed  bool `json:"human_reviewed"`
}
