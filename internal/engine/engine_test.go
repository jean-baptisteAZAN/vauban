package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
)

type fakeModule struct {
	id   string
	reqs []string
}

func (m fakeModule) ID() string                                           { return m.id }
func (m fakeModule) Title() string                                        { return m.id }
func (m fakeModule) Detect(*detect.Project) Status                        { return StatusMissing }
func (m fakeModule) Requires() []string                                   { return m.reqs }
func (m fakeModule) Plan(*detect.Project, *config.Config) ([]Step, error) { return nil, nil }

func ids(mods []Module) []string {
	out := make([]string, len(mods))
	for i, m := range mods {
		out[i] = m.ID()
	}
	return out
}

func TestOrder(t *testing.T) {
	a := fakeModule{id: "a"}
	b := fakeModule{id: "b", reqs: []string{"a"}}
	c := fakeModule{id: "c", reqs: []string{"b"}}
	got, err := Order([]Module{c, a, b})
	if err != nil {
		t.Fatalf("Order: %v", err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("Order = %v, want %v", ids(got), want)
	}

	x := fakeModule{id: "x", reqs: []string{"y"}}
	y := fakeModule{id: "y", reqs: []string{"x"}}
	if _, err := Order([]Module{x, y}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle: err = %v, want error containing \"cycle\"", err)
	}
}

func TestOrderStableAmongPeers(t *testing.T) {
	p := fakeModule{id: "p"}
	q := fakeModule{id: "q"}
	r := fakeModule{id: "r", reqs: []string{"q"}}
	got, err := Order([]Module{r, q, p})
	if err != nil {
		t.Fatalf("Order: %v", err)
	}
	if want := []string{"q", "r", "p"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("Order = %v, want %v", ids(got), want)
	}
}

func TestOrderUnknownRequirement(t *testing.T) {
	_, err := Order([]Module{fakeModule{id: "deploy", reqs: []string{"infra"}}})
	if err == nil || !strings.Contains(err.Error(), "deploy") || !strings.Contains(err.Error(), "infra") {
		t.Fatalf("err = %v, want error naming deploy and infra", err)
	}
}

type countStep struct {
	id    string
	calls *int
	err   error
	out   Outputs
}

func (s countStep) ID() string       { return s.id }
func (s countStep) Describe() string { return s.id }
func (s countStep) Apply(context.Context, *Env) (Outputs, error) {
	*s.calls++
	return s.out, s.err
}

func TestExecuteResume(t *testing.T) {
	root := t.TempDir()
	var c1, c2, c3 int
	steps := []Step{
		countStep{id: "m:one", calls: &c1, out: Outputs{"server_ip": "1.2.3.4"}},
		countStep{id: "m:two", calls: &c2, err: errors.New("boom")},
		countStep{id: "m:three", calls: &c3},
	}
	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	env := &Env{Root: root, Run: &FakeRunner{}, In: Outputs{}}
	err = Execute(context.Background(), steps, st, env)
	if err == nil || !strings.Contains(err.Error(), "m:two: boom") {
		t.Fatalf("Execute err = %v, want m:two: boom", err)
	}
	if want := map[string]bool{"m:one": true}; !reflect.DeepEqual(st.Done, want) {
		t.Fatalf("Done = %v, want %v", st.Done, want)
	}
	if _, err := os.Stat(filepath.Join(root, ".vauban", "state.json")); err != nil {
		t.Fatalf("state file: %v", err)
	}
	if c3 != 0 {
		t.Fatalf("step three ran after failure")
	}

	st, err = LoadState(root)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.Outputs["server_ip"] != "1.2.3.4" {
		t.Fatalf("persisted outputs = %v", st.Outputs)
	}
	steps[1] = countStep{id: "m:two", calls: &c2}
	env = &Env{Root: root, Run: &FakeRunner{}, In: Outputs{}}
	if err := Execute(context.Background(), steps, st, env); err != nil {
		t.Fatalf("Execute resume: %v", err)
	}
	if c1 != 1 {
		t.Fatalf("step one applied %d times, want 1", c1)
	}
	if c2 != 2 || c3 != 1 {
		t.Fatalf("calls two=%d three=%d, want 2 and 1", c2, c3)
	}
	for _, id := range []string{"m:one", "m:two", "m:three"} {
		if !st.Done[id] {
			t.Fatalf("%s not done", id)
		}
	}
	if env.In["server_ip"] != "1.2.3.4" {
		t.Fatalf("env.In not seeded from state: %v", env.In)
	}
}

