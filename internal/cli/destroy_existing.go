package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module/server"
	"github.com/jean-baptisteAZAN/vauban/internal/remote"
	"github.com/jean-baptisteAZAN/vauban/internal/ui"
)

// existingDeploySecrets are the GitHub secrets an existing-server apply sets.
var existingDeploySecrets = []string{"DEPLOY_HOST", "DEPLOY_SSH_KEY", "DEPLOY_DOMAIN", "DEPLOY_KNOWN_HOSTS", "DEPLOY_PORT"}

// Markers the destroy script prints on stdout.
const (
	markAbsent   = "VAUBAN_PROJECT_ABSENT"
	markConflict = "VAUBAN_OWNER_CONFLICT"
	markForeign  = "VAUBAN_SITE_FOREIGN"
	markVolume   = "VAUBAN_VOLUME "
	markUnknown  = "VAUBAN_VOLUMES_UNKNOWN"
)

// cleanLine drops control characters from server-provided text.
func cleanLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// hasMarker reports whether out has a line that is exactly marker.
func hasMarker(out []byte, marker string) bool {
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == marker {
			return true
		}
	}
	return false
}

// ownerConflict returns the other owner a script reported ("-" for none).
func ownerConflict(out []byte) (string, bool) {
	for _, l := range strings.Split(string(out), "\n") {
		if other, ok := strings.CutPrefix(strings.TrimSpace(l), markConflict+" "); ok {
			return cleanLine(strings.TrimSpace(other)), true
		}
	}
	return "", false
}

