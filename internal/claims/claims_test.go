package claims

import (
	"context"
	"strings"
	"testing"

	"github.com/itskrsna/Crucible/internal/llm"
)

type fakeLLM struct{ reply string }

func (f fakeLLM) Chat(context.Context, []llm.Message, bool) (string, error) { return f.reply, nil }

func TestSelectTablesKeepsRelevantOnes(t *testing.T) {
	latex := `intro \begin{table}ETTh1 MSE 0.38\end{table} mid \begin{table}unrelated\end{table}`
	got := SelectTables(latex, []string{"ETTh1"}, 1000)
	if !strings.Contains(got, "ETTh1") || strings.Contains(got, "unrelated") {
		t.Errorf("got %q", got)
	}
}

func TestSelectTablesRanksMainResultsFirstAndStripsMarkup(t *testing.T) {
	latex := `\begin{table}\caption{Ablation study}
\toprule[2pt] \multicolumn{2}{c|}{ETTh1} & 0.111 \\ \end{table}
\begin{table}\caption{Main forecasting results}
\midrule \textbf{\underline{ETTh1}} & \multirow{2}*{MSE} & 0.379 \\ % hidden comment
\end{table}`
	got := SelectTables(latex, []string{"ETTh1"}, 5000)
	if strings.Index(got, "0.379") > strings.Index(got, "0.111") {
		t.Errorf("main results should come before the ablation:\n%s", got)
	}
	for _, noise := range []string{`\toprule`, `\midrule`, `\textbf`, `\multicolumn`, `\multirow`, "hidden comment"} {
		if strings.Contains(got, noise) {
			t.Errorf("%q should be stripped:\n%s", noise, got)
		}
	}
	if !strings.Contains(got, "ETTh1") || !strings.Contains(got, "MSE") {
		t.Errorf("content must survive compaction:\n%s", got)
	}
}

func TestExtractDropsNumbersNotInPaper(t *testing.T) {
	reply := `{"claims":[
	  {"metric":"MSE","dataset":"ETTh1","setting":"H=96","value":0.379,"source":"T2"},
	  {"metric":"MAE","dataset":"ETTh1","setting":"H=96","value":0.777,"source":"T2"}]}`
	got, err := Extract(context.Background(), fakeLLM{reply}, "t", "ETTh1 MSE 0.379 MAE 0.400")
	if err != nil || len(got) != 1 || got[0].Value != 0.379 {
		t.Fatalf("invented value 0.777 should be dropped: %+v %v", got, err)
	}
}

func TestSelectTablesFallsBackToText(t *testing.T) {
	if got := SelectTables("no tables here", []string{"ETTh1"}, 5); got != "no ta" {
		t.Errorf("got %q", got)
	}
}

func TestExtractValidatesModelOutput(t *testing.T) {
	reply := `{"claims":[
	  {"metric":"MSE","dataset":"ETTh1","setting":"H=96","value":0.379,"best_baseline":0.386,"source":"Table 2"},
	  {"metric":"MSE","dataset":"ETTh1","setting":"H=96","value":0.5,"source":"dup"},
	  {"metric":"","dataset":"ETTh1","value":0.1},
	  {"metric":"MAE","dataset":"ETTh1","setting":"H=96"},
	  {"metric":"MAE","dataset":"ETTh1","setting":"H=96","value":0.4,"source":"Table 2"}]}`
	got, err := Extract(context.Background(), fakeLLM{reply}, "t", "values 0.379 0.386 0.4 0.5 0.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d claims: %+v", len(got), got)
	}
	if got[0].ID != "c1" || got[1].ID != "c2" || got[0].BestBaseline == nil || *got[0].BestBaseline != 0.386 {
		t.Errorf("unexpected claims %+v", got)
	}
}
