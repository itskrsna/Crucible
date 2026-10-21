// Package site renders the public registry: an index, one page per record, and badges.
package site

import (
	"fmt"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"sort"

	"github.com/itskrsna/Crucible/internal/record"
	"github.com/itskrsna/Crucible/internal/schemas"
)

// ProjectURL is where disputes and corrections are filed.
const ProjectURL = "https://github.com/itskrsna/Crucible"

type Stats struct {
	Total, Reproduced, Partial, NotReproduced, CouldNotRun int
	// Among claims that reproduced and had a baseline to compare with:
	BaselineChecked, BeatBaselines int
}

type entry struct {
	Rec     schemas.Record
	Slug    string
	Overall string
	Dispute string
}

func Compute(recs []schemas.Record) Stats {
	var s Stats
	for _, r := range recs {
		s.Total++
		switch record.Overall(r) {
		case "reproduced":
			s.Reproduced++
		case "partially reproduced":
			s.Partial++
		case "not reproduced":
			s.NotReproduced++
		default:
			s.CouldNotRun++
		}
		for _, c := range r.Results {
			if c.Verdict == schemas.Reproduced && c.BeatsBaselines != nil {
				s.BaselineChecked++
				if *c.BeatsBaselines {
					s.BeatBaselines++
				}
			}
		}
	}
	return s
}

func color(overall string) string {
	switch overall {
	case "reproduced":
		return "#2e9d4e"
	case "partially reproduced":
		return "#c98a12"
	case "not reproduced":
		return "#c0392b"
	default:
		return "#7a7a7a"
	}
}

// Badge returns an SVG status badge.
func Badge(overall string) string {
	left, right := "crucible", html.EscapeString(overall)
	lw, rw := 8+len(left)*7, 10+len(overall)*7
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%[1]d" height="20" role="img" aria-label="%[3]s: %[4]s">`+
		`<rect width="%[5]d" height="20" fill="#555"/><rect x="%[5]d" width="%[6]d" height="20" fill="%[7]s"/>`+
		`<g fill="#fff" font-family="Verdana,sans-serif" font-size="11" text-anchor="middle">`+
		`<text x="%[8]d" y="14">%[3]s</text><text x="%[9]d" y="14">%[4]s</text></g></svg>`,
		lw+rw, 0, left, right, lw, rw, color(overall), lw/2, lw+rw/2)
}

const css = `:root{--bg:#fff;--fg:#1b1b1b;--muted:#666;--line:#ddd;--card:#f7f7f7}
@media (prefers-color-scheme:dark){:root{--bg:#141414;--fg:#e8e8e8;--muted:#9a9a9a;--line:#333;--card:#1e1e1e}}
body{margin:0 auto;max-width:860px;padding:24px 16px;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif}
a{color:inherit}h1{margin:0 0 4px}.muted{color:var(--muted)}
table{border-collapse:collapse;width:100%;margin:16px 0}th,td{text-align:left;padding:8px;border-bottom:1px solid var(--line);vertical-align:top}
.stats{display:flex;flex-wrap:wrap;gap:12px;margin:16px 0}.stat{background:var(--card);padding:12px 16px;border-radius:8px}
.stat b{display:block;font-size:24px}.note{background:var(--card);padding:12px 16px;border-radius:8px;margin:16px 0}
code{font-size:13px;word-break:break-all}pre{overflow-x:auto;background:var(--card);padding:12px;border-radius:8px}`