// runDestroyExisting destroys one project on an existing server: the project's
// containers, site, directory and authorized key on the server, then the
// GitHub secrets and vauban's state. Docker, Caddy, the network, the deploy
// user and other projects are never touched.
func runDestroyExisting(cmd *cobra.Command, root string, c *config.Config, st *engine.State, p *detect.Project, purge bool) error {
	f := cmd.Flags()
	if c.Server == nil {
		return errors.New("target existing needs a server: block")
	}
	w := cmd.OutOrStdout()
	// The server the project is deployed on (host and port), which vauban.yaml
	// may no longer name (edited by hand); the ssh user and key come from
	// vauban.yaml, since they may have changed (root ssh turned off, say).
	srv := *c.Server
	if rec, applied, ok := server.AppliedServer(st); ok {
		if want := server.HostPort(srv.Host, srv.SSHPort()); want != applied {
			ui.Warning(w, fmt.Sprintf("vauban.yaml names %s but this project is deployed on %s: destroying it there", want, applied))
		}
		srv.Host, srv.Port = rec.Host, rec.Port
	}
	t, err := remote.TargetFor(root, &srv)
	if err != nil {
		return err
	}
	tty := interactive()
	ctx, sig := watchSignals(cmd.Context(), cmd.ErrOrStderr(), tty)
	defer sig.stop()
	run := sig.runner(cmdRunner)

	_, problems := destroyPreflight(ctx, root, "", p.GitHubRepo, false)
	if p.GitHubRepo == "" {
		problems = append(problems, "no GitHub remote: the project's owner marker on the server cannot be checked")
	}
	if len(problems) > 0 {
		ui.Warnings(w, "cannot destroy:", problems)
		return &ExitError{Code: 1, Err: errors.New("preflight failed — nothing was destroyed")}
	}
	repo := p.GitHubRepo
	sites, container := st.Outputs["server_sites_dir"], st.Outputs["server_caddy_container"]
	absent := false
	if sites == "" || container == "" {
		// Never applied (or Caddy never ready): ask the server.
		a, err := remote.RunAudit(ctx, run, t, c.Project, repo)
		if err != nil {
			return fmt.Errorf("cannot reach %s@%s: %w", t.User, t.Host, err)
		}
		sites, container = a.SitesDir, a.CaddyContainer
		absent = a.Facts.ProjectOwner == ""
	}
	if sites == "" || container == "" {
		sites, container = "-", "-"
	}
	mode := "sudo"
	if t.User == "deploy" {
		mode = "direct"
	}
	purgeArg := "no"
	if purge {
		purgeArg = "yes"
	}
	script := func(action string) []string {
		return []string{action, mode, sites, c.Project, repo, container, purgeArg}
	}

	// Read-only: prove the project is ours and list what --purge would delete.
	var volumes []string
	var volumesKnown bool
	{
		out, err := remote.Exec(ctx, run, t, server.DestroyScript(), script("plan")...)
		if other, ok := ownerConflict(out); ok && err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("%w: refusing to destroy it — nothing was destroyed", conflictErr(other, c.Project, t.Host, repo))}
		}
		if err != nil {
			if ctx.Err() != nil {
				return &ExitError{Code: 130, Err: errDestroyInterrupted}
			}
			if strings.Contains(err.Error(), "cannot act as deploy") {
				return &ExitError{Code: 1, Err: fmt.Errorf("cannot act as the deploy user on %s: ssh as deploy, or give the ssh user passwordless sudo (sudo -n runuser failed) — nothing was destroyed", t.Host)}
			}
			return &ExitError{Code: 1, Err: fmt.Errorf("cannot check %s@%s — nothing was destroyed: %w", t.User, t.Host, err)}
		}
		absent = absent || hasMarker(out, markAbsent)
		volumesKnown = !hasMarker(out, markUnknown)
		for _, l := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), markVolume); ok {
				volumes = append(volumes, cleanLine(strings.TrimSpace(v)))
			}
		}
	}

	ui.Heading(w, c.Project)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "destroy will:")
	if absent {
		fmt.Fprintf(w, "  - nothing on the server to remove but a leftover \"# vauban:%s\" line in ~deploy/.ssh/authorized_keys: ~deploy/apps/%s does not exist on %s\n", c.Project, c.Project, t.Host)
	} else {
		down := "down"
		if purge {
			down = "down -v"
		}
		fmt.Fprintf(w, "  - on %s@%s: $ docker compose -p vauban-%s -f ~deploy/apps/%s/compose.yaml %s\n", t.User, t.Host, c.Project, c.Project, down)
		if sites != "-" {
			fmt.Fprintf(w, "  - remove %s/%s.caddy if vauban wrote it, then validate and reload %s (a failed validate restores it)\n", sites, c.Project, container)
		} else {
			ui.Warning(w, "the Caddy sites dir is unknown: a site file for "+c.Project+" may remain on the server")
		}
		fmt.Fprintf(w, "  - remove ~deploy/apps/%s (only because its .vauban-owner is %s)\n", c.Project, repo)
		fmt.Fprintf(w, "  - remove the \"# vauban:%s\" line from ~deploy/.ssh/authorized_keys (last: it revokes the deploy key)\n", c.Project)
	}
	for _, s := range existingDeploySecrets {
		fmt.Fprintf(w, "  - $ gh secret delete %s --repo %s\n", s, repo)
	}
	fmt.Fprintln(w, "  - remove .vauban/state.json")
	fmt.Fprintln(w, "  - keep .vauban/known_hosts")
	if purge && !absent {
		switch {
		case !volumesKnown:
			fmt.Fprintf(w, "  - --purge also deletes the project's Docker volumes (vauban-%s_*; could not list them): their data is lost\n", c.Project)
		case len(volumes) == 0:
			fmt.Fprintln(w, "  - --purge: the project declares no Docker volumes")
		default:
			fmt.Fprintf(w, "  - --purge also deletes these Docker volumes, their data is lost: %s\n", strings.Join(volumes, ", "))
		}
	} else if !absent {
		fmt.Fprintln(w, "  - keep the project's Docker volumes (--purge deletes them)")
	}
	fmt.Fprintln(w)

	var typed string
	switch {
	case f.Changed("confirm"):
		typed, _ = f.GetString("confirm")
	case tty:
		ok, err := typedProject(ctx, c.Project, "to destroy it")
		if err != nil {
			return err
		}
		if ok {
			typed = c.Project
		}
	default:
		return &ExitError{Code: 2, Err: errDestroyNeedsConfirm}
	}
	if typed != c.Project {
		return &ExitError{Code: 2, Err: fmt.Errorf("project name does not match %q — nothing was destroyed", c.Project)}
	}

	prog := ui.NewProgress(w)
	sig.beforeExit(prog.Abort)
	defer prog.Pause()
	interrupted := func(err error) error {
		switch {
		case sig.forced.Load():
			return &ExitError{Code: 130}
		case ctx.Err() != nil:
			return &ExitError{Code: 130, Err: errDestroyInterrupted}
		}
		return err
	}
	{
		const id, desc = "server:destroy", "remove the project from the server (ssh)"
		prog.Update(id, desc, engine.PhaseStart, nil)
		out, err := remote.Exec(ctx, run, t, server.DestroyScript(), script("run")...)
		if err != nil {
			prog.Update(id, desc, engine.PhaseFail, err)
			return interrupted(runFailure(out, err, c.Project, repo, t.Host))
		}
		prog.Update(id, desc, engine.PhaseDone, nil)
		prog.Pause()
		if hasMarker(out, markAbsent) {
			fmt.Fprintf(w, "nothing on the server to remove: ~deploy/apps/%s does not exist\n", c.Project)
		}
		if sites == "-" {
			ui.Warning(w, "the Caddy sites dir was unknown: a site file for "+c.Project+" may remain on the server")
		}
		if hasMarker(out, markForeign) {
			ui.Warning(w, fmt.Sprintf("%s/%s.caddy was not written by vauban: left in place", sites, c.Project))
		}
	}

	failed, cut := deleteSecrets(ctx, prog, run, root, repo, existingDeploySecrets)
	prog.Pause()
	if cut || ctx.Err() != nil {
		return interrupted(ctx.Err())
	}
	if err := os.Remove(filepath.Join(root, ".vauban", "state.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	fmt.Fprintln(w, "removed .vauban/state.json")
	writeLeftInPlace(w, root, c, st)
	if len(failed) > 0 {
		return &ExitError{Code: 1, Err: fmt.Errorf("could not delete secrets: %s", strings.Join(failed, "; "))}
	}
	return nil
}

// conflictErr says why a project dir is not ours ("-" means no marker).
func conflictErr(other, project, host, repo string) error {
	if other == "-" {
		return fmt.Errorf("~deploy/apps/%s on %s has no .vauban-owner marker, so it is not %s's", project, host, repo)
	}
	return fmt.Errorf("project %q on %s belongs to %s, not %s", project, host, other, repo)
}

// runFailure explains a failed remove run: nothing local was changed, and a
// rerun finishes what is left.
func runFailure(out []byte, err error, project, repo, host string) error {
	if other, ok := ownerConflict(out); ok {
		return &ExitError{Code: 1, Err: fmt.Errorf("%w: nothing was destroyed", conflictErr(other, project, host, repo))}
	}
	if strings.Contains(err.Error(), "cannot act as deploy") {
		return &ExitError{Code: 1, Err: fmt.Errorf("cannot act as the deploy user on %s: ssh as deploy, or give the ssh user passwordless sudo (sudo -n runuser failed)", host)}
	}
	return &ExitError{Code: 1, Err: fmt.Errorf("removing the project from %s failed — secrets and state left untouched, rerun `vauban destroy` to finish: %w", host, err)}
}
