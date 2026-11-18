package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module/infra"
	"github.com/jean-baptisteAZAN/vauban/internal/tools"
	"github.com/jean-baptisteAZAN/vauban/internal/ui"
)

// deploySecrets are the GitHub secrets the deploy module sets.
var deploySecrets = []string{"DEPLOY_HOST", "DEPLOY_SSH_KEY", "DEPLOY_DOMAIN"}

var errDestroyNeedsConfirm = errors.New("destroy needs a terminal or --confirm <project>")

func newDestroyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Destroy the server and delete the deploy secrets",
		Long: "Destroy the server (tofu destroy), delete the deploy secrets and forget\n" +
			"vauban's state. Generated files and the local deploy key stay in place.\n\n" +
			"Always asks for the project name; without a terminal pass --confirm <project>.\n" +
			"--yes and --force are not enough.",
		Args: cobra.NoArgs,
		RunE: abortable(runDestroy),
	}
	cmd.Flags().String("confirm", "", "the project name, to confirm without a terminal")
	return cmd
}

func runDestroy(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
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
	st, err := engine.LoadState(root)
	if err != nil {
		return err
	}
	p, err := detect.Scan(root)
	if err != nil {
		return err
	}
	managed := infra.IsManaged(root)
	tofu, _ := tools.Tofu()
	tofuArgs := []string{"-chdir=infra", "destroy", "-auto-approve", "-input=false"}

	w := cmd.OutOrStdout()
	ui.Heading(w, c.Project)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "destroy will:")
	if managed {
		fmt.Fprintf(w, "  - $ %s %s  (deletes the %s server and firewall)\n", tofu, strings.Join(tofuArgs, " "), c.Cloud)
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
	case interactive():
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
	if managed {
		prov, err := cloud.Get(c.Cloud)
		if err != nil {
			return err
		}
		creds, err := prov.CredentialsEnv()
		if err != nil {
			return err
		}
		const id, desc = "infra:destroy", "destroy cloud resources (tofu destroy)"
		prog.Update(id, desc, engine.PhaseStart, nil)
		_, err = cmdRunner.Run(ctx, engine.Cmd{Dir: root, Name: tofu, Args: tofuArgs, Env: creds})
		if err != nil {
			prog.Update(id, desc, engine.PhaseFail, err)
			return fmt.Errorf("tofu destroy failed — secrets and state left untouched: %w", err)
		}
		prog.Update(id, desc, engine.PhaseDone, nil)
	}

	var failed []string
	if p.GitHubRepo != "" {
		for _, s := range deploySecrets {
			desc := "gh secret delete " + s + " --repo " + p.GitHubRepo
			prog.Update(s, desc, engine.PhaseStart, nil)
			_, err := cmdRunner.Run(ctx, engine.Cmd{Dir: root, Name: "gh", Args: []string{"secret", "delete", s, "--repo", p.GitHubRepo}})
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
	}
	prog.Pause()

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

// secretNotFound reports whether gh failed because the secret does not exist.
func secretNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "404")
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
		key, _ = infra.DeployKeyPath(c.Project)
	}
	for _, k := range []string{key, key + ".pub"} {
		if _, err := os.Stat(k); k != ".pub" && err == nil {
			kept = append(kept, k)
		}
	}
	ui.Warnings(w, "left in place (delete them yourself if you no longer need them):", kept)
}
