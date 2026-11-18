package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

// Progress reports apply's steps, one line each. Styled: a spinner while the
// step runs, replaced by ✓ (done), · (skipped) or ✗ (failed). Plain:
// "[run] …", "[ok] …", "[skip] …", "[fail] …: <err>".
type Progress struct {
	w      io.Writer
	styled bool
	spin   bool // the terminal has a size; a 0x0 pty renders nothing
	prog   *tea.Program
	done   chan struct{}
	exists bool // the running step reported "skip (exists)"
	live   atomic.Pointer[tea.Program]
}

// NewProgress writes to w, styled when Styled() reports so.
func NewProgress(w io.Writer) *Progress {
	p := &Progress{w: w, styled: Styled()}
	if p.styled {
		width, _, err := term.GetSize(os.Stdout.Fd())
		p.spin = err == nil && width > 0
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
	if p.prog == nil {
		fmt.Fprintln(p.w, line)
		return
	}
	p.stop(line)
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

// Abort stops the running spinner program, if any, restoring the terminal.
// Unlike the other methods it is safe to call from another goroutine (for
// example just before a forced exit).
func (p *Progress) Abort() {
	if prog := p.live.Load(); prog != nil {
		prog.Kill()
	}
}

// Pause removes the running spinner line, if any, so the caller can print or
// prompt. The step's final line is printed when it finishes.
func (p *Progress) Pause() {
	if p.prog != nil {
		p.stop("")
	}
}

func (p *Progress) start(describe string) {
	if !p.spin {
		return
	}
	m := stepModel{spin: spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(Warn)), text: describe}
	p.prog = tea.NewProgram(m, tea.WithOutput(p.w), tea.WithInput(nil), tea.WithoutSignalHandler())
	p.live.Store(p.prog)
	p.done = make(chan struct{})
	go func(prog *tea.Program, done chan struct{}) {
		_, _ = prog.Run()
		close(done)
	}(p.prog, p.done)
}

// stop replaces the spinner line with final (or nothing) and waits for the
// program to exit.
func (p *Progress) stop(final string) {
	p.prog.Send(finishMsg(final))
	p.live.Store(nil)
	<-p.done
	p.prog = nil
}

type finishMsg string

// stepModel is one spinner line for the running step.
type stepModel struct {
	spin  spinner.Model
	text  string
	final *string
}

func (m stepModel) Init() tea.Cmd { return m.spin.Tick }

func (m stepModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case finishMsg:
		s := string(msg)
		m.final = &s
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m stepModel) View() tea.View {
	if m.final != nil {
		if *m.final == "" {
			return tea.NewView("")
		}
		// On exit the renderer erases from its last line down: end with a
		// newline so the final line stays.
		return tea.NewView(*m.final + "\n")
	}
	return tea.NewView(m.spin.View() + " " + m.text)
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