func TestExecuteStopsOnCancelledContext(t *testing.T) {
	root := t.TempDir()
	var n int
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := &State{}
	err := Execute(ctx, []Step{countStep{id: "m:x", calls: &n}}, st, &Env{Root: root})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n != 0 {
		t.Fatalf("step applied after cancel")
	}
}

func TestStateFileMode(t *testing.T) {
	root := t.TempDir()
	st := &State{Done: map[string]bool{"a:b": true}, Outputs: Outputs{"k": "v"}}
	if err := st.Save(root); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(filepath.Join(root, ".vauban", "state.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Join(root, ".vauban"))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 700", di.Mode().Perm())
	}
	got, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !reflect.DeepEqual(got, st) {
		t.Fatalf("round trip = %+v, want %+v", got, st)
	}
}

func TestLoadStateAbsent(t *testing.T) {
	st, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.Done == nil || st.Outputs == nil || len(st.Done) != 0 || len(st.Outputs) != 0 {
		t.Fatalf("state = %+v, want empty initialised", st)
	}
}

func TestExecuteRejectsDuplicateIDs(t *testing.T) {
	var n int
	steps := []Step{countStep{id: "m:a", calls: &n}, countStep{id: "m:a", calls: &n}}
	err := Execute(context.Background(), steps, &State{}, &Env{Root: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), `duplicate step id "m:a"`) {
		t.Fatalf("err = %v, want duplicate step id", err)
	}
	if n != 0 {
		t.Fatalf("applied %d steps before rejecting", n)
	}
}

func TestExecuteNilConfirmUnusedByUngatedSteps(t *testing.T) {
	root := t.TempDir()
	steps := []Step{
		WriteFile{Path: "a.txt", Content: []byte("a")},
		RunCommand{StepID: "m:cmd", Cmd: Cmd{Name: "true"}},
	}
	env := &Env{Root: root, Run: &FakeRunner{}} // Confirm deliberately nil
	if err := Execute(context.Background(), steps, &State{}, env); err != nil {
		t.Fatalf("Execute with nil Confirm: %v", err)
	}
	if ErrNotConfirmed == nil || ErrNotConfirmed.Error() != "not confirmed" {
		t.Fatalf("ErrNotConfirmed = %v", ErrNotConfirmed)
	}
}

func TestExecuteReportsProgress(t *testing.T) {
	root := t.TempDir()
	var n int
	st := &State{Done: map[string]bool{"m:done": true}}
	var got []string
	env := &Env{Root: root, Progress: func(id, describe string, phase Phase, err error) {
		line := id + " " + describe + " " + phase.String()
		if err != nil {
			line += " " + err.Error()
		}
		got = append(got, line)
	}}
	err := Execute(context.Background(), []Step{
		countStep{id: "m:done", calls: &n},
		countStep{id: "m:ok", calls: &n},
		countStep{id: "m:bad", calls: &n, err: errors.New("boom")},
	}, st, env)
	if err == nil {
		t.Fatal("want error")
	}
	want := []string{
		"m:done m:done skip",
		"m:ok m:ok start",
		"m:ok m:ok done",
		"m:bad m:bad start",
		"m:bad m:bad fail boom",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("progress = %q, want %q", got, want)
	}
}

func TestExecRunnerInterruptsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	out, _ := ExecRunner{}.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", `trap 'echo graceful; exit 1' INT; sleep 5 >/dev/null 2>&1 & wait`}})
	if !strings.Contains(string(out), "graceful") {
		t.Fatalf("command was not interrupted gracefully, stdout %q", out)
	}
}
