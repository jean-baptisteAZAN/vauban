package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module/infra"
	"github.com/jean-baptisteAZAN/vauban/internal/module/server"
	"github.com/jean-baptisteAZAN/vauban/internal/tools"
	"github.com/jean-baptisteAZAN/vauban/internal/ui"
)

// deploySecrets are the GitHub secrets the deploy module sets.
var deploySecrets = []string{"DEPLOY_HOST", "DEPLOY_SSH_KEY", "DEPLOY_DOMAIN"}

var (
	errDestroyNeedsConfirm = errors.New("destroy needs a terminal or --confirm <project>")
	errDestroyInterrupted  = errors.New("interrupted — state kept; rerun `vauban destroy` to finish")
)

func newDestroyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Destroy the server and delete the deploy secrets",
		Long: "Destroy the server (tofu destroy), delete the deploy secrets and forget\n" +
			"vauban's state. Generated files and the local deploy key stay in place.\n\n" +
			"Always asks for the project name; without a terminal pass --confirm <project>.\n" +
			"--yes and --force are not enough.\n\n" +
			"With target: existing, nothing is destroyed but this project: its containers,\n" +
			"Caddy site, ~deploy/apps/<project> and deploy key line are removed from the\n" +
			"server; Docker, Caddy and other projects stay. --purge also deletes the\n" +
			"project's Docker volumes.",
		Args: cobra.NoArgs,
		RunE: abortable(runDestroy),
	}
	cmd.Flags().String("confirm", "", "the project name, to confirm without a terminal")
	cmd.Flags().Bool("purge", false, "existing server: also delete the project's Docker volumes (data is lost)")
	return cmd
}

