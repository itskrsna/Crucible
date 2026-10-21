// Package sandbox runs untrusted paper code inside a locked-down Docker container.
//
// The container has no host mounts, dropped privileges, and CPU, memory and
// process limits. It does have outbound network access (needed for git clone
// and package installs); egress filtering is not implemented yet.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Result struct {
	Output   string
	ExitCode int
	TimedOut bool
}

// Executor is what the agent needs from a sandbox; tests provide fakes.
type Executor interface {
	Exec(ctx context.Context, command string, timeout time.Duration) (Result, error)
	Close(ctx context.Context) error
}

type Options struct {
	Image    string
	CPUs     float64
	MemoryGB int
}

type Docker struct {
	name string
}

func run(ctx context.Context, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// Start launches an idle container from opts.Image.
func Start(ctx context.Context, o Options) (*Docker, error) {
	if o.CPUs <= 0 {
		o.CPUs = 4
	}
	if o.MemoryGB <= 0 {
		o.MemoryGB = 8
	}
	var id [4]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	name := "crucible-" + hex.EncodeToString(id[:])
	args := []string{"run", "-d", "--name", name}
	for _, e := range pipEnv {
		args = append(args, "-e", e)
	}
	args = append(args,
		"--cpus", fmt.Sprint(o.CPUs),
		"--memory", fmt.Sprintf("%dg", o.MemoryGB),
		"--pids-limit", "1024",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--workdir", "/work",
		o.Image, "sleep", "infinity")
	out, err := run(ctx, "", args...)
	if err != nil {
		return nil, fmt.Errorf("sandbox: start container: %w: %s", err, strings.TrimSpace(out))
	}
	return &Docker{name: name}, nil
}

const maxOutput = 256 << 10

// tail keeps the last max bytes, since the end of a log is usually what matters.
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "[...truncated...]\n" + s[len(s)-max:]
}

// Exec runs command in a fresh bash. Each call starts from /work; only
// filesystem changes persist between calls. The in-container timeout is
// authoritative so runaway processes die with it.
func (d *Docker) Exec(ctx context.Context, command string, timeout time.Duration) (Result, error) {
	secs := int(timeout.Seconds())
	if secs < 1 {
		secs = 1
	}
	out, err := run(ctx, "", "exec", d.name, "timeout", "-k", "5", fmt.Sprint(secs), "bash", "-lc", command)
	res := Result{Output: tail(out, maxOutput)}
	if err == nil {
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		res.TimedOut = res.ExitCode == 124 || res.ExitCode == 137
		return res, nil
	}
	return res, fmt.Errorf("sandbox: docker exec: %w", err)
}

func (d *Docker) Close(ctx context.Context) error {
	out, err := run(ctx, "", "rm", "-f", d.name)
	if err != nil {
		return fmt.Errorf("sandbox: remove container: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// BuildImage builds a Docker image from dockerfile text with an empty context.
func BuildImage(ctx context.Context, dockerfile, tag string) error {
	out, err := run(ctx, dockerfile, "build", "-t", tag, "-")
	if err != nil {
		return fmt.Errorf("sandbox: docker build: %w: %s", err, tail(out, 2000))
	}
	return nil
}
