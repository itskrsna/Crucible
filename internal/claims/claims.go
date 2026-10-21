// Package claims extracts a paper's headline quantitative claims with an LLM.
package claims

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/itskrsna/crucible/internal/llm"
	"github.com/itskrsna/crucible/internal/schemas"
)

const (
	maxPromptChars = 12000
	maxClaims      = 5
)

var tableEnv = regexp.MustCompile(`(?s)\\begin\{table\*?\}.*?\\end\{table\*?\}`)

var (
	texComment  = regexp.MustCompile(`(?m)(^|[^\\])%.*$`) // an unescaped % starts a comment
	texRule     = regexp.MustCompile(`\\(?:cmidrule|cline)(?:\([^)]*\))?\{[^}]*\}|\\(?:toprule|midrule|bottomrule|hline)(?:\[[^\]]*\])?|\\(?:vspace|hspace)\*?\{[^}]*\}`)
	texMulti    = regexp.MustCompile(`\\multi(?:column|row)\{[^}]*\}(?:\{[^}]*\}|\*)?\{([^{}]*)\}`)
	texWrap     = regexp.MustCompile(`\\(?:textbf|textit|underline|emph|textcolor\{[^}]*\}|colorbox\{[^}]*\}|cellcolor\{[^}]*\}|rotatebox\{[^}]*\}|scalebox\{[^}]*\}|text)\{([^{}]*)\}`)
	texSpaces   = regexp.MustCompile(`[ \t]+`)
	texBlank    = regexp.MustCompile(`\n\s*\n+`)
	texCaption  = regexp.MustCompile(`(?s)\\caption\{(.*?)\}\s*(?:\\|$)`)
	badCaptions = []string{"ablation", "hyper", "sensitivity", "efficiency", "complexity", "memory", "parameter"}
	goodCaption = []string{"main", "result", "comparison", "forecasting", "long-term", "benchmark"}
)

// compactTable strips LaTeX formatting that carries no numbers, so small
// models see rows of values instead of markup.
func compactTable(t string) string {
	t = texComment.ReplaceAllString(t, "$1")
	t = texRule.ReplaceAllString(t, "")
	for i := 0; i < 3; i++ { // nested wrappers, e.g. \textbf{\underline{x}}
		t = texMulti.ReplaceAllString(t, "$1")
		t = texWrap.ReplaceAllString(t, "$1")
	}
	t = texSpaces.ReplaceAllString(t, " ")
	return strings.TrimSpace(texBlank.ReplaceAllString(t, "\n"))
}

// tableScore ranks main-results tables above ablations and cost tables.
func tableScore(t string) int {
	m := texCaption.FindStringSubmatch(t)
	if m == nil {
		return 0
	}
	cap, score := strings.ToLower(m[1]), 0
	for _, w := range goodCaption {
		if strings.Contains(cap, w) {
			score++
		}
	}
	for _, w := range badCaptions {
		if strings.Contains(cap, w) {
			score -= 3
		}
	}
	return score
}

// SelectTables returns the LaTeX table environments that mention any keyword,
// compacted and ordered with main-results tables first, capped at max
// characters. If none match, it falls back to the start of the text.
func SelectTables(latex string, keywords []string, max int) string {
	var picked []string
	for _, t := range tableEnv.FindAllString(latex, -1) {
		low := strings.ToLower(t)
		for _, k := range keywords {
			if strings.Contains(low, strings.ToLower(k)) {
				picked = append(picked, t)
				break
			}
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return tableScore(picked[i]) > tableScore(picked[j]) })

	var sb strings.Builder
	for _, t := range picked {
		t = compactTable(t)
		if sb.Len()+len(t) > max {
			break
		}
		sb.WriteString(t)
		sb.WriteString("\n\n")
	}
	if sb.Len() == 0 && len(picked) > 0 {
		// A single table larger than the budget: keep its start rather than nothing.
		t := compactTable(picked[0])
		if len(t) > max {
			t = t[:max]
		}
		return t
	}
	if sb.Len() == 0 {
		if len(latex) > max {
			return latex[:max]
		}
		return latex
	}
	return sb.String()
}

const systemPrompt = `You extract quantitative results from machine learning papers.
Reply with one JSON object: {"claims":[{"metric":"MSE","dataset":"ETTh1","setting":"horizon 96","value":0.379,"best_baseline":0.386,"source":"Table 2"}]}
Rules:
- Only results for the paper's own PROPOSED method, copied exactly from the tables. Never compute or guess numbers.
- At most 5 claims. Prefer MSE and MAE on ETTh1 with prediction horizon 96; otherwise the paper's main benchmark.
- best_baseline is the best (strongest) number among the OTHER methods for the same dataset, metric and setting, if the table shows it; otherwise null.
- If the paper reports no such numbers, return {"claims":[]}.`

var DefaultKeywords = []string{"ETTh1", "ETTh2", "ETTm1", "ETTm2", "Weather", "Electricity", "MSE"}

type rawClaim struct {
	Metric       string   `json:"metric"`
	Dataset      string   `json:"dataset"`
	Setting      string   `json:"setting"`
	Value        *float64 `json:"value"`
	BestBaseline *float64 `json:"best_baseline"`
	Source       string   `json:"source"`
}

// Extract asks the model for headline claims and validates what comes back.
func Extract(ctx context.Context, ch llm.Chatter, title, latex string) ([]schemas.Claim, error) {
	excerpt := SelectTables(latex, DefaultKeywords, maxPromptChars)
	msgs := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: fmt.Sprintf("Paper: %s\n\nLaTeX excerpt:\n%s", title, excerpt)},
	}
	var resp struct {
		Claims []rawClaim `json:"claims"`
	}
	if err := llm.ChatJSON(ctx, ch, msgs, &resp); err != nil {
		return nil, fmt.Errorf("claims: %w", err)
	}
	return validate(resp.Claims, numbersIn(excerpt)), nil
}

var numberRe = regexp.MustCompile(`\d*\.\d+|\d+`)

// numbersIn collects every number that appears in text.
func numbersIn(text string) map[float64]bool {
	nums := map[float64]bool{}
	for _, s := range numberRe.FindAllString(text, -1) {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			nums[v] = true
		}
	}
	return nums
}

// validate drops malformed claims and any whose value does not literally appear
// in the paper text we showed the model (a guard against invented numbers; it
// cannot catch a real number taken from the wrong column).
func validate(raw []rawClaim, inText map[float64]bool) []schemas.Claim {
	seen := map[string]bool{}
	var out []schemas.Claim
	for _, r := range raw {
		metric, dataset := strings.TrimSpace(r.Metric), strings.TrimSpace(r.Dataset)
		if metric == "" || dataset == "" || r.Value == nil || math.IsNaN(*r.Value) || math.IsInf(*r.Value, 0) {
			continue
		}
		if inText != nil && !inText[*r.Value] {
			continue
		}
		key := strings.ToLower(metric + "|" + dataset + "|" + r.Setting)
		if seen[key] {
			continue
		}
		seen[key] = true
		c := schemas.Claim{
			ID:      fmt.Sprintf("c%d", len(out)+1),
			Metric:  metric,
			Dataset: dataset,
			Setting: strings.TrimSpace(r.Setting),
			Value:   *r.Value,
			Source:  strings.TrimSpace(r.Source),
		}
		if r.BestBaseline != nil && !math.IsNaN(*r.BestBaseline) && !math.IsInf(*r.BestBaseline, 0) {
			c.BestBaseline = r.BestBaseline
		}
		out = append(out, c)
		if len(out) == maxClaims {
			break
		}
	}
	return out
}
