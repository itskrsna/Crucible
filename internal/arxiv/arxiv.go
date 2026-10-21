// Package arxiv talks to the arXiv API and fetches paper sources.
package arxiv

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const userAgent = "crucible/0.1 (open-source reproducibility checker)"

type Entry struct {
	ID        string // without version, e.g. 2501.12345
	Title     string
	Summary   string
	Comment   string
	Published time.Time
}

type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string `xml:"id"`
	Title     string `xml:"title"`
	Summary   string `xml:"summary"`
	Published string `xml:"published"`
	Comment   string `xml:"http://arxiv.org/schemas/atom comment"`
}

var versionSuffix = regexp.MustCompile(`v\d+$`)

// NormalizeID turns an abs URL or versioned id into a bare id.
func NormalizeID(raw string) string {
	raw = strings.TrimSpace(raw)
	for _, p := range []string{"https://arxiv.org/abs/", "http://arxiv.org/abs/", "arXiv:", "arxiv:"} {
		raw = strings.TrimPrefix(raw, p)
	}
	return versionSuffix.ReplaceAllString(raw, "")
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func ParseFeed(r io.Reader) ([]Entry, error) {
	var feed atomFeed
	if err := xml.NewDecoder(r).Decode(&feed); err != nil {
		return nil, fmt.Errorf("arxiv: parse feed: %w", err)
	}
	var out []Entry
	for _, e := range feed.Entries {
		pub, _ := time.Parse(time.RFC3339, strings.TrimSpace(e.Published))
		out = append(out, Entry{
			ID:        NormalizeID(e.ID),
			Title:     collapse(e.Title),
			Summary:   collapse(e.Summary),
			Comment:   collapse(e.Comment),
			Published: pub,
		})
	}
	return out, nil
}

func get(ctx context.Context, hc *http.Client, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arxiv: GET %s: %s", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func query(ctx context.Context, hc *http.Client, params url.Values) ([]Entry, error) {
	b, err := get(ctx, hc, "https://export.arxiv.org/api/query?"+params.Encode(), 32<<20)
	if err != nil {
		return nil, err
	}
	return ParseFeed(bytes.NewReader(b))
}

// Search returns entries matching an arXiv search_query, newest first.
func Search(ctx context.Context, hc *http.Client, searchQuery string, start, max int) ([]Entry, error) {
	return query(ctx, hc, url.Values{
		"search_query": {searchQuery},
		"start":        {fmt.Sprint(start)},
		"max_results":  {fmt.Sprint(max)},
		"sortBy":       {"submittedDate"},
		"sortOrder":    {"descending"},
	})
}

func Get(ctx context.Context, hc *http.Client, id string) (Entry, error) {
	entries, err := query(ctx, hc, url.Values{"id_list": {NormalizeID(id)}})
	if err != nil {
		return Entry{}, err
	}
	if len(entries) == 0 || entries[0].Title == "" {
		return Entry{}, fmt.Errorf("arxiv: paper %s not found", id)
	}
	return entries[0], nil
}

// FetchLatex downloads a paper's source and returns its concatenated .tex files.
func FetchLatex(ctx context.Context, hc *http.Client, id string) (string, error) {
	data, err := get(ctx, hc, "https://arxiv.org/e-print/"+NormalizeID(id), 64<<20)
	if err != nil {
		return "", err
	}
	return LatexFromSource(data)
}

const maxLatex = 8 << 20

// LatexFromSource handles both tar.gz bundles and single gzipped .tex files.
func LatexFromSource(data []byte) (string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("arxiv: source is not gzip (probably a PDF-only submission)")
	}
	raw, err := io.ReadAll(io.LimitReader(zr, 100<<20))
	if err != nil {
		return "", fmt.Errorf("arxiv: decompress: %w", err)
	}

	tr := tar.NewReader(bytes.NewReader(raw))
	files := map[string]string{}
	tarOK := false
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		tarOK = true
		if !strings.HasSuffix(strings.ToLower(h.Name), ".tex") {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(tr, 10<<20))
		files[h.Name] = string(b)
	}

	if !tarOK {
		if strings.Contains(string(raw), `\begin{document}`) {
			return string(raw), nil
		}
		return "", errors.New("arxiv: source contains no LaTeX")
	}
	if len(files) == 0 {
		return "", errors.New("arxiv: source archive contains no .tex files")
	}

	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		if sb.Len()+len(files[n]) > maxLatex {
			break
		}
		sb.WriteString(files[n])
		sb.WriteString("\n")
	}
	return sb.String(), nil
}