func runDestroy(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()
	dir, _ := f.GetString("dir")
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	c, err := config.Load(root)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no %s in %s: nothing to destroy", config.FileName, root)
	}
	if err != nil {
		return err
	}
	purge, _ := f.GetBool("purge")
	if purge && !c.Existing() {
		return &ExitError{Code: 2, Err: errors.New("--purge needs target: existing in vauban.yaml")}
	}
	st, err := engine.LoadState(root)
	if err != nil {
		return err
	}
	p, err := detect.Scan(root)
	if err != nil {
		return err
	}
	if c.Existing() {
		return runDestroyExisting(cmd, root, c, st, p, purge)
	}
	if applied := server.AppliedTarget(st); applied != "" {
		return &ExitError{Code: 1, Err: fmt.Errorf("this project is deployed on %s (target: existing) but vauban.yaml says target: cloud: set target: existing and its server: block back in vauban.yaml, then run vauban destroy — nothing was destroyed", applied)}
	}
	managed := infra.IsManaged(root)
	// The cloud the infra was applied on, which vauban.yaml may no longer name.
	cloudID := cmp.Or(infra.AppliedCloud(root, st), c.Cloud)
	tofu, _ := tools.Tofu()
	tofuArgs := []string{"-chdir=infra", "destroy", "-auto-approve", "-input=false"}
	// A fresh checkout has no providers installed: init before destroying.
	var initArgs []string
	if _, err := os.Stat(filepath.Join(root, "infra", ".terraform")); managed && errors.Is(err, fs.ErrNotExist) {
		initArgs = []string{"-chdir=infra", "init", "-input=false"}
	}
	tty := interactive()
	ctx, sig := watchSignals(cmd.Context(), cmd.ErrOrStderr(), tty)
	defer sig.stop()
	run := sig.runner(cmdRunner)

	w := cmd.OutOrStdout()
	creds, problems := destroyPreflight(ctx, root, cloudID, p.GitHubRepo, managed)
	if len(problems) > 0 {
		ui.Warnings(w, "cannot destroy:", problems)
		return &ExitError{Code: 1, Err: errors.New("preflight failed — nothing was destroyed")}
	}
	ui.Heading(w, c.Project)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "destroy will:")
	if managed {
		if initArgs != nil {
			fmt.Fprintf(w, "  - $ %s %s\n", tofu, strings.Join(initArgs, " "))
		}
		fmt.Fprintf(w, "  - $ %s %s  (deletes the %s server and firewall)\n", tofu, strings.Join(tofuArgs, " "), cloudID)
	} else {
		fmt.Fprintln(w, "  - no vauban-managed infra/main.tf: no cloud resources to destroy")
	}
	if p.GitHubRepo != "" {
		for _, s := range deploySecrets {
			fmt.Fprintf(w, "  - $ gh secret delete %s --repo %s\n", s, p.GitHubRepo)
		}
	} else {
		fmt.Fprintln(w, "  - no GitHub remote: deploy secrets left as they are")
	}
	fmt.Fprintln(w, "  - remove .vauban/state.json")
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
	// interrupted maps a failure caused by a signal to exit 130.
	interrupted := func(err error) error {
		switch {
		case sig.forced.Load():
			return &ExitError{Code: 130} // the forced-stop notice is already printed
		case ctx.Err() != nil:
			return &ExitError{Code: 130, Err: errDestroyInterrupted}
		}
		return err
	}
	if managed && initArgs != nil {
		const id, desc = "infra:init", "install providers (tofu init)"
		prog.Update(id, desc, engine.PhaseStart, nil)
		if _, err := run.Run(ctx, engine.Cmd{Dir: root, Name: tofu, Args: initArgs, Env: creds}); err != nil {
			prog.Update(id, desc, engine.PhaseFail, err)
			return interrupted(fmt.Errorf("tofu init failed — nothing was destroyed: %w", err))
		}
		prog.Update(id, desc, engine.PhaseDone, nil)
	}
	if managed {
		const id, desc = "infra:destroy", "destroy cloud resources (tofu destroy)"
		prog.Update(id, desc, engine.PhaseStart, nil)
		_, err = run.Run(ctx, engine.Cmd{Dir: root, Name: tofu, Args: tofuArgs, Env: creds})
		if err != nil {
			prog.Update(id, desc, engine.PhaseFail, err)
			return interrupted(fmt.Errorf("tofu destroy failed — secrets and state left untouched: %w", err))
		}
		prog.Update(id, desc, engine.PhaseDone, nil)
	}

	var failed []string
	if p.GitHubRepo != "" {
		var cut bool
		if failed, cut = deleteSecrets(ctx, prog, run, root, p.GitHubRepo, deploySecrets); cut {
			return interrupted(ctx.Err())
		}
	}
	prog.Pause()
	if ctx.Err() != nil { // a delete was cut short: keep state so a rerun finishes
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

// deleteSecrets deletes the named secrets of repo and returns the failures
// ("NAME: error"). A secret that is not set counts as deleted. cut is true
// when ctx ended before every secret was handled.
func deleteSecrets(ctx context.Context, prog *ui.Progress, run engine.Runner, root, repo string, names []string) (failed []string, cut bool) {
	for _, s := range names {
		if ctx.Err() != nil {
			return failed, true
		}
		desc := "gh secret delete " + s + " --repo " + repo
		prog.Update(s, desc, engine.PhaseStart, nil)
		_, err := run.Run(ctx, engine.Cmd{Dir: root, Name: "gh", Args: []string{"secret", "delete", s, "--repo", repo}})
		switch {
		case err == nil:
			prog.Update(s, desc, engine.PhaseDone, nil)
		case secretNotFound(err):
			prog.Update(s, desc+" (not set)", engine.PhaseSkip, nil)
		default:
			prog.Update(s, desc, engine.PhaseFail, err)
			failed = append(failed, s+": "+err.Error())
		}
	}
	return failed, false
}

// secretNotFound reports whether gh failed because the secret does not
// exist: gh secret delete then prints "HTTP 404". Anything else (no gh, no
// repository, no access) is a real failure.
func secretNotFound(err error) bool {
	return strings.Contains(err.Error(), "HTTP 404")
}

// destroyPreflight checks, before confirmation, what destroy needs: gh on
// PATH, authenticated and able to access the repo (when there are secrets to
// delete), and for managed
// infra tofu and the credentials of cloudID, which it returns.
func destroyPreflight(ctx context.Context, root, cloudID, repo string, managed bool) (creds, problems []string) {
	if repo != "" {
		if _, err := exec.LookPath("gh"); err != nil {
			problems = append(problems, "gh not found (install https://cli.github.com, then run gh auth login)")
		} else if _, err := engine.PreflightRunner.Run(ctx, engine.Cmd{Dir: root, Name: "gh", Args: []string{"auth", "status"}}); err != nil {
			// The command's output is discarded: it may describe the token.
			problems = append(problems, "gh is not authenticated (run gh auth login)")
		} else if _, err := engine.PreflightRunner.Run(ctx, engine.Cmd{Dir: root, Name: "gh", Args: []string{"api", "repos/" + repo, "--silent"}}); err != nil {
			// GitHub answers 404 for a repo the token cannot see, as for a
			// missing secret: only with access is a later 404 "not set".
			problems = append(problems, "cannot access GitHub repo "+repo+" with the current gh login")
		}
	}
	if managed {
		if _, ok := tools.Tofu(); !ok {
			problems = append(problems, "OpenTofu (tofu) not found (install: https://opentofu.org/docs/intro/install/)")
		}
		prov, err := cloud.Get(cloudID)
		if err == nil {
			creds, err = prov.CredentialsEnv()
		}
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	return creds, problems
}

// writeLeftInPlace lists what destroy keeps: generated files, the local
// Terraform state and the deploy key.
func writeLeftInPlace(w io.Writer, root string, c *config.Config, st *engine.State) {
	files := []string{config.FileName}
	for id := range st.Done {
		switch {
		case strings.HasPrefix(id, "file:"):
			files = append(files, strings.TrimPrefix(id, "file:"))
		case id == "infra:main-tf":
			files = append(files, "infra/main.tf")
		case id == "infra:cloud-init":
			files = append(files, "infra/cloud-init.yaml")
		}
	}
	files = append(files, "infra/terraform.tfstate", "infra/terraform.tfstate.backup")
	var kept []string
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			kept = append(kept, f)
		}
	}
	slices.Sort(kept)
	kept = slices.Compact(kept)
	key := st.Outputs["deploy_key_path"]
	if key == "" {
		if path, err := infra.DeployKeyPath(c.Project); err == nil {
			key = path
		}
	}
	if key != "" {
		for _, k := range []string{key, key + ".pub"} {
			if _, err := os.Stat(k); err == nil {
				kept = append(kept, k)
			}
		}
	}
	ui.Warnings(w, "left in place (delete them yourself if you no longer need them):", kept)
}
