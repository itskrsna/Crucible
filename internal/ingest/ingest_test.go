package ingest

import "testing"

func TestFindRepoURL(t *testing.T) {
	tests := []struct {
		name  string
		texts []string
		want  string
	}{
		{"plain", []string{"code at https://github.com/foo/bar."}, "https://github.com/foo/bar"},
		{"latex url", []string{`\url{https://github.com/foo/bar}`}, "https://github.com/foo/bar"},
		{"git suffix", []string{"https://github.com/foo/bar.git"}, "https://github.com/foo/bar"},
		{"skips non-repo pages", []string{"https://github.com/features/actions https://github.com/a/b"}, "https://github.com/a/b"},
		{"first text wins", []string{"https://github.com/x/y", "https://github.com/a/b"}, "https://github.com/x/y"},
		{"none", []string{"no link here"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FindRepoURL(tt.texts...); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
