package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

const (
	msgStopping = "stopping gracefully… press Ctrl-C again to force"
	msgForced   = "forced stop — tofu state may be locked; run `tofu -chdir=infra force-unlock <id>` if the next apply reports a lock"
)

var (
	// gracefulTimeout is how long, without a terminal, a stopping command
	// may take after the first signal before it is force-killed.
	gracefulTimeout = 5 * time.Minute
	// forceExitDelay is how long vauban waits, after a forced stop, for the
	// command to return before it exits 130 itself.
	forceExitDelay = 5 * time.Second
	// notifySignals subscribes c to SIGINT and SIGTERM. Tests replace it.
	notifySignals = func(c chan<- os.Signal) (stop func()) {
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		return func() { signal.Stop(c) }
	}
)

type interruptOpts struct {
	tty            bool
	graceful       time.Duration // non-TTY only; 0 = never force on its own
	forceExitDelay time.Duration
	exit           func(code int)
}

// interrupter turns SIGINT/SIGTERM into a graceful, then forced, stop.
// Children run in their own process group (engine.ExecRunner), so a
// terminal Ctrl-C reaches only vauban:
//   - the first signal cancels ctx; ExecRunner forwards one SIGINT to the
//     running command (tofu stops cleanly and saves its state);
//   - a second signal (or, without a terminal, gracefulTimeout) closes kill:
//     ExecRunner SIGKILLs the command's group, and if the command still has
//     not returned after forceExitDelay vauban exits 130.
type interrupter struct {
	kill   chan struct{}
	forced atomic.Bool
	stop   func()
}

// watchSignals sets up an interrupter on the process signals. Call stop when
// the command returns.
func watchSignals(parent context.Context, w io.Writer, tty bool) (context.Context, *interrupter) {
	sigs := make(chan os.Signal, 2)
	unsubscribe := notifySignals(sigs)
	ctx, in := newInterrupter(parent, w, sigs, interruptOpts{
		tty: tty, graceful: gracefulTimeout, forceExitDelay: forceExitDelay, exit: os.Exit,
	})
	stop := in.stop
	in.stop = func() { unsubscribe(); stop() }
	return ctx, in
}

func newInterrupter(parent context.Context, w io.Writer, sigs <-chan os.Signal, o interruptOpts) (context.Context, *interrupter) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	var once sync.Once
	in := &interrupter{kill: make(chan struct{})}
	in.stop = func() { once.Do(func() { close(done); cancel() }) }
	go func() {
		select {
		case <-sigs:
		case <-done:
			return
		}
		fmt.Fprintln(w, msgStopping)
		cancel()

		var timeout <-chan time.Time
		if !o.tty && o.graceful > 0 {
			t := time.NewTimer(o.graceful)
			defer t.Stop()
			timeout = t.C
		}
		select {
		case <-sigs:
		case <-timeout:
		case <-done:
			return
		}
		in.forced.Store(true)
		fmt.Fprintln(w, msgForced)
		close(in.kill)

		t := time.NewTimer(o.forceExitDelay)
		defer t.Stop()
		select {
		case <-t.C:
			o.exit(130)
		case <-done:
		}
	}()
	return ctx, in
}

// runner wires an ExecRunner to the forced stop; other runners pass through.
func (in *interrupter) runner(r engine.Runner) engine.Runner {
	if er, ok := r.(engine.ExecRunner); ok {
		er.Kill = in.kill
		return er
	}
	return r
}
