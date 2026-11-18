package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"github.com/charmbracelet/x/ansi"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
)

// Choice is a selectable value with an optional human label.
type Choice struct{ ID, Label string }

// Sources feeds AskConfig with the clouds and, per cloud, regions, sizes and
// defaults. Regions and Sizes may call cloud APIs; on error AskConfig falls
// back to a text input prefilled with the default.
type Sources struct {
	Clouds        []Choice
	DefaultRegion func(cloud string) string
	DefaultSize   func(cloud string) string
	Regions       func(ctx context.Context, cloud string) ([]Choice, error)
	Sizes         func(ctx context.Context, cloud, region string) ([]Choice, error)
	DefaultPort   int
}

const lookupTimeout = 20 * time.Second

// AskConfig asks for cloud, region, size, domain and port, starting from
// existing (which may be nil), and returns the resulting config.
func AskConfig(p *detect.Project, existing *config.Config, src Sources) (*config.Config, error) {
	c := config.Config{}
	if existing != nil {
		c = *existing
	}
	if c.Project == "" {
		c.Project = p.Name
	}
	prevCloud := c.Cloud

	if c.Cloud == "" && len(src.Clouds) > 0 {
		c.Cloud = src.Clouds[0].ID
	}
	cloudOpts := make([]huh.Option[string], 0, len(src.Clouds))
	for _, ch := range src.Clouds {
		cloudOpts = append(cloudOpts, huh.NewOption(cloudLabel(ch), ch.ID))
	}
	if err := RunForm(context.Background(), func() string { return Answer("Cloud", choiceLabel(src.Clouds, c.Cloud, cloudLabel)) },
		huh.NewSelect[string]().Title("Cloud").Description("where the server runs").Options(cloudOpts...).Value(&c.Cloud),
	); err != nil {
		return nil, err
	}
	if c.Cloud != prevCloud {
		c.Region, c.Size = "", ""
	}
	if c.Region == "" {
		c.Region = src.DefaultRegion(c.Cloud)
	}
	if c.Size == "" {
		c.Size = src.DefaultSize(c.Cloud)
	}

	regions, err := lookup("Fetching regions…", func(ctx context.Context) ([]Choice, error) {
		return src.Regions(ctx, c.Cloud)
	})
	if err := pick("Region", regions, err, &c.Region); err != nil {
		return nil, err
	}
	sizes, err := lookup("Fetching server sizes…", func(ctx context.Context) ([]Choice, error) {
		return src.Sizes(ctx, c.Cloud, c.Region)
	})
	if err := pick("Size", sizes, err, &c.Size); err != nil {
		return nil, err
	}

	if c.AppPort == 0 {
		c.AppPort = src.DefaultPort
	}
	port := strconv.Itoa(c.AppPort)
	summary := func() string {
		domain := strings.TrimSpace(c.Domain)
		if domain == "" {
			domain = "<ip>.sslip.io"
		}
		// the trailing newline leaves a blank line before what follows
		return Answer("Domain", domain) + "\n" + Answer("App port", strings.TrimSpace(port)) + "\n"
	}
	if err := RunForm(context.Background(), summary,
		huh.NewInput().Title("Domain").Description("must point to the server; leave empty to use sslip.io").
			Placeholder("empty = <ip>.sslip.io").Value(&c.Domain),
		huh.NewInput().Title("App port").Description("the port your app listens on in the container").
			Value(&port).Validate(func(s string) error { _, err := ParsePort(s); return err }),
	); err != nil {
		return nil, err
	}
	c.Domain = strings.TrimSpace(c.Domain)
	if c.AppPort, err = ParsePort(port); err != nil {
		return nil, err
	}
	return &c, nil
}

// ParsePort parses a TCP port in 1-65535.
func ParsePort(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, errors.New("port must be a number between 1 and 65535")
	}
	return n, nil
}

// lookup runs fetch behind a spinner.
func lookup(title string, fetch func(context.Context) ([]Choice, error)) ([]Choice, error) {
	var out []Choice
	var ferr error
	err := spinner.New().Title(" " + title).ActionWithErr(func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		defer cancel()
		out, ferr = fetch(ctx)
		return nil
	}).Run()
	if err != nil {
		return nil, err
	}
	return out, ferr
}

