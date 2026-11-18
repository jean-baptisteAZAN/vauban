// Package engine plans and applies vauban modules as ordered, resumable steps.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
)

// Outputs are non-secret key/value results passed from one step to the next.
type Outputs map[string]string

// Status reports how much of a module is already present in a repository.
type Status int

const (
	StatusPresent Status = iota
	StatusMissing
	StatusPartial
)

// Module is one area of setup (container, ci, infra, deploy...).
type Module interface {
	ID() string
	Title() string // human label for doctor
	Detect(p *detect.Project) Status
	Requires() []string // module IDs that must run first
	Plan(p *detect.Project, c *config.Config) ([]Step, error)
}

// Step is one idempotent unit of work shown in plan output and run by apply.
type Step interface {
	ID() string       // stable, unique: "<module>:<thing>"
	Describe() string // one line for plan output
	Apply(ctx context.Context, env *Env) (Outputs, error)
}

// Env is what a step needs to apply itself.
type Env struct {
	Root  string
	Force bool
	Run   Runner
	In    Outputs // accumulated non-secret outputs of previous steps
	Log   io.Writer
	// Confirm asks the user to approve a risky step (title, then details such
	// as a plan). Steps that need it treat nil as "not confirmed".
	Confirm func(title, details string) (bool, error)
	// Progress, when set, is told as Execute skips (already done), starts,
	// finishes or fails each step. err is set only for PhaseFail.
	Progress func(id, describe string, phase Phase, err error)
}

// Phase is a step's progress through Execute.
type Phase int

const (
	PhaseStart Phase = iota // about to apply
	PhaseDone               // applied and saved
	PhaseSkip               // already done in state
	PhaseFail               // apply or save failed
)

func (p Phase) String() string {
	switch p {
	case PhaseStart:
		return "start"
	case PhaseDone:
		return "done"
	case PhaseSkip:
		return "skip"
	case PhaseFail:
		return "fail"
	}
	return "unknown"
}

func (e *Env) progress(s Step, phase Phase, err error) {
	if e.Progress != nil {
		e.Progress(s.ID(), s.Describe(), phase, err)
	}
}

// ErrNotConfirmed is returned by a step whose risky action was not approved.
// Nothing was changed; rerunning asks again.
var ErrNotConfirmed = errors.New("not confirmed")

func (e *Env) logf(format string, args ...any) {
	if e.Log != nil {
		fmt.Fprintf(e.Log, format, args...)
	}
}

// Order sorts modules so every module comes after its requirements. Among
// modules whose requirements are satisfied, input order is preserved.
func Order(mods []Module) ([]Module, error) {
	byID := make(map[string]bool, len(mods))
	for _, m := range mods {
		byID[m.ID()] = true
	}
	for _, m := range mods {
		for _, r := range m.Requires() {
			if !byID[r] {
				return nil, fmt.Errorf("module %s requires unknown module %s", m.ID(), r)
			}
		}
	}
	done := make(map[string]bool, len(mods))
	out := make([]Module, 0, len(mods))
	for len(out) < len(mods) {
		progressed := false
		for _, m := range mods {
			if done[m.ID()] || !allDone(m.Requires(), done) {
				continue
			}
			done[m.ID()] = true
			out = append(out, m)
			progressed = true
			break
		}
		if !progressed {
			var stuck []string
			for _, m := range mods {
				if !done[m.ID()] {
					stuck = append(stuck, m.ID())
				}
			}
			return nil, fmt.Errorf("module dependency cycle among: %s", strings.Join(stuck, ", "))
		}
	}
	return out, nil
}

func allDone(ids []string, done map[string]bool) bool {
	for _, id := range ids {
		if !done[id] {
			return false
		}
	}
	return true
}

// Execute applies steps in order, skipping those already done in st and
// saving st after every step so an interrupted run resumes where it stopped.
// Outputs saved in st are merged into env.In before any step runs and take
// precedence over values already in env.In. Duplicate step IDs are rejected
// before anything is applied.
func Execute(ctx context.Context, steps []Step, st *State, env *Env) error {
	seen := make(map[string]bool, len(steps))
	for _, s := range steps {
		if seen[s.ID()] {
			return fmt.Errorf("duplicate step id %q", s.ID())
		}
		seen[s.ID()] = true
	}
	st.init()
	if env.In == nil {
		env.In = Outputs{}
	}
	for k, v := range st.Outputs {
		env.In[k] = v
	}
	for _, s := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := s.ID()
		if st.Done[id] {
			env.progress(s, PhaseSkip, nil)
			continue
		}
		env.progress(s, PhaseStart, nil)
		out, err := s.Apply(ctx, env)
		if err != nil {
			env.progress(s, PhaseFail, err)
			return fmt.Errorf("%s: %w", id, err)
		}
		for k, v := range out {
			st.Outputs[k] = v
			env.In[k] = v
		}
		st.Done[id] = true
		if err := st.Save(env.Root); err != nil {
			env.progress(s, PhaseFail, err)
			return fmt.Errorf("%s: save state: %w", id, err)
		}
		env.progress(s, PhaseDone, nil)
	}
	return nil
}
