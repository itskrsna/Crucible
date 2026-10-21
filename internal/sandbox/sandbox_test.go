package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTailKeepsEnd(t *testing.T) {
	got := tail("0123456789", 4)
	if !strings.HasSuffix(got, "6789") || !strings.Contains(got, "truncated") {
		t.Errorf("got %q", got)
	}
	if tail("abc", 10) != "abc" {
		t.Error("short strings should be unchanged")
	}
}

// Needs a running Docker daemon: CRUCIBLE_DOCKER_TEST=1 go test ./internal/sandbox
func TestDockerSandbox(t *testing.T) {
	if os.Getenv("CRUCIBLE_DOCKER_TEST") == "" {
		t.Skip("set CRUCIBLE_DOCKER_TEST=1 to run")
	}
	ctx := context.Background()
	d, err := Start(ctx, Options{Image: "python:3.11", CPUs: 1, MemoryGB: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(ctx)

	res, err := d.Exec(ctx, "echo hi && pwd", 30*time.Second)
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Output, "hi") || !strings.Contains(res.Output, "/work") {
		t.Fatalf("%+v %v", res, err)
	}

	if _, err := d.Exec(ctx, "echo data > /work/f.txt", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	res, _ = d.Exec(ctx, "cat /work/f.txt", 30*time.Second)
	if strings.TrimSpace(res.Output) != "data" {
		t.Errorf("files should persist between execs, got %q", res.Output)
	}

	res, _ = d.Exec(ctx, "exit 3", 30*time.Second)
	if res.ExitCode != 3 {
		t.Errorf("exit code %d", res.ExitCode)
	}

	res, _ = d.Exec(ctx, "sleep 30", 2*time.Second)
	if !res.TimedOut {
		t.Errorf("expected timeout, got %+v", res)
	}

	res, _ = d.Exec(ctx, "ls /host /mnt 2>&1; id -u; cat /proc/self/status | grep CapEff", 30*time.Second)
	if !strings.Contains(res.Output, "0000000000000000") {
		t.Errorf("capabilities should be dropped: %s", res.Output)
	}
}
