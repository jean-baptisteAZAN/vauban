//go:build unix

package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets the test binary act as a helper process that, like vauban,
// cancels its context on SIGINT while ExecRunner runs a child.
func TestMain(m *testing.M) {
	if os.Getenv("VAUBAN_RUNNER_HELPER") == "1" {
		runnerHelper()
		return
	}
	os.Exit(m.Run())
}

// The scripts write $READY with the echo builtin (no foreground child that a
// SIGINT could interrupt); waitReady waits for the full line.

// countINT records its pid in $READY, then counts the SIGINTs it receives
// for two seconds and prints the count.
const countINT = `n=0; trap 'n=$((n+1))' INT; echo $$ > "$READY"; ` +
	`i=0; while [ $i -lt 20 ]; do sleep 0.1; i=$((i+1)); done; echo count=$n`

func runnerHelper() {
	// Cancel a moment after SIGINT so that, were the child in our group, the
	// group-wide SIGINT and the forwarded one could not coalesce.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-sig
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	out, err := ExecRunner{}.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", countINT}, Env: []string{"READY=" + os.Getenv("READY")}})
	fmt.Printf("%s", out)
	if err != nil {
		fmt.Println("err:", err)
	}
	os.Exit(0)
}

// waitReady waits for the child to write its pid to path.
func waitReady(path string) (int, error) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
			return strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0, errors.New("child never became ready")
}

// A terminal Ctrl-C signals the whole foreground process group. The child
// runs in its own group, so it gets exactly one SIGINT: the one ExecRunner
// forwards when the context is cancelled.
func TestExecRunnerForwardsOneSIGINT(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	helper := exec.Command(os.Args[0], "-test.run=^$")
	helper.Env = append(os.Environ(), "VAUBAN_RUNNER_HELPER=1", "READY="+ready)
	helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	helper.Stdout = &out
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = helper.Process.Kill() }() // may already be gone
	child, err := waitReady(ready)
	if err != nil {
		t.Fatal(err)
	}
	if pg, err := syscall.Getpgid(child); err != nil || pg != child {
		t.Fatalf("child pgid = %d (%v), want its own group %d", pg, err, child)
	}
	if err := syscall.Kill(-helper.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := helper.Wait(); err != nil {
		t.Fatalf("helper: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "count=1\n") {
		t.Fatalf("child should get exactly one SIGINT, got:\n%s", out.String())
	}
}

func TestExecRunnerInterruptsOnCancel(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if _, err := waitReady(ready); err == nil {
			cancel()
		}
	}()
	script := `trap 'echo graceful; exit 1' INT; echo $$ > "$READY"; sleep 10 >/dev/null 2>&1 & wait`
	out, _ := ExecRunner{}.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", script}, Env: []string{"READY=" + ready}})
	if !strings.Contains(string(out), "graceful") {
		t.Fatalf("command was not interrupted gracefully, stdout %q", out)
	}
}

// A child that ignores SIGINT is SIGKILLed, with its group, once Kill closes.
func TestExecRunnerKillForcesStop(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kill := make(chan struct{})
	go func() {
		if _, err := waitReady(ready); err == nil {
			cancel()
			time.Sleep(100 * time.Millisecond) // the ignored SIGINT has been sent
			close(kill)
		}
	}()
	script := `trap '' INT; echo $$ > "$READY"; sleep 30`
	start := time.Now()
	_, err := ExecRunner{Kill: kill}.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", script}, Env: []string{"READY=" + ready}})
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("err = %v, want killed", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("Run took %s after kill", d)
	}
}
