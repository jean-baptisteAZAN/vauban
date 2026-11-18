package ui

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

func TestProgressExistingFileIsSkip(t *testing.T) {
	for _, styled := range []bool{false, true} {
		var buf bytes.Buffer
		p := &Progress{w: &buf, styled: styled}
		p.Update("file:Dockerfile", "write Dockerfile", engine.PhaseStart, nil)
		fmt.Fprintf(p.Log(), "skip (exists) Dockerfile\n")
		p.Update("file:Dockerfile", "write Dockerfile", engine.PhaseDone, nil)
		p.Update("file:x", "write x", engine.PhaseStart, nil)
		p.Update("file:x", "write x", engine.PhaseDone, nil)
		out := buf.String()
		if strings.Contains(out, "skip (exists)") || strings.Contains(out, "✓ write Dockerfile") || strings.Contains(out, "[ok] write Dockerfile") {
			t.Errorf("styled=%v: existing file shown as written:\n%s", styled, out)
		}
		if !strings.Contains(out, "write Dockerfile (exists)") || !(strings.Contains(out, "✓") || strings.Contains(out, "[ok] write x")) {
			t.Errorf("styled=%v: unexpected output:\n%s", styled, out)
		}
	}
}

func TestFitLineTruncatesWithEllipsis(t *testing.T) {
	if got := fitLine("abcdef", 5); got != "abc…" {
		t.Errorf("fitLine = %q, want %q", got, "abc…")
	}
	if got := fitLine("abc", 5); got != "abc" {
		t.Errorf("short line changed: %q", got)
	}
	if got := fitLine("abcdef", 0); got != "abcdef" {
		t.Errorf("unknown width truncated: %q", got)
	}
	styled := OK.Render("✓") + " " + strings.Repeat("x", 50)
	got := fitLine(styled, 21)
	if w := ansi.StringWidth(got); w != 20 || !strings.HasSuffix(ansi.Strip(got), "…") {
		t.Errorf("fitLine(styled) = %q (width %d)", got, w)
	}
}

// The spinner must not query the terminal (DECRQM): nothing reads the reply,
// so the terminal echoes it onto the screen and the shell prompt.
func TestSpinnerSendsNoTerminalQueries(t *testing.T) {
	var buf safeBuffer
	p := &Progress{w: &buf, styled: true, width: func() int { return 30 }}
	p.Update("a", "write a", engine.PhaseStart, nil)
	time.Sleep(250 * time.Millisecond) // a few spinner frames
	p.Update("a", "write a", engine.PhaseDone, nil)
	long := "$ tofu -chdir=infra plan -input=false -no-color -out=tfplan"
	p.Update("b", long, engine.PhaseStart, nil)
	p.Update("b", long, engine.PhaseDone, nil)
	p.Update("c", "write c", engine.PhaseStart, nil)
	p.Abort()
	out := buf.String()
	for _, q := range []string{"$p", "\x1b[?2026", "\x1b[?2027"} {
		if strings.Contains(out, q) {
			t.Errorf("output contains %q: %q", q, out)
		}
	}
	if !strings.Contains(out, "✓") || !strings.Contains(out, "write a\n") {
		t.Errorf("done line missing: %q", out)
	}
	if strings.Contains(out, "out=tfplan") || !strings.Contains(out, "…\n") {
		t.Errorf("long line not truncated with an ellipsis: %q", out)
	}
	if !strings.HasSuffix(out, ansi.ShowCursor) {
		t.Errorf("cursor not restored at the end: %q", out)
	}
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
