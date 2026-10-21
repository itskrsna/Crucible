package arxiv

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

const sampleFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:arxiv="http://arxiv.org/schemas/atom">
  <entry>
    <id>http://arxiv.org/abs/2501.12345v2</id>
    <published>2025-01-20T10:00:00Z</published>
    <title>A  Better
      Forecaster</title>
    <summary>We forecast. Code: https://github.com/a/b</summary>
    <arxiv:comment>12 pages</arxiv:comment>
  </entry>
</feed>`

func TestParseFeed(t *testing.T) {
	es, err := ParseFeed(strings.NewReader(sampleFeed))
	if err != nil || len(es) != 1 {
		t.Fatalf("%v %v", es, err)
	}
	e := es[0]
	if e.ID != "2501.12345" || e.Title != "A Better Forecaster" || e.Comment != "12 pages" {
		t.Errorf("unexpected entry %+v", e)
	}
	if e.Published.Year() != 2025 {
		t.Errorf("bad date %v", e.Published)
	}
}

func targz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestLatexFromTarball(t *testing.T) {
	data := targz(t, map[string]string{"main.tex": `\begin{document}A`, "fig.png": "x", "t.tex": "B"})
	got, err := LatexFromSource(data)
	if err != nil || !strings.Contains(got, "A") || !strings.Contains(got, "B") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestLatexFromSingleGzippedFile(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(`\documentclass{article}\begin{document}hi\end{document}`))
	zw.Close()
	got, err := LatexFromSource(buf.Bytes())
	if err != nil || !strings.Contains(got, "hi") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestLatexFromPDFOnlyFails(t *testing.T) {
	if _, err := LatexFromSource([]byte("%PDF-1.5")); err == nil {
		t.Error("expected error")
	}
}

func TestNormalizeID(t *testing.T) {
	for in, want := range map[string]string{
		"http://arxiv.org/abs/2501.12345v3": "2501.12345",
		"arXiv:2501.12345":                  "2501.12345",
		"2501.12345v1":                      "2501.12345",
	} {
		if got := NormalizeID(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
