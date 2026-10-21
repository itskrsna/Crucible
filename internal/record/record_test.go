package record

import (
	"path/filepath"
	"testing"

	"github.com/itskrsna/Crucible/internal/schemas"
)

func rec(verdicts ...schemas.Verdict) schemas.Record {
	r := schemas.Record{Paper: schemas.Paper{ArxivID: "cs/0601001"}}
	for _, v := range verdicts {
		r.Results = append(r.Results, schemas.ClaimResult{Verdict: v})
	}
	return r
}

func TestOverall(t *testing.T) {
	tests := []struct {
		rec  schemas.Record
		want string
	}{
		{rec(), "unverified"},
		{rec(schemas.Reproduced, schemas.Reproduced), "reproduced"},
		{rec(schemas.Reproduced, schemas.NotReproduced), "not reproduced"},
		{rec(schemas.CouldNotRun, schemas.CouldNotRun), "could not run"},
		{rec(schemas.Reproduced, schemas.Partial), "partially reproduced"},
		{rec(schemas.Reproduced, schemas.CouldNotRun), "partially reproduced"},
	}
	for _, tt := range tests {
		if got := Overall(tt.rec); got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
}

func TestNegativeResultsNeedHumanReview(t *testing.T) {
	neg := rec(schemas.NotReproduced)
	if Publishable(neg) {
		t.Error("unreviewed negative record must not be publishable")
	}
	neg.HumanReviewed = true
	if !Publishable(neg) {
		t.Error("reviewed negative record should be publishable")
	}
	if !Publishable(rec(schemas.Reproduced, schemas.Partial)) {
		t.Error("positive record needs no review")
	}
}

func TestWriteAndLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	r := rec(schemas.Reproduced)
	r.Dockerfile = "FROM x\n"
	if err := Write(root, r, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(filepath.Join(Dir(root, "cs/0601001"), "record.json"))
	if err != nil || got.Dockerfile != "FROM x\n" {
		t.Fatalf("%+v %v", got, err)
	}
	all, _ := LoadAll(root)
	if len(all) != 1 {
		t.Errorf("LoadAll found %d", len(all))
	}
}
