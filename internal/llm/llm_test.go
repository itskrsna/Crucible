package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestChatCachesResponses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "m", t.TempDir())
	msgs := []Message{{Role: "user", Content: "hi"}}
	for i := 0; i < 2; i++ {
		got, err := c.Chat(context.Background(), msgs, false)
		if err != nil || got != "hello" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("server hit %d times, want 1 (second call should be cached)", hits.Load())
	}
}

func TestChatRetriesServerErrors(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 2 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "m", "")
	c.retryDelay = 0
	got, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, false)
	if err != nil || got != "ok" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestExtractJSON(t *testing.T) {
	got := ExtractJSON("Sure!\n```json\n{\"a\": 1}\n```")
	if got != `{"a": 1}` {
		t.Errorf("got %q", got)
	}
}

type scripted struct {
	replies []string
	calls   int
}

func (s *scripted) Chat(_ context.Context, _ []Message, _ bool) (string, error) {
	r := s.replies[s.calls]
	s.calls++
	return r, nil
}

func TestChatJSONRetriesOnBadJSON(t *testing.T) {
	s := &scripted{replies: []string{"not json", `{"n": 7}`}}
	var out struct{ N int }
	if err := ChatJSON(context.Background(), s, nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.N != 7 || s.calls != 2 {
		t.Errorf("n=%d calls=%d", out.N, s.calls)
	}
}
