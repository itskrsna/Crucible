package site

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itskrsna/Crucible/internal/record"
	"github.com/itskrsna/Crucible/internal/schemas"
)

func yes() *bool { b := true; return &b }

func TestGeneratePublishesOnlyReviewedNegatives(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	good := schemas.Record{
		Paper:   schemas.Paper{ArxivID: "2501.00001", Title: "Good <Paper>"},
		Results: []schemas.ClaimResult{{Verdict: schemas.Reproduced, BeatsBaselines: yes()}},
	}
	bad := schemas.Record{
		Paper:   schemas.Paper{ArxivID: "2501.00002", Title: "Bad Paper"},
		Results: []schemas.ClaimResult{{Verdict: schemas.NotReproduced}},
	}
	for _, r := range []schemas.Record{good, bad} {
		if err := record.Write(root, r, nil); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := Generate(root, out)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || stats.Reproduced != 1 || stats.BeatBaselines != 1 {
		t.Errorf("stats %+v", stats)
	}
	if _, err := os.Stat(filepath.Join(out, "2501.00002.html")); err == nil {
		t.Error("unreviewed negative record was published")
	}
	idx, _ := os.ReadFile(filepath.Join(out, "index.html"))
	if !strings.Contains(string(idx), "Good &lt;Paper&gt;") {
		t.Error("title should be HTML-escaped in index")
	}
	if _, err := os.Stat(filepath.Join(out, "badges", "2501.00001.svg")); err != nil {
		t.Error("badge missing")
	}
}

func TestPageShowsLimitationsAndDoesNotOverclaimReplay(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	r := schemas.Record{
		Paper:       schemas.Paper{ArxivID: "2501.00003", Title: "Limited"},
		Results:     []schemas.ClaimResult{{Verdict: schemas.Reproduced}},
		Limitations: []string{"single seed <only>"},
	}
	if err := record.Write(root, r, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(root, out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(out, "2501.00003.html"))
	page := string(b)
	if !strings.Contains(page, "single seed &lt;only&gt;") {
		t.Error("limitations must be listed and HTML-escaped")
	}
	if !strings.Contains(page, "Not replay-verified") || strings.Contains(page, "reproduced its measurements") {
		t.Error("an unverified record must not claim it can be replayed")
	}

	r.ReplayVerified = true
	if err := record.Write(root, r, nil); err != nil {
		t.Fatal(err)
	}
	Generate(root, out)
	b, _ = os.ReadFile(filepath.Join(out, "2501.00003.html"))
	if !strings.Contains(string(b), "reproduced its measurements") || strings.Contains(string(b), "Not replay-verified") {
		t.Error("a verified record should say so")
	}
}

func TestBadgeEscapesText(t *testing.T) {
	if svg := Badge(`<x>`); strings.Contains(svg, "<x>") {
		t.Error("badge text must be escaped")
	}
}
