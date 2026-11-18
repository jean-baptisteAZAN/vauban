package ui

import (
	"fmt"
	"io"
	"os"

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

// ApplyFooter writes the success line and the commit hint.
func ApplyFooter(w io.Writer, domain string) {
	const next = `next: git add -A && git commit -m "chore: vauban setup" && git push`
	url := ""
	if domain != "" {
		url = " · https://" + domain
	}
	fmt.Fprintln(w)
	if Styled() {
		fmt.Fprintln(w, OK.Render("✓ ready")+url)
		fmt.Fprintln(w, Muted.Render(next))
		return
	}
	fmt.Fprintln(w, "[ok] ready"+url)
	fmt.Fprintln(w, next)
}
