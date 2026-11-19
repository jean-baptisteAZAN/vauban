package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/remote"
	"github.com/jean-baptisteAZAN/vauban/internal/ui"
)

func newAuditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "audit",
		Short: "Read-only check of the existing server (target: existing)",
		Args:  cobra.NoArgs,
		RunE:  runAudit,
	}
}

func runAudit(cmd *cobra.Command, _ []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	c, err := config.Load(root)
	if errors.Is(err, fs.ErrNotExist) {
		return &ExitError{Code: 2, Err: fmt.Errorf("no %s in %s: run vauban plan and choose \"Use an existing server\" first", config.FileName, root)}
	}
	if err != nil {
		return err
	}
	if !c.Existing() || c.Server == nil {
		return &ExitError{Code: 2, Err: errors.New("audit needs target: existing in vauban.yaml")}
	}
	p, err := detect.Scan(root)
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	if p.GitHubRepo == "" {
		ui.Warning(w, "no GitHub remote detected: the project directory ownership check cannot match")
	}
	t, err := remote.TargetFor(root, c.Server)
	if err != nil {
		return err
	}
	a, err := remote.RunAudit(cmd.Context(), cmdRunner, t, c.Project, p.GitHubRepo)
	if err != nil {
		return err
	}
	ui.Heading(w, fmt.Sprintf("%s · %s@%s", c.Project, t.User, t.Host))
	ui.RenderAudit(w, a)
	if g := remote.CaddyGuide(a); a.Caddy == remote.CaddyUserNeedsSetup && g != "" {
		ui.Section(w, "caddy setup")
		fmt.Fprintln(w, g)
	}
	if b := a.Blockers(); len(b) > 0 {
		ui.Warnings(w, "cannot apply:", b)
		return &ExitError{Code: 1}
	}
	return nil
}
