package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/tools"
)

// PreflightRunner runs the commands Preflight needs (gh auth status). Tests
// replace it with a FakeRunner.
var PreflightRunner Runner = ExecRunner{}

const (
	preflightTimeout = 20 * time.Second
	quotaTimeout     = 10 * time.Second
)

// QuotaMode says how Preflight treats the cloud quota.
type QuotaMode int

const (
	QuotaSkip  QuotaMode = iota // no server will be created: not checked
	QuotaBlock                  // a new server: an exhausted quota fails preflight
	QuotaWarn                   // resuming an interrupted first apply: only warn
)

// PreflightOptions tunes the checks that depend on the infra state.
type PreflightOptions struct {
	Quota     QuotaMode
	Replacing bool // a server is applied: the change resizes or replaces it
}

// Notices are preflight findings that never block apply: Warnings (e.g. a
// quota that could not be checked) and Notes (e.g. a cloud with no quota API).
type Notices struct{ Warnings, Notes []string }

// Preflight checks, before any step runs, everything the selected modules
// need: a valid config, and for deploy a GitHub remote and an authenticated
// gh, for infra tofu, ssh-keygen and working cloud credentials. It returns
// one error listing every problem, or nil. Messages never include secrets.
// Unless opts.Quota is QuotaSkip, it also asks the cloud whether the account
// quota allows the server: only an exhausted quota in QuotaBlock mode is an
// error; a quota that could not be checked is a warning, never a block.
func Preflight(p *detect.Project, c *config.Config, mods []Module, opts PreflightOptions) (Notices, error) {
	var notices Notices
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

	if selected["server"] && c.Existing() {
		errs = append(errs, checkSSH(c)...)
	}

	if selected["infra"] && !c.Existing() {
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
			} else if opts.Quota != QuotaSkip {
				if err := checkQuota(prov, c, opts, &notices); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return notices, errors.Join(errs...)
}

// checkSSH checks what reaching an existing server needs: ssh and
// ssh-keygen on PATH, and the key file readable (or an ssh-agent socket).
func checkSSH(c *config.Config) []error {
	var errs []error
	for _, bin := range []string{"ssh", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			errs = append(errs, fmt.Errorf("%s not found (install OpenSSH)", bin))
		}
	}
	if c.Server == nil {
		return errs // Validate reports it
	}
	key, err := c.Server.KeyPath()
	switch {
	case err != nil:
		errs = append(errs, err)
	case key == "":
		if os.Getenv("SSH_AUTH_SOCK") == "" {
			errs = append(errs, errors.New("server.key is agent but no ssh-agent is running (SSH_AUTH_SOCK is not set)"))
		}
	default:
		f, err := os.Open(key)
		if err != nil {
			errs = append(errs, fmt.Errorf("ssh key %s is not readable: %w", key, errors.Unwrap(err)))
		} else {
			_ = f.Close()
		}
	}
	return errs
}

// checkQuota asks prov for the quota of c's region and size, with its own
// timeout. It returns an error only for an exhausted quota in QuotaBlock
// mode; everything else lands in notices.
func checkQuota(prov cloud.Provider, c *config.Config, opts PreflightOptions, notices *Notices) error {
	ctx, cancel := context.WithTimeout(context.Background(), quotaTimeout)
	defer cancel()
	q, err := prov.Quota(ctx, c.Region, c.Size, opts.Replacing)
	switch {
	case err != nil:
		notices.Warnings = append(notices.Warnings, "quota not checked: "+err.Error())
	case !q.OK && opts.Quota == QuotaBlock:
		return errors.New(q.Reason)
	case !q.OK:
		notices.Warnings = append(notices.Warnings, q.Reason+" (not blocking: resuming an interrupted first apply, which may already have created resources)")
	case q.Reason != "":
		if why, ok := strings.CutPrefix(q.Reason, "not checked: "); ok {
			notices.Notes = append(notices.Notes, prov.ID()+" quota not checked ("+why+")")
		} else {
			notices.Notes = append(notices.Notes, prov.ID()+" quota: "+q.Reason)
		}
	}
	return nil
}