// pick asks for one of opts, or for free text prefilled with *value when the
// options could not be listed (the error is shown, shortened).
func pick(title string, opts []Choice, listErr error, value *string) error {
	ctx := context.Background()
	if listErr != nil || len(opts) == 0 {
		why := "no options returned"
		if listErr != nil {
			why = shortError(listErr)
		}
		return RunForm(ctx, func() string { return Answer(title, *value) },
			huh.NewInput().Title(title).Description(fmt.Sprintf("could not list them (%s); type a value", why)).
				Value(value).Validate(huh.ValidateNotEmpty()),
		)
	}
	options := make([]huh.Option[string], 0, len(opts))
	for _, o := range opts {
		options = append(options, huh.NewOption(optionLabel(o), o.ID))
	}
	return RunForm(ctx, func() string { return Answer(title, choiceLabel(opts, *value, optionLabel)) },
		huh.NewSelect[string]().Title(title).Options(options...).Height(min(len(options)+2, 12)).Value(value),
	)
}

// optionLabel is how a choice reads in a select: its label, prefixed with
// its ID unless the label already starts with it (cloud labels do, e.g.
// "fra1 · Frankfurt 1").
func optionLabel(c Choice) string {
	switch {
	case c.Label == "":
		return c.ID
	case strings.HasPrefix(c.Label, c.ID):
		return c.Label
	}
	return c.ID + " · " + c.Label
}

// choiceLabel is how the select showed the choice whose ID is id (show
// renders a choice), or id when it is not among choices.
func choiceLabel(choices []Choice, id string, show func(Choice) string) string {
	for _, c := range choices {
		if c.ID == id {
			return show(c)
		}
	}
	return id
}

// cloudLabel is how a cloud reads in the Cloud select.
func cloudLabel(c Choice) string { return c.Label }

// Answer is one line recapping a submitted field: "✓ Title: value" (no
// colon after a question).
func Answer(title, value string) string {
	if !strings.HasSuffix(title, "?") {
		title += ":"
	}
	return OK.Render("✓") + " " + Muted.Render(title) + " " + value
}

// RunForm runs fields as a one-group form and, once it is submitted,
// replaces it on screen with summary() (the answers, one line each). Errors
// match huh's Form.RunWithContext.
//
// A plain huh form ends on an empty view, which makes bubbletea's inline
// renderer shrink its frame; on a shrink it clamps its idea of the cursor row
// to the new, smaller frame, so it erases from the wrong row: after moving
// the cursor in a select, a stale partial form stayed on screen (e.g.
// "DigitalOcean" shown after choosing Hetzner). Instead, the last frame grows
// by one empty row, which leaves the cursor on that row (the renderer moves
// there after any height change), and RunForm then moves up over the frame,
// erases it and prints the summary.
func RunForm(ctx context.Context, summary func() string, fields ...huh.Field) error {
	form := huh.NewForm(huh.NewGroup(fields...))
	form.SubmitCmd = tea.Quit
	form.CancelCmd = tea.Interrupt
	m := answeredForm{form: form, last: new(string)}
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	switch {
	case form.State == huh.StateAborted || errors.Is(err, tea.ErrInterrupted):
		return huh.ErrUserAborted
	case errors.Is(err, tea.ErrProgramKilled):
		return huh.ErrTimeout
	case err != nil:
		return fmt.Errorf("huh: %w", err)
	}
	if form.State == huh.StateCompleted {
		fmt.Fprint(os.Stdout, eraseUp(lineCount(*m.last))+summary()+"\n")
	}
	return nil
}

// eraseUp moves the cursor up n rows to the first column and erases from
// there to the end of the screen.
func eraseUp(n int) string {
	s := "\r"
	if n > 0 {
		s += ansi.CursorUp(n)
	}
	return s + ansi.EraseScreenBelow
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// answeredForm runs form, remembering its last view (*last); once submitted
// it shows that view plus an empty row (see RunForm).
type answeredForm struct {
	form *huh.Form
	last *string
}

func (m answeredForm) Init() tea.Cmd { return m.form.Init() }

func (m answeredForm) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.form.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		m.form = f
	}
	return m, cmd
}

func (m answeredForm) View() tea.View {
	if m.form.State == huh.StateCompleted {
		return tea.NewView(*m.last + "\n")
	}
	v := tea.NewView(m.form.View())
	*m.last = v.Content
	v.ReportFocus = true // as huh's own Run does
	return v
}

// shortError keeps the first line of err, capped, for display in a form.
func shortError(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	if len(s) > 120 {
		s = strings.ToValidUTF8(s[:117], "") + "…"
	}
	return s
}