const footer = `<p class="note muted">crucible is an automated tool. A result of "not reproduced" or "could not run" can be caused by the
agent, the sandbox or missing resources, not only by a problem in the paper, and says nothing about the authors' conduct.
Every negative result is reviewed by a human before it is listed. Think a record is wrong?
<a href="%s/issues/new?title=%s">Open a dispute</a>.</p>`

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>crucible registry</title><style>` + css + `</style></head><body>
<h1>crucible registry</h1><p class="muted">Open, rerunnable attempts to reproduce published results.</p>
<div class="stats">
<div class="stat"><b>{{.Stats.Total}}</b>papers checked</div>
<div class="stat"><b>{{.Stats.Reproduced}}</b>reproduced</div>
<div class="stat"><b>{{.Stats.Partial}}</b>partially</div>
<div class="stat"><b>{{.Stats.NotReproduced}}</b>not reproduced</div>
<div class="stat"><b>{{.Stats.CouldNotRun}}</b>could not run</div>
</div>
{{if .Stats.BaselineChecked}}<p>Of {{.Stats.BaselineChecked}} reproduced claims that had a baseline to compare against,
<b>{{.Stats.BeatBaselines}}</b> still beat the best baseline the paper itself reported.</p>{{end}}
<table><tr><th>Paper</th><th>Result</th></tr>
{{range .Entries}}<tr><td><a href="{{.Slug}}.html">{{.Rec.Paper.Title}}</a><br><span class="muted">arXiv {{.Rec.Paper.ArxivID}}</span></td>
<td><img src="badges/{{.Slug}}.svg" alt="{{.Overall}}"></td></tr>{{end}}
</table>{{.Footer}}</body></html>`))

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Rec.Paper.Title}} — crucible</title><style>` + css + `</style></head><body>
<p><a href="index.html">← registry</a></p><h1>{{.Rec.Paper.Title}}</h1>
<p class="muted">arXiv {{.Rec.Paper.ArxivID}} · <a href="{{.Rec.Paper.RepoURL}}">code</a>{{if .Rec.Paper.RepoCommit}} @ <code>{{.Rec.Paper.RepoCommit}}</code>{{end}} · model <code>{{.Rec.Model}}</code></p>
<p><img src="badges/{{.Slug}}.svg" alt="{{.Overall}}"></p>
<table><tr><th>Claim</th><th>Paper</th><th>Measured</th><th>Verdict</th></tr>
{{range .Rec.Results}}<tr><td>{{.Claim.Metric}} · {{.Claim.Dataset}} {{.Claim.Setting}}<br><span class="muted">{{.Claim.Source}}</span></td>
<td>{{.Claim.Value}}</td><td>{{if .Measured}}{{.Measured}}{{else}}—{{end}}</td>
<td>{{.Verdict}}{{if .Reason}} ({{.Reason}}){{end}}{{if .Note}}<br><span class="muted">{{.Note}}</span>{{end}}
{{if .BeatsBaselines}}<br><span class="muted">still beats the paper's reported baselines</span>{{end}}</td></tr>{{end}}
</table>
{{if .Rec.HumanReviewed}}<p class="muted">Reviewed by a human.</p>{{end}}
<h2>Rerun it</h2><p>The record includes a Dockerfile and the exact commands. No model is needed to rerun:</p>
<pre>crucible rerun records/{{.Slug}}</pre>
{{.Footer}}</body></html>`))

// Generate writes the registry for all publishable records in recordsDir into outDir.
func Generate(recordsDir, outDir string) (Stats, error) {
	all, err := record.LoadAll(recordsDir)
	if err != nil {
		return Stats{}, err
	}
	var recs []schemas.Record
	for _, r := range all {
		if record.Publishable(r) {
			recs = append(recs, r)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Paper.ArxivID > recs[j].Paper.ArxivID })

	if err := os.MkdirAll(filepath.Join(outDir, "badges"), 0o755); err != nil {
		return Stats{}, err
	}
	var entries []entry
	for _, r := range recs {
		e := entry{Rec: r, Slug: record.Slug(r.Paper.ArxivID), Overall: record.Overall(r)}
		entries = append(entries, e)
		if err := render(filepath.Join(outDir, e.Slug+".html"), pageTmpl, map[string]any{
			"Rec": r, "Slug": e.Slug, "Overall": e.Overall, "Footer": footerHTML(e.Slug),
		}); err != nil {
			return Stats{}, err
		}
		if err := os.WriteFile(filepath.Join(outDir, "badges", e.Slug+".svg"), []byte(Badge(e.Overall)), 0o644); err != nil {
			return Stats{}, err
		}
	}
	stats := Compute(recs)
	err = render(filepath.Join(outDir, "index.html"), indexTmpl, map[string]any{
		"Stats": stats, "Entries": entries, "Footer": footerHTML("registry"),
	})
	return stats, err
}

func footerHTML(subject string) template.HTML {
	return template.HTML(fmt.Sprintf(footer, ProjectURL, template.URLQueryEscaper("Dispute: "+subject)))
}

func render(path string, t *template.Template, data any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return t.Execute(f, data)
}
