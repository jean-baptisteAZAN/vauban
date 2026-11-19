package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/ui"
)

// quotaError matches the (redacted) cloud errors of a refused server, on
// the infra:apply step only:
// Scaleway "quota exceeded(s)", Hetzner "resource_limit_exceeded",
// DigitalOcean "droplet limit".
var quotaError = regexp.MustCompile(`(?i)quota|limit[_ ]exceeded|droplet limit|limit reached`)

var (
	errNeedTTY      = errors.New("apply needs a terminal or --yes")
	errNotConfirmed = errors.New("destructive infra change not confirmed — nothing was applied; rerun and type the project name (or pass --confirm <project>)")
	errInterrupted  = errors.New("interrupted — rerun `vauban apply` to resume")
)

func newApplyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Write the files, create the server and wire deploys, as shown by plan",
		Long: "Write the files, create the server and wire deploys, as shown by plan.\n\n" +
			"Asks before starting (--yes skips that). Destroying or replacing cloud resources\n" +
			"always needs the project name typed in a terminal, or --confirm <project>;\n" +
			"--yes and --force (which only overwrites generated files) do not approve it.\n" +
			"Interrupted runs resume where they stopped.",
		Args: cobra.NoArgs,
		RunE: abortable(runApply),
	}
	addConfigFlags(cmd)
	cmd.Flags().String("confirm", "", "the project name, to approve destroying or replacing resources without a terminal")
	return cmd
}

func runApply(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()
	dir, _ := f.GetString("dir")
	force, _ := f.GetBool("force")
	yes, _ := f.GetBool("yes")
	tty := interactive()
	if !tty && !yes {
		return &ExitError{Code: 2, Err: errNeedTTY}
	}
	ctx, sig := watchSignals(cmd.Context(), cmd.ErrOrStderr(), tty)
	defer sig.stop()
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	p, err := detect.Scan(root)
	if err != nil {
		return err
	}
	if err := requireRemote(p); err != nil {
		return err
	}
	c, err := resolveConfig(cmd, p)
	if err != nil {
		return err
	}
	if err := saveConfig(root, c); err != nil {
		return err
	}
	confirmed := false
	if f.Changed("confirm") {
		if typed, _ := f.GetString("confirm"); typed != c.Project {
			return &ExitError{Code: 2, Err: fmt.Errorf("--confirm %q does not match project %q — nothing was applied", typed, c.Project)}
		}
		confirmed = true
	}
	pl, err := preparePlan(root, c, force)
	if err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	notices, err := engine.Preflight(pl.p, c, pl.mods, pl.preflightOptions(root, c))
	if err != nil {
		ui.Warnings(w, "cannot apply:", append(strings.Split(err.Error(), "\n"), notices.Warnings...))
		return &ExitError{Code: 1, Err: errors.New("preflight failed — nothing was applied")}
	}
	ui.Warnings(w, "warnings:", notices.Warnings)
	writeNotes(w, notices.Notes)
	writeConfigSummary(w, p, c)
	if c.Existing() {
		// The server as it is now, before asking: blockers or a pending
		// Caddy change stop here, before any change. The server:audit step
		// runs it again on a first apply (its outputs feed the other steps).
		fmt.Fprintln(w)
		if hw := hostWarning(c.Server.Host); hw != "" {
			ui.Warning(w, hw)
		}
		if pw := portWarning(pl.st, c, force); pw != "" {
			ui.Warning(w, pw)
		}
		lines, stop := writeAudit(ctx, w, root, p.GitHubRepo, c)
		if stop {
			ui.Warnings(w, "cannot apply:", lines)
			return &ExitError{Code: 1, Err: errors.New("the server is not ready (see above) — nothing was applied")}
		}
	}
	ui.RenderPlan(w, pl.items(root, force))
	writeDrift(w, root, pl.st, c, pl.infraStale)
	writeSiteChange(w, pl, c)
	fmt.Fprintln(w)

	if !yes {
		ok := false
		answer := func() string {
			if ok {
				return ui.Answer("Apply this plan?", "Yes")
			}
			return ui.Answer("Apply this plan?", "No")
		}
		if err := ui.RunForm(ctx, answer, huh.NewConfirm().Title("Apply this plan?").Value(&ok)); err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(w, "nothing applied")
			return nil
		}
	}

	prog := ui.NewProgress(w)
	sig.beforeExit(prog.Abort)
	env := &engine.Env{
		Root:     root,
		Force:    force,
		Run:      sig.runner(cmdRunner),
		Log:      prog.Log(),
		Confirm:  confirmDestructive(ctx, w, prog, c.Project, confirmed, tty),
		Progress: prog.Update,
	}
	before := maps.Clone(pl.st.Done)
	err = engine.Execute(ctx, pl.steps(), pl.st, env)
	prog.Pause()
	switch {
	case err != nil && sig.forced.Load():
		return &ExitError{Code: 130} // the forced-stop notice is already printed
	case err != nil && ctx.Err() != nil:
		return &ExitError{Code: 130, Err: errInterrupted}
	case errors.Is(err, engine.ErrNotConfirmed):
		return &ExitError{Code: 3, Err: errNotConfirmed}
	case err != nil && strings.HasPrefix(err.Error(), "infra:apply:") && quotaError.MatchString(err.Error()):
		return fmt.Errorf("%w\n%s", err, cloud.QuotaHint(c.Cloud, c.Region, c.Size))
	case err != nil:
		return err
	}
	ui.ApplyFooter(w, footer(c, pl.st, before))
	if c.Existing() {
		fmt.Fprintln(w, ui.Hint("deploys run from GitHub Actions: push to main to deploy https://"+c.Domain))
	}
	return nil
}

// footer describes the finished run from the final state: the domain, the
// DNS records a custom domain needs, and whether only files, the domain or
// secrets changed (steps newly done since before) so a push redeploys.
func footer(c *config.Config, st *engine.State, before map[string]bool) ui.Footer {
	f := ui.Footer{Domain: st.Outputs["domain"]}
	if c.Existing() {
		f.Domain = c.Domain
	}
	if c.Domain != "" && !c.Existing() { // an existing server's DNS is the plan's line
		if ip := st.Outputs["server_ip"]; ip != "" {
			f.DNS = append(f.DNS, "A "+c.Domain+" → "+ip)
		}
		if ip6 := st.Outputs["server_ipv6"]; ip6 != "" {
			f.DNS = append(f.DNS, "AAAA "+c.Domain+" → "+ip6)
		}
	}
	changed := false
	for id := range st.Done {
		if before[id] {
			continue
		}
		if id == "infra:apply" {
			return f
		}
		changed = changed || strings.HasPrefix(id, "file:") || strings.HasPrefix(id, "deploy:") || strings.HasPrefix(id, "secret:")
	}
	f.Redeploy = changed
	return f
}

// confirmDestructive is env.Confirm for apply. It always shows the plan,
// then approves with a matching --confirm <project> (confirmed), else asks
// for the project name in a terminal, else refuses. --yes and --force do
// not approve destruction.
func confirmDestructive(ctx context.Context, w io.Writer, prog *ui.Progress, project string, confirmed, tty bool) func(title, details string) (bool, error) {
	return func(title, details string) (bool, error) {
		prog.Pause()
		ui.DestructivePlan(w, title, details)
		if confirmed {
			ui.Warning(w, "--confirm "+project+": going ahead without typed confirmation")
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
	title := "Type the project name " + purpose
	err := ui.RunForm(ctx, func() string { return ui.Answer(title, typed) },
		huh.NewInput().Title(title).Description("project: "+project).Value(&typed),
	)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(typed) == project, nil
}
