package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	stateDir  = ".vauban"
	stateFile = "state.json"
	// stateIgnore keeps everything in .vauban/ out of git.
	stateIgnore = "*\n"
)

// State records which steps are done and the non-secret outputs they produced.
type State struct {
	Done    map[string]bool `json:"done"`
	Outputs Outputs         `json:"outputs"`
}

func (s *State) init() {
	if s.Done == nil {
		s.Done = map[string]bool{}
	}
	if s.Outputs == nil {
		s.Outputs = Outputs{}
	}
}

// LoadState reads .vauban/state.json under root, or returns an empty State if absent.
func LoadState(root string) (*State, error) {
	st := &State{}
	b, err := os.ReadFile(filepath.Join(root, stateDir, stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		st.init()
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	st.init()
	return st, nil
}

// Save atomically writes .vauban/state.json (dir 0700, file 0600) and
// keeps .vauban/.gitignore ignoring the whole directory.
func (s *State) Save(root string) error {
	dir := filepath.Join(root, stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod state dir: %w", err)
	}
	gi := filepath.Join(dir, ".gitignore")
	if b, err := os.ReadFile(gi); err != nil || string(b) != stateIgnore {
		if err := os.WriteFile(gi, []byte(stateIgnore), 0o644); err != nil {
			return fmt.Errorf("write state .gitignore: %w", err)
		}
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	tmp, err := os.CreateTemp(dir, stateFile+".*.tmp") // CreateTemp uses mode 0600
	if err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best-effort cleanup of the temp file (gone after rename)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close() // the write error is the one to report
		return fmt.Errorf("write state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, stateFile)); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}
