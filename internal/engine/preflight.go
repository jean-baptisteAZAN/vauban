package engine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/tools"
)

// PreflightRunner runs the commands Preflight needs (gh auth status). Tests
// replace it with a FakeRunner.
var PreflightRunner Runner = ExecRunner{}

const preflightTimeout = 20 * time.Second

// Preflight checks, before any step runs, everything the selected modules
// need: a valid config, and for deploy a GitHub remote and an authenticated
// gh, for infra tofu, ssh-keygen and working cloud credentials. It returns
// one error listing every problem, or nil. Messages never include secrets.
// With checkQuota (apply may create a server), it also asks the cloud whether
// the account's quota allows one more server of the configured size.
func Preflight(p *detect.Project, c *config.Config, mods []Module, checkQuota bool) error {
	selected := make(map[string]bool, len(mods))
	for _, m := range mods {
		selected[m.ID()] = true
	}
	var errs []error
	if err := c.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", config.FileName, err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), preflightTimeout)
	defer cancel()

	if selected["deploy"] {
		if p.GitHubRepo == "" {
			errs = append(errs, errors.New("deploy needs a GitHub remote (git remote add origin …)"))
		}
		// The command's output is discarded: it may describe the token.
		if _, err := PreflightRunner.Run(ctx, Cmd{Dir: p.Root, Name: "gh", Args: []string{"auth", "status"}}); err != nil {
			errs = append(errs, errors.New("gh is not authenticated (install https://cli.github.com, then run gh auth login)"))
		}
	}

	if selected["infra"] {
		if _, ok := tools.Tofu(); !ok {
			errs = append(errs, errors.New("OpenTofu (tofu) not found (install: https://opentofu.org/docs/intro/install/)"))
		}
		if _, err := exec.LookPath("ssh-keygen"); err != nil {
			errs = append(errs, errors.New("ssh-keygen not found (install OpenSSH)"))
		}
		// An unknown cloud is already reported by Validate.
		if prov, err := cloud.Get(c.Cloud); err == nil {
			if _, err := prov.CredentialsEnv(); err != nil {
				errs = append(errs, err)
			} else if err := prov.CheckAuth(ctx); err != nil {
				errs = append(errs, err)
			} else if checkQuota {
				if q, err := prov.Quota(ctx, c.Region, c.Size); err != nil {
					errs = append(errs, err)
				} else if !q.OK {
					errs = append(errs, errors.New(q.Reason))
				}
			}
		}
	}
	return errors.Join(errs...)
}
