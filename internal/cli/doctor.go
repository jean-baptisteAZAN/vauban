package cli

import (
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module"
	"github.com/jean-baptisteAZAN/vauban/internal/tools"
	"github.com/jean-baptisteAZAN/vauban/internal/ui"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report the detected stack, what is missing and which tools are installed",
		Args:  cobra.NoArgs,
		RunE:  runDoctor,
	}
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	p, err := detect.Scan(dir)
	if err != nil {
		return err
	}
	c, _ := config.Load(dir) // no (or unreadable) vauban.yaml: report everything
	writeDoctorFor(cmd.OutOrStdout(), p, c, module.All(), tools.Check())
	return nil
}

func markFor(s engine.Status) string {
	switch s {
	case engine.StatusPresent:
		return "ok"
	case engine.StatusPartial:
		return "partial"
	}
	return "missing"
}

// writeDoctorFor writes the report with the project's config (nil when there
// is none): it shows the target and hides the module the other target never uses.
func writeDoctorFor(w io.Writer, p *detect.Project, c *config.Config, mods []engine.Module, ts []tools.Tool) {
	ui.Heading(w, p.Name)

	stack := "unknown"
	if p.Stack != "" {
		stack = string(p.Stack)
		if detail := strings.TrimSpace(p.PackageManager + " " + p.LangVersion); detail != "" {
			stack += " (" + detail + ")"
		}
	}
	ui.Row(w, "stack", stack)

	gh := p.GitHubRepo
	if gh == "" {
		gh = "none"
	}
	ui.Row(w, "github", gh)

	if c != nil && c.Existing() && c.Server != nil {
		ui.Row(w, "target", "existing ("+c.Server.User+"@"+c.Server.Host+")")
	}

	var parts []string
	for _, m := range mods {
		if c != nil && ((m.ID() == "server") != c.Existing()) && (m.ID() == "server" || m.ID() == "infra") {
			continue
		}
		parts = append(parts, ui.Mark(markFor(m.Detect(p)))+" "+m.ID())
	}
	if len(parts) == 0 {
		parts = []string{ui.Hint("none registered")}
	}
	ui.Row(w, "modules", strings.Join(parts, "  "))

	parts = nil
	for _, t := range ts {
		s := ui.Mark(markFor(engine.StatusMissing)) + " " + t.Name
		if t.Found {
			s = ui.Mark("ok") + " " + t.Name
		} else if t.Hint != "" {
			s += ui.Hint(" → " + t.Hint)
		}
		parts = append(parts, s)
	}
	ui.Row(w, "tools", strings.Join(parts, "  "))
	next := "vauban plan"
	if p.GitHubRepo == "" {
		commit, create := remoteSteps(p)
		next = create
		if commit != "" {
			next = "commit first (" + commit + "), then " + create
		}
	}
	ui.Row(w, "next", next)
}
