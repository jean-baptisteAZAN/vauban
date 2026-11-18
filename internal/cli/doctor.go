package cli

import (
	"io"
	"strings"

	"github.com/spf13/cobra"

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
	writeDoctor(cmd.OutOrStdout(), p, module.All(), tools.Check())
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

func writeDoctor(w io.Writer, p *detect.Project, mods []engine.Module, ts []tools.Tool) {
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

	var parts []string
	for _, m := range mods {
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
