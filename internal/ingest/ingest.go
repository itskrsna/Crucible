// Package ingest gathers a paper's text and code location.
package ingest

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"

	"github.com/itskrsna/crucible/internal/arxiv"
	"github.com/itskrsna/crucible/internal/schemas"
)

var githubRepo = regexp.MustCompile(`https?://(?:www\.)?github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)`)

var notRepoOwners = map[string]bool{"features": true, "topics": true, "orgs": true, "sponsors": true, "settings": true}

// FindRepoURL returns the first GitHub repository URL found in the texts, in order.
func FindRepoURL(texts ...string) string {
	for _, t := range texts {
		for _, m := range githubRepo.FindAllStringSubmatch(t, -1) {
			owner, repo := m[1], strings.TrimSuffix(strings.TrimRight(m[2], ".-_"), ".git")
			if notRepoOwners[strings.ToLower(owner)] || repo == "" {
				continue
			}
			return fmt.Sprintf("https://github.com/%s/%s", owner, repo)
		}
	}
	return ""
}

// ResolveCommit returns the commit SHA at the repository's default branch head.
func ResolveCommit(ctx context.Context, repoURL string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", repoURL, "HEAD")
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ingest: git ls-remote %s: %w", repoURL, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("ingest: empty ls-remote output for %s", repoURL)
	}
	return fields[0], nil
}

// Load fetches metadata and LaTeX for a paper and pins its repository commit.
// A missing LaTeX source or unresolvable commit degrades gracefully; a missing
// repository is an error because nothing can be run.
func Load(ctx context.Context, hc *http.Client, id string) (schemas.Paper, string, error) {
	entry, err := arxiv.Get(ctx, hc, id)
	if err != nil {
		return schemas.Paper{}, "", err
	}
	latex, latexErr := arxiv.FetchLatex(ctx, hc, id)

	paper := schemas.Paper{ArxivID: entry.ID, Title: entry.Title}
	paper.RepoURL = FindRepoURL(entry.Summary, entry.Comment, latex)
	if paper.RepoURL == "" {
		return paper, latex, fmt.Errorf("ingest: no GitHub repository found for %s", id)
	}
	if latexErr != nil {
		return paper, "", fmt.Errorf("ingest: %w", latexErr)
	}
	if sha, err := ResolveCommit(ctx, paper.RepoURL); err == nil {
		paper.RepoCommit = sha
	}
	return paper, latex, nil
}
