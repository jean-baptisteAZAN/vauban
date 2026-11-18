package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Runner runs external commands.
type Runner interface {
	Run(ctx context.Context, cmd Cmd) ([]byte, error) // returns stdout
}

// Cmd is an external command. Env entries are added to the current environment.
type Cmd struct {
	Dir, Name string
	Args, Env []string
	Stdin     io.Reader
}

// ExecRunner runs commands with os/exec.
type ExecRunner struct{}

// Run executes cmd and returns its stdout; on failure the error includes stderr.
// When ctx is cancelled the command gets SIGINT, not SIGKILL, and Run waits
// for it to exit: tofu then stops cleanly and saves its state.
func (ExecRunner) Run(ctx context.Context, cmd Cmd) ([]byte, error) {
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	c.Cancel = func() error { return c.Process.Signal(os.Interrupt) }
	c.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		c.Env = append(os.Environ(), cmd.Env...)
	}
	var stdin []byte
	if cmd.Stdin != nil {
		b, err := io.ReadAll(cmd.Stdin)
		if err != nil {
			return nil, fmt.Errorf("%s: read stdin: %w", cmd.Name, err)
		}
		stdin = b
		c.Stdin = bytes.NewReader(b)
	}
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		if msg := strings.TrimSpace(redact(stderr.String(), cmd.Env, stdin)); msg != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %w: %s", cmd.Name, err, msg)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w", cmd.Name, err)
	}
	return stdout.Bytes(), nil
}

// redact replaces Env values and stdin content (4+ chars) in s with ***.
func redact(s string, env []string, stdin []byte) string {
	secrets := []string{string(stdin), strings.TrimSpace(string(stdin))}
	for _, kv := range env {
		if _, v, ok := strings.Cut(kv, "="); ok {
			secrets = append(secrets, v)
		}
	}
	for _, v := range secrets {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// FakeRunner records commands instead of running them. Responses and Errors
// are keyed by the full command line (Name and Args joined by spaces), else
// by Name+" "+first arg; unmatched commands return empty output.
type FakeRunner struct {
	Calls     []Cmd
	Responses map[string][]byte
	Errors    map[string]error
}

// Run records cmd and returns the canned response or error for its key.
func (f *FakeRunner) Run(_ context.Context, cmd Cmd) ([]byte, error) {
	f.Calls = append(f.Calls, cmd)
	full := strings.Join(append([]string{cmd.Name}, cmd.Args...), " ")
	short := cmd.Name
	if len(cmd.Args) > 0 {
		short += " " + cmd.Args[0]
	}
	for _, key := range []string{full, short} {
		if err := f.Errors[key]; err != nil {
			return nil, err
		}
	}
	if out, ok := f.Responses[full]; ok {
		return out, nil
	}
	return f.Responses[short], nil
}
