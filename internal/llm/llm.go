// Package llm is a small client for any OpenAI-compatible chat API
// (Ollama, OpenRouter, Groq, vLLM, ...) with an on-disk response cache.
package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Chatter is what the rest of the code depends on, so tests can fake it.
type Chatter interface {
	Chat(ctx context.Context, msgs []Message, jsonMode bool) (string, error)
}

type Client struct {
	BaseURL  string
	APIKey   string
	Model    string
	CacheDir string // empty disables caching
	HTTP     *http.Client

	retryDelay time.Duration
}

func New(baseURL, apiKey, model, cacheDir string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIKey:     apiKey,
		Model:      model,
		CacheDir:   cacheDir,
		HTTP:       &http.Client{Timeout: 15 * time.Minute},
		retryDelay: 2 * time.Second,
	}
}

func (c *Client) cachePath(msgs []Message, jsonMode bool) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%t|", c.Model, jsonMode)
	for _, m := range msgs {
		fmt.Fprintf(h, "%s:%d:%s|", m.Role, len(m.Content), m.Content)
	}
	return filepath.Join(c.CacheDir, hex.EncodeToString(h.Sum(nil))+".txt")
}

func (c *Client) Chat(ctx context.Context, msgs []Message, jsonMode bool) (string, error) {
	if c.CacheDir != "" {
		if b, err := os.ReadFile(c.cachePath(msgs, jsonMode)); err == nil {
			return string(b), nil
		}
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Small models sometimes fall into repetition loops that the server
		// aborts with a 500. Retrying the identical deterministic request would
		// loop again, so nudge the temperature up on each retry.
		body := map[string]any{
			"model":       c.Model,
			"messages":    msgs,
			"temperature": 0.3 * float64(min(attempt-1, 2)),
			"max_tokens":  maxTokens,
			"stream":      false,
		}
		if jsonMode {
			body["response_format"] = map[string]string{"type": "json_object"}
		}
		payload, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		reply, retry, wait, err := c.once(ctx, payload)
		if err == nil {
			c.store(msgs, jsonMode, reply)
			return reply, nil
		}
		lastErr = err
		if !retry {
			break
		}
		if wait == 0 {
			wait = c.retryDelay * time.Duration(attempt)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
	return "", lastErr
}

const (
	maxAttempts = 6
	// Reasoning models spend output tokens on thinking before the answer, so
	// the cap must leave room for both.
	maxTokens = 8192
	// Free tiers are rate-limited per minute; wait this long if the server
	// doesn't say when to retry.
	defaultRateLimitWait = 20 * time.Second
	maxRateLimitWait     = 90 * time.Second
)

// once makes a single request. retry says whether trying again may help, and
// wait is how long the server asked us to hold off (zero if it didn't).
func (c *Client) once(ctx context.Context, payload []byte) (reply string, retry bool, wait time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", false, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", true, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", true, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests {
			wait = rateLimitWait(resp.Header.Get("Retry-After"), c.retryDelay)
		}
		retry = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return "", retry, wait, fmt.Errorf("llm: %s: %s", resp.Status, snippet(data))
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return "", false, 0, fmt.Errorf("llm: unexpected response: %s", snippet(data))
	}
	content := out.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		// Usually the model ran out of tokens while thinking, or the provider hiccuped.
		return "", true, 0, fmt.Errorf("llm: empty reply")
	}
	return content, false, 0, nil
}

// rateLimitWait honours a Retry-After header in seconds, within sane bounds.
// With no usable header it waits defaultRateLimitWait, unless retries are
// disabled for tests (base == 0).
func rateLimitWait(header string, base time.Duration) time.Duration {
	if base == 0 {
		return 0
	}
	if s, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, maxRateLimitWait)
	}
	return defaultRateLimitWait
}

func (c *Client) store(msgs []Message, jsonMode bool, reply string) {
	if c.CacheDir == "" {
		return
	}
	if err := os.MkdirAll(c.CacheDir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(c.cachePath(msgs, jsonMode), []byte(reply), 0o644)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// ExtractJSON returns the outermost {...} in s, tolerating code fences and chatter.
func ExtractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return s
	}
	return s[start : end+1]
}

// ChatJSON asks for a JSON object and decodes it into out, feeding parse errors
// back to the model for up to three attempts (small models often emit bad JSON).
func ChatJSON(ctx context.Context, ch Chatter, msgs []Message, out any) error {
	msgs = append([]Message{}, msgs...)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		reply, err := ch.Chat(ctx, msgs, true)
		if err != nil {
			return err
		}
		if lastErr = json.Unmarshal([]byte(ExtractJSON(reply)), out); lastErr == nil {
			return nil
		}
		msgs = append(msgs,
			Message{Role: "assistant", Content: reply},
			Message{Role: "user", Content: "That was not valid JSON (" + lastErr.Error() + "). Reply with only the JSON object."},
		)
	}
	return fmt.Errorf("llm: no valid JSON after 3 attempts: %w", lastErr)
}
