// Package record reads, writes and classifies reproducibility records.
package record

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/itskrsna/Crucible/internal/schemas"
)

// Slug makes an arXiv id safe for file names (old-style ids contain "/").
func Slug(arxivID string) string { return strings.ReplaceAll(arxivID, "/", "_") }

func Dir(root, arxivID string) string { return filepath.Join(root, Slug(arxivID)) }

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Write stores record.json, Dockerfile and (optionally) transcript.json under root/<id>/.
func Write(root string, rec schemas.Record, transcript any) error {
	dir := Dir(root, rec.Paper.ArxivID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "record.json"), rec); err != nil {
		return err
	}
	if rec.Dockerfile != "" {
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(rec.Dockerfile), 0o644); err != nil {
			return err
		}
	}
	if transcript != nil {
		return writeJSON(filepath.Join(dir, "transcript.json"), transcript)
	}
	return nil
}

func Load(path string) (schemas.Record, error) {
	var rec schemas.Record
	b, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, fmt.Errorf("record: parse %s: %w", path, err)
	}
	return rec, nil
}

// LoadAll loads every root/*/record.json, skipping unreadable ones.
func LoadAll(root string) ([]schemas.Record, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*", "record.json"))
	if err != nil {
		return nil, err
	}
	var recs []schemas.Record
	for _, p := range paths {
		if rec, err := Load(p); err == nil {
			recs = append(recs, rec)
		}
	}
	return recs, nil
}

// Overall summarises a record's claim verdicts in one phrase.
func Overall(rec schemas.Record) string {
	var reproduced, partial, notRepro, couldNot int
	for _, r := range rec.Results {
		switch r.Verdict {
		case schemas.Reproduced:
			reproduced++
		case schemas.Partial:
			partial++
		case schemas.NotReproduced:
			notRepro++
		case schemas.CouldNotRun:
			couldNot++
		}
	}
	switch {
	case len(rec.Results) == 0:
		return "unverified"
	case notRepro > 0:
		return "not reproduced"
	case reproduced == len(rec.Results):
		return "reproduced"
	case reproduced == 0 && partial == 0:
		return "could not run"
	default:
		return "partially reproduced"
	}
}

// NeedsReview is true when a record contains a negative verdict. Negative
// results are never published until a human has checked them, because the
// failure may be the agent's fault rather than the paper's.
func NeedsReview(rec schemas.Record) bool {
	for _, r := range rec.Results {
		if r.Verdict == schemas.NotReproduced || r.Verdict == schemas.CouldNotRun {
			return true
		}
	}
	return false
}

func Publishable(rec schemas.Record) bool {
	return !NeedsReview(rec) || rec.HumanReviewed
}
