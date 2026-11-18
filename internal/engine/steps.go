package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WriteFile writes Content to Path (relative to Env.Root). It leaves an
// existing file untouched unless Env.Force is set.
type WriteFile struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

func (w WriteFile) ID() string       { return "file:" + w.Path }
func (w WriteFile) Describe() string { return "write " + w.Path }

func (w WriteFile) Apply(_ context.Context, env *Env) (Outputs, error) {
	path := filepath.Join(env.Root, w.Path)
	if _, err := os.Stat(path); err == nil {
		if !env.Force {
			env.logf("skip (exists) %s\n", w.Path)
			return nil, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	mode := w.Mode
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, w.Content, mode); err != nil {
		return nil, err
	}
	// WriteFile keeps an existing file's mode and is subject to umask.
	if err := os.Chmod(path, mode); err != nil {
		return nil, err
	}
	return nil, nil
}

// RunCommand runs Cmd and turns its stdout into outputs with Parse (optional).
type RunCommand struct {
	StepID, Label string
	Cmd           Cmd
	Parse         func([]byte) (Outputs, error)
}

func (r RunCommand) ID() string { return r.StepID }

// Describe never includes Cmd.Env or Stdin, which may hold credentials.
func (r RunCommand) Describe() string {
	line := "$ " + strings.Join(append([]string{r.Cmd.Name}, r.Cmd.Args...), " ")
	if r.Label != "" {
		return r.Label + ": " + line
	}
	return line
}

func (r RunCommand) Apply(ctx context.Context, env *Env) (Outputs, error) {
	cmd := r.Cmd
	if cmd.Dir != "" && !filepath.IsAbs(cmd.Dir) {
		cmd.Dir = filepath.Join(env.Root, cmd.Dir)
	} else if cmd.Dir == "" {
		cmd.Dir = env.Root
	}
	out, err := env.Run.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if r.Parse == nil {
		return nil, nil
	}
	return r.Parse(out)
}

// SetSecret stores a GitHub Actions secret via `gh secret set`, passing the
// value on stdin. The value comes from Env.In[FromOutput] or, otherwise, the
// contents of FromFile; FromFile "$key" means the path is Env.In[key].
type SetSecret struct {
	Repo, Name, FromOutput, FromFile string
}

func (s SetSecret) ID() string       { return "secret:" + s.Name }
func (s SetSecret) Describe() string { return "gh secret set " + s.Name + " --repo " + s.Repo }

func (s SetSecret) Apply(ctx context.Context, env *Env) (Outputs, error) {
	value, err := s.value(env)
	if err != nil {
		return nil, err
	}
	_, err = env.Run.Run(ctx, Cmd{
		Dir:   env.Root,
		Name:  "gh",
		Args:  []string{"secret", "set", s.Name, "--repo", s.Repo},
		Stdin: bytes.NewReader(value),
	})
	return nil, err
}

func (s SetSecret) value(env *Env) ([]byte, error) {
	if s.FromOutput != "" {
		v, ok := env.In[s.FromOutput]
		if !ok || v == "" {
			return nil, fmt.Errorf("secret %s: no value for output %q", s.Name, s.FromOutput)
		}
		return []byte(v), nil
	}
	path := s.FromFile
	if key, ok := strings.CutPrefix(path, "$"); ok {
		path = env.In[key]
		if path == "" {
			return nil, fmt.Errorf("secret %s: no path for output %q", s.Name, key)
		}
	}
	if path == "" {
		return nil, fmt.Errorf("secret %s: no source configured", s.Name)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secret %s: read %s: %w", s.Name, path, err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("secret %s: %s is empty", s.Name, path)
	}
	return b, nil
}

// Kinds a custom step may report through an optional Kind() string method so
// plan output renders it like a built-in step. Its Describe must then read
// "write <path>" (KindWrite) or "[<label>: ]$ <command line>" (KindCommand).
const (
	KindWrite   = "write"
	KindCommand = "command"
)
