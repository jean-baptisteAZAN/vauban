package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/ui"
)

var (
	errNeedTTY      = errors.New("apply needs a terminal or --yes")
	errNotConfirmed = errors.New("destructive infra change not confirmed — nothing was applied; rerun and type the project name (or pass --force)")
	errInterrupted  = errors.New("interrupted — rerun `vauban apply` to resume")
)

func newApplyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Write the files, create the server and wire deploys, as shown by plan",
		Long: "Write the files, create the server and wire deploys, as shown by plan.\n\n" +
			"Asks before starting (--yes skips that). Destroying or replacing cloud resources\n" +
			"always needs the project name typed in a terminal, or --force.\n" +
			"Interrupted runs resume where they stopped.",
		Args: cobra.NoArgs,
		RunE: abortable(runApply),
	}
	addConfigFlags(cmd)
	return cmd
}

func runApply(cmd *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal, restore the default handling so a second
	// Ctrl-C kills vauban.
	go func() { <-ctx.Done(); stop() }()

	f := cmd.Flags()
	dir, _ := f.GetString("dir")
	force, _ := f.GetBool("force")
	yes, _ := f.GetBool("yes")
	tty := interactive()
	if !tty && !yes {
		return &ExitError{Code: 2, Err: errNeedTTY}
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	p, err := detect.Scan(root)
	if err != nil {
		return err
	}
	c, err := resolveConfig(cmd, p)
	if err != nil {
		return err
	}
	pl, err := preparePlan(root, c, force)
	if err != nil {
		return err
	}
	if err := c.Save(root); err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	if err := engine.Preflight(pl.p, c, pl.mods); err != nil {
		ui.Warnings(w, "cannot apply:", strings.Split(err.Error(), "\n"))
		return &ExitError{Code: 1, Err: errors.New("preflight failed — nothing was applied")}
	}
	writeConfigSummary(w, p, c)
	ui.RenderPlan(w, pl.items(root, force))
	writeDrift(w, pl.st, c, pl.infraStale)
	fmt.Fprintln(w)

	if !yes {
		ok := false
		if err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title("Apply this plan?").Value(&ok),
		)).RunWithContext(ctx); err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(w, "nothing applied")
			return nil
		}
	}

	prog := ui.NewProgress(w)
	env := &engine.Env{
		Root:     root,
		Force:    force,
		Run:      cmdRunner,
		Confirm:  confirmDestructive(ctx, w, prog, c.Project, force, tty),
		Progress: prog.Update,
	}
	if !ui.Styled() {
		env.Log = w // "skip (exists)" notes; they would break the spinner
	}
	err = engine.Execute(ctx, pl.steps(), pl.st, env)
	prog.Pause()
	switch {
	case ctx.Err() != nil:
		return &ExitError{Code: 130, Err: errInterrupted}
	case errors.Is(err, engine.ErrNotConfirmed):
		return &ExitError{Code: 3, Err: errNotConfirmed}
	case err != nil:
		return err
	}
	ui.ApplyFooter(w, pl.st.Outputs["domain"])
	return nil
}

// confirmDestructive is env.Confirm for apply. It always shows the plan,
// then approves with --force, else asks for the project name in a terminal,
// else refuses. --yes does not approve destruction.
func confirmDestructive(ctx context.Context, w io.Writer, prog *ui.Progress, project string, force, tty bool) func(title, details string) (bool, error) {
	return func(title, details string) (bool, error) {
		prog.Pause()
		ui.DestructivePlan(w, title, details)
		if force {
			ui.Warning(w, "--force: going ahead without typed confirmation")
			return true, nil
		}
		if !tty {
			return false, nil
		}
		return typedProject(ctx, project, "to apply these changes")
	}
}

// typedProject asks for the project name and reports whether it matches.
func typedProject(ctx context.Context, project, purpose string) (bool, error) {
	var typed string
	err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Type the project name " + purpose).
			Description("project: " + project).Value(&typed),
	)).RunWithContext(ctx)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(typed) == project, nil
}
