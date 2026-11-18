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
func (ExecRunner) Run(ctx context.Context, cmd Cmd) ([]byte, error) {
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	c.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		c.Env = append(os.Environ(), cmd.Env...)
	}
	c.Stdin = cmd.Stdin
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %w: %s", cmd.Name, err, msg)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w", cmd.Name, err)
	}
	return stdout.Bytes(), nil
}

// FakeRunner records commands instead of running them. Responses and Errors
// are keyed by Name+" "+first arg; unmatched commands return empty output.
type FakeRunner struct {
	Calls     []Cmd
	Responses map[string][]byte
	Errors    map[string]error
}

// Run records cmd and returns the canned response or error for its key.
func (f *FakeRunner) Run(_ context.Context, cmd Cmd) ([]byte, error) {
	f.Calls = append(f.Calls, cmd)
	key := cmd.Name
	if len(cmd.Args) > 0 {
		key += " " + cmd.Args[0]
	}
	if err := f.Errors[key]; err != nil {
		return nil, err
	}
	return f.Responses[key], nil
}
