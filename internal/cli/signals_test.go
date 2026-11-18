package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s not closed", what)
	}
}

func TestInterrupterGracefulThenForced(t *testing.T) {
	var buf bytes.Buffer
	sigs := make(chan os.Signal, 2)
	exited := make(chan int, 1)
	ctx, in := newInterrupter(context.Background(), &buf, sigs, interruptOpts{tty: true, forceExitDelay: 50 * time.Millisecond, exit: func(c int) { exited <- c }})

	sigs <- os.Interrupt
	waitClosed(t, ctx.Done(), "ctx")
	if buf.String() != msgStopping+"\n" {
		t.Fatalf("after first signal: %q", buf.String())
	}
	select {
	case <-in.kill:
		t.Fatal("killed after one signal in a TTY")
	case <-time.After(50 * time.Millisecond):
	}

	sigs <- syscall.SIGTERM
	waitClosed(t, in.kill, "kill")
	if !in.forced.Load() || !strings.HasSuffix(buf.String(), msgForced+"\n") {
		t.Fatalf("after second signal: forced=%v %q", in.forced.Load(), buf.String())
	}
	// Nothing returned in time: the backstop exits 130.
	select {
	case c := <-exited:
		if c != 130 {
			t.Fatalf("exit code %d", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no backstop exit")
	}
	in.stop()
}

func TestInterrupterNonTTYForcesAfterTimeout(t *testing.T) {
	var buf bytes.Buffer
	sigs := make(chan os.Signal, 2)
	_, in := newInterrupter(context.Background(), &buf, sigs, interruptOpts{graceful: 20 * time.Millisecond, forceExitDelay: time.Hour, exit: func(int) {}})
	defer in.stop()
	sigs <- os.Interrupt
	waitClosed(t, in.kill, "kill")
	if !in.forced.Load() {
		t.Fatal("not forced")
	}
}

func TestInterrupterStopCancelsBackstop(t *testing.T) {
	var buf bytes.Buffer
	sigs := make(chan os.Signal, 2)
	exited := make(chan int, 1)
	_, in := newInterrupter(context.Background(), &buf, sigs, interruptOpts{tty: true, forceExitDelay: 100 * time.Millisecond, exit: func(c int) { exited <- c }})
	sigs <- os.Interrupt
	sigs <- os.Interrupt
	waitClosed(t, in.kill, "kill")
	in.stop()
	select {
	case <-exited:
		t.Fatal("backstop exit after stop")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestInterrupterRunnerGetsKill(t *testing.T) {
	_, in := newInterrupter(context.Background(), &bytes.Buffer{}, make(chan os.Signal), interruptOpts{exit: func(int) {}})
	defer in.stop()
	er, ok := in.runner(engine.ExecRunner{}).(engine.ExecRunner)
	if !ok || er.Kill == nil {
		t.Fatal("ExecRunner not wired to kill")
	}
	fr := &engine.FakeRunner{}
	if in.runner(fr) != fr {
		t.Fatal("other runners must pass through")
	}
}

// The first signal during apply cancels Execute gracefully: exit 130 with the
// resume message, and the stopping notice on stderr.
func TestApplySignalGraceful(t *testing.T) {
	root, fr := applyEnv(t)
	sigs := make(chan os.Signal, 2)
	old := notifySignals
	notifySignals = func(c chan<- os.Signal) func() {
		go func() {
			for s := range sigs {
				c <- s
			}
		}()
		return func() {}
	}
	t.Cleanup(func() { notifySignals = old })
	setRunner(t, signalRunner{FakeRunner: fr, on: "tofu -chdir=infra init -input=false", sigs: sigs})

	out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes", "--cloud", "hetzner")
	wantExit(t, err, 130, "interrupted — rerun `vauban apply` to resume", out)
	if !strings.Contains(out, msgStopping) {
		t.Errorf("missing %q in:\n%s", msgStopping, out)
	}
	st, _ := engine.LoadState(root)
	if st.Done["infra:init"] {
		t.Error("interrupted step marked done")
	}
}

// signalRunner sends SIGINT on `on`, then behaves like a child that stops
// once interrupted.
type signalRunner struct {
	*engine.FakeRunner
	on   string
	sigs chan<- os.Signal
}

func (s signalRunner) Run(ctx context.Context, cmd engine.Cmd) ([]byte, error) {
	out, err := s.FakeRunner.Run(ctx, cmd)
	if strings.Join(append([]string{cmd.Name}, cmd.Args...), " ") == s.on {
		s.sigs <- os.Interrupt
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return out, err
}
