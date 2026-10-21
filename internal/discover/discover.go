// Package discover finds recent time-series forecasting papers whose code is
// likely to be reproducible on a CPU.
package discover

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/itskrsna/Crucible/internal/arxiv"
	"github.com/itskrsna/Crucible/internal/ingest"
	"github.com/itskrsna/Crucible/internal/llm"
)

// Benchmarks are the small, standard long-term-forecasting datasets that fit a CPU.
var Benchmarks = []string{"ETTh1", "ETTh2", "ETTm1", "ETTm2", "Weather", "Electricity"}

type Candidate struct {
	ArxivID       string    `json:"arxiv_id"`
	Title         string    `json:"title"`
	RepoURL       string    `json:"repo_url"`
	Datasets      []string  `json:"datasets"`
	Published     time.Time `json:"published"`
	Score         float64   `json:"score,omitempty"`
	EstCPUMinutes int       `json:"est_cpu_minutes,omitempty"`
	TriageNote    string    `json:"triage_note,omitempty"`
}

type Options struct {
	Limit    int           // stop after this many candidates
	Scan     int           // maximum arXiv entries to look at
	Since    time.Time     // ignore older papers
	Deep     bool          // also fetch LaTeX for papers with no link in the abstract/comment
	Triage   llm.Chatter   // optional LLM runnability scoring
	Delay    time.Duration // pause between arXiv source downloads (arXiv asks for >= 3s)
	Progress func(format string, args ...any)
}

const searchQuery = `cat:cs.LG AND abs:"time series" AND abs:forecasting`

// DetectDatasets lists benchmark names mentioned in the text (case-insensitive, whole-word).
func DetectDatasets(text string) []string {
	var found []string
	for _, b := range Benchmarks {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(b) + `\b`)
		if re.MatchString(text) {
			found = append(found, b)
		}
	}
	return found
}

func (o Options) logf(f string, a ...any) {
	if o.Progress != nil {
		o.Progress(f, a...)
	}
}

func Run(ctx context.Context, hc *http.Client, o Options) ([]Candidate, error) {
	if o.Limit <= 0 {
		o.Limit = 50
	}
	if o.Scan <= 0 {
		o.Scan = 600
	}
	if o.Delay == 0 {
		o.Delay = 3 * time.Second
	}

	var out []Candidate
	for start := 0; start < o.Scan && len(out) < o.Limit; start += 100 {
		entries, err := arxiv.Search(ctx, hc, searchQuery, start, 100)
		if err != nil {
			return out, err
		}
		if len(entries) == 0 {
			break
		}
		for _, e := range entries {
			if len(out) >= o.Limit {
				break
			}
			if !o.Since.IsZero() && e.Published.Before(o.Since) {
				return finish(ctx, hc, out, o), nil
			}
			repo := ingest.FindRepoURL(e.Summary, e.Comment)
			if repo == "" && !o.Deep {
				continue
			}
			time.Sleep(o.Delay)
			latex, err := arxiv.FetchLatex(ctx, hc, e.ID)
			if err != nil {
				o.logf("skip %s: %v", e.ID, err)
				continue
			}
			if repo == "" {
				repo = ingest.FindRepoURL(latex)
			}
			ds := DetectDatasets(latex)
			if repo == "" || len(ds) == 0 {
				continue
			}
			o.logf("candidate %s %s (%s)", e.ID, repo, strings.Join(ds, ","))
			out = append(out, Candidate{ArxivID: e.ID, Title: e.Title, RepoURL: repo, Datasets: ds, Published: e.Published})
		}
		time.Sleep(o.Delay)
	}
	return finish(ctx, hc, out, o), nil
}

func finish(ctx context.Context, hc *http.Client, cs []Candidate, o Options) []Candidate {
	if o.Triage == nil {
		return cs
	}
	for i := range cs {
		score, mins, note, err := triage(ctx, hc, o.Triage, cs[i])
		if err != nil {
			o.logf("triage %s failed: %v", cs[i].ArxivID, err)
			continue
		}
		cs[i].Score, cs[i].EstCPUMinutes, cs[i].TriageNote = score, mins, note
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Score > cs[j].Score })
	return cs
}

var repoPath = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)$`)

func readme(ctx context.Context, hc *http.Client, repoURL string) (string, error) {
	m := repoPath.FindStringSubmatch(repoURL)
	if m == nil {
		return "", fmt.Errorf("bad repo url %q", repoURL)
	}
	u := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/HEAD/README.md", m[1], m[2])
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("README: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 6000))
	return string(b), err
}

const triagePrompt = `You judge whether a research repository's results can be reproduced on a laptop CPU (no GPU) in under 30 minutes.
Reply with JSON: {"score":0.0-1.0,"est_cpu_minutes":integer,"note":"one sentence"}.
Score high when the README shows a simple evaluation or short training command, pretrained checkpoints, small datasets (ETT, Weather, Electricity), and plain PyTorch/NumPy.
Score low when it needs CUDA kernels, GPUs, huge datasets, or has no run instructions.`

func triage(ctx context.Context, hc *http.Client, ch llm.Chatter, c Candidate) (float64, int, string, error) {
	text, err := readme(ctx, hc, c.RepoURL)
	if err != nil {
		return 0, 0, "", err
	}
	var resp struct {
		Score float64 `json:"score"`
		Mins  int     `json:"est_cpu_minutes"`
		Note  string  `json:"note"`
	}
	msgs := []llm.Message{
		{Role: "system", Content: triagePrompt},
		{Role: "user", Content: "README:\n" + text},
	}
	if err := llm.ChatJSON(ctx, ch, msgs, &resp); err != nil {
		return 0, 0, "", err
	}
	if resp.Score < 0 {
		resp.Score = 0
	}
	if resp.Score > 1 {
		resp.Score = 1
	}
	return resp.Score, resp.Mins, resp.Note, nil
}
