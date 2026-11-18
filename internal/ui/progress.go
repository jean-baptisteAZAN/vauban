package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

// Progress reports apply's steps, one line each. Styled: a spinner while the
// step runs, replaced by ✓ (done), · (skipped) or ✗ (failed), each line cut
// to the terminal width with "…". Plain: "[run] …", "[ok] …", "[skip] …",
// "[fail] …: <err>".
//
// The spinner is drawn directly (carriage return, erase line) rather than by
// a bubbletea program: a program started per step queries the terminal
// (DECRQM for modes 2026/2027) without reading stdin, so the terminal's reply
// was echoed onto the screen and, after exit, onto the shell prompt.
type Progress struct {
	w      io.Writer
	styled bool
	// width reports the terminal's columns; nil, or 0 when unknown (no
	// spinner, no truncation). A 0x0 pty renders no spinner.
	width  func() int
	exists bool // the running step reported "skip (exists)"

	mu  sync.Mutex // guards cur and spinner drawing (Abort may run concurrently)
	cur *spinLine
}

// spinLine is the running step's spinner line.
type spinLine struct {
	text  string
	frame int
	stop  chan struct{}
	done  chan struct{}
}

var spinStyle = spinner.MiniDot

// NewProgress writes to w, styled when Styled() reports so.
func NewProgress(w io.Writer) *Progress {
	p := &Progress{w: w, styled: Styled()}
	if p.styled {
		p.width = func() int {
			cols, _, err := term.GetSize(os.Stdout.Fd())
			if err != nil {
				return 0
			}
			return cols
		}
	}
	return p
}

// Update has the signature of engine.Env.Progress.
func (p *Progress) Update(_, describe string, phase engine.Phase, err error) {
	switch phase {
	case engine.PhaseStart:
		p.exists = false
	case engine.PhaseDone:
		if p.exists {
			phase, describe = engine.PhaseSkip, describe+" (exists)"
		}
	}
	if !p.styled {
		switch phase {
		case engine.PhaseStart:
			fmt.Fprintln(p.w, "[run] "+describe)
		case engine.PhaseDone:
			fmt.Fprintln(p.w, "[ok] "+describe)
		case engine.PhaseSkip:
			fmt.Fprintln(p.w, "[skip] "+describe)
		case engine.PhaseFail:
			fmt.Fprintf(p.w, "[fail] %s: %v\n", describe, err)
		}
		return
	}
	var line string
	switch phase {
	case engine.PhaseStart:
		p.Pause()
		p.start(describe)
		return
	case engine.PhaseDone:
		line = OK.Render("✓") + " " + describe
	case engine.PhaseSkip:
		line = Muted.Render("· " + describe)
	case engine.PhaseFail:
		line = Err.Render("✗") + " " + describe + Muted.Render(": "+fmt.Sprint(err))
	}
	p.finish(line)
}

// Log returns a writer for engine.Env.Log. A "skip (exists) <path>" note
// turns the running step's final line into a skip; other notes are printed.
func (p *Progress) Log() io.Writer { return progressLog{p} }

type progressLog struct{ p *Progress }

func (l progressLog) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.HasPrefix(line, "skip (exists) ") {
			l.p.exists = true
			continue
		}
		l.p.Pause()
		if l.p.styled {
			line = Muted.Render(line)
		}
		fmt.Fprintln(l.p.w, line)
	}
	return len(b), nil
}

// Abort removes the running spinner line, if any, and shows the cursor again.
// Unlike the other methods it is safe to call from another goroutine (for
// example just before a forced exit).
func (p *Progress) Abort() { p.finish("") }

// Pause removes the running spinner line, if any, so the caller can print or
// prompt. The step's final line is printed when it finishes.
func (p *Progress) Pause() { p.finish("") }

// cols is the terminal width, 0 when unknown.
func (p *Progress) cols() int {
	if p.width == nil {
		return 0
	}
	return p.width()
}

func (p *Progress) start(describe string) {
	if p.cols() <= 0 {
		return
	}
	l := &spinLine{text: describe, stop: make(chan struct{}), done: make(chan struct{})}
	p.mu.Lock()
	p.cur = l
	fmt.Fprint(p.w, ansi.HideCursor)
	p.draw(l)
	p.mu.Unlock()
	go p.spin(l)
}

// spin redraws l at the spinner's pace until l.stop is closed.
func (p *Progress) spin(l *spinLine) {
	defer close(l.done)
	tick := time.NewTicker(spinStyle.FPS)
	defer tick.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-tick.C:
			p.mu.Lock()
			if p.cur == l {
				l.frame = (l.frame + 1) % len(spinStyle.Frames)
				p.draw(l)
			}
			p.mu.Unlock()
		}
	}
}

// draw rewrites the current line with l's frame and text. p.mu is held.
func (p *Progress) draw(l *spinLine) {
	line := Warn.Render(spinStyle.Frames[l.frame]) + " " + l.text
	fmt.Fprint(p.w, "\r"+ansi.EraseEntireLine+fitLine(line, p.cols()))
}

// finish replaces the spinner line, if any, with final (nothing when final is
// ""), stops the spinner and shows the cursor again.
func (p *Progress) finish(final string) {
	p.mu.Lock()
	l := p.cur
	p.cur = nil
	if l != nil {
		fmt.Fprint(p.w, "\r"+ansi.EraseEntireLine)
	}
	if final != "" {
		fmt.Fprintln(p.w, fitLine(final, p.cols()))
	}
	if l != nil {
		fmt.Fprint(p.w, ansi.ShowCursor)
	}
	p.mu.Unlock()
	if l != nil {
		close(l.stop)
		<-l.done
	}
}

// fitLine cuts s (which may hold ANSI styles) to width-1 columns, ending it
// with "…" when cut. Leaving the last column free avoids terminals that wrap
// as soon as it is written. width <= 0 means unknown: s is returned as is.
func fitLine(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Truncate(s, width-1, "…")
}

// Footer is what apply reports once every step succeeded.
type Footer struct {
	Domain string   // the public domain, "" if unknown
	DNS    []string // records the user must create, e.g. "A example.com → 1.2.3.4"
	// Redeploy: only files, the domain or secrets changed (no infra change),
	// so a push redeploys.
	Redeploy bool
}

// ApplyFooter writes the success line, the DNS records to create and the
// next git command.
func ApplyFooter(w io.Writer, f Footer) {
	next := `next: git add -A && git commit -m "chore: vauban setup" && git push`
	if f.Redeploy {
		next = `push to redeploy: git add -A && git commit -m "chore: vauban update" && git push`
	}
	url := ""
	if f.Domain != "" {
		url = " · https://" + f.Domain
	}
	fmt.Fprintln(w)
	styled := Styled()
	if styled {
		fmt.Fprintln(w, OK.Render("✓ ready")+url)
	} else {
		fmt.Fprintln(w, "[ok] ready"+url)
	}
	for _, r := range f.DNS {
		line := "DNS: create " + r
		if styled {
			line = Warn.Render(line)
		}
		fmt.Fprintln(w, line)
	}
	if styled {
		next = Muted.Render(next)
	}
	fmt.Fprintln(w, next)
}
