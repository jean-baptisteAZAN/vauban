package server

import (
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/remote"
)

// ProjectsDir is the directory under ~deploy that holds one dir per project.
const ProjectsDir = "apps"

// caddyfile is the whole vauban-managed Caddyfile: every site is a file.
const caddyfile = "import /etc/caddy/sites/*.caddy\n"

const ownerConflict = "VAUBAN_OWNER_CONFLICT "

//go:embed templates/caddy-compose.yaml
var composeTemplate string

// RenderCompose is the compose file of the vauban-managed Caddy.
func RenderCompose() string { return composeTemplate }

// RenderSite is the Caddy site file of one project.
func RenderSite(project, domain string, port int) string {
	return fmt.Sprintf("%s {\n\treverse_proxy %s-app:%d\n}\n", domain, project, port)
}

var (
	domainRe    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)
	containerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	projectRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
)

// actAs picks how a step runs docker or writes as deploy: "direct" when the
// ssh user is deploy, "sudo" (runuser through sudo -n) when it has sudo, else
// "direct" only when needDockerOnly and the ssh user is in the docker group.
func actAs(t remote.Target, env *engine.Env, dockerOnly bool, what string) (string, error) {
	switch {
	case t.User == "deploy":
		return "direct", nil
	case env.In["server_sudo"] == "true":
		return "sudo", nil
	case dockerOnly && env.In["server_docker_group"] == "true":
		return "direct", nil
	}
	if dockerOnly {
		return "", fmt.Errorf("%s needs docker access: ssh as deploy, use a user in the docker group, or give the user passwordless sudo", what)
	}
	return "", fmt.Errorf("%s needs to act as the deploy user: ssh as deploy, or give the ssh user passwordless sudo", what)
}

// remoteErr adds a conflict explanation when the script reported an owner clash.
func remoteErr(out []byte, err error, project, repo string) error {
	if err == nil {
		return nil
	}
	for _, l := range strings.Split(string(out), "\n") {
		if other, ok := strings.CutPrefix(strings.TrimSpace(l), strings.TrimSpace(ownerConflict)); ok {
			other = strings.TrimSpace(other)
			if other == "-" {
				return fmt.Errorf("~deploy/%s/%s exists without a .vauban-owner marker, so it is not ours (%s): pick another project name or remove it by hand", ProjectsDir, project, repo)
			}
			return fmt.Errorf("project %q on this server belongs to %s, not %s: pick another project name", project, other, repo)
		}
	}
	return err
}

// trimLines keeps the last n lines of an error message.
func trimLines(err error, n int) error {
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	if len(lines) <= n {
		return err
	}
	return errors.New(strings.Join(lines[len(lines)-n:], "\n"))
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

type networkStep struct{ t remote.Target }

func (networkStep) ID() string { return "server:network" }
func (n networkStep) Describe() string {
	return "create the Docker network vauban on " + n.t.Host + " if missing"
}

func (n networkStep) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	mode, err := actAs(n.t, env, true, "the Docker network vauban")
	if err != nil {
		return nil, err
	}
	_, err = remote.Exec(ctx, env.Run, n.t, networkScript, mode)
	return nil, err
}

type caddyStep struct{ t remote.Target }

func (caddyStep) ID() string { return "server:caddy" }
func (c caddyStep) Describe() string {
	return "set up the shared Caddy on " + c.t.Host + " (installs vauban-caddy only when none exists; sudo)"
}

func (c caddyStep) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	switch env.In["server_caddy_mode"] {
	case CaddyModeName(remote.CaddyVaubanReady), CaddyModeName(remote.CaddyUserReady):
		return nil, nil
	case CaddyModeName(remote.CaddyUserNeedsSetup):
		if g := strings.TrimSpace(env.In["server_caddy_guide"]); g != "" {
			return nil, errors.New(g)
		}
		return nil, errors.New("your Caddy is not set up for vauban: run vauban audit for the one-time change to make")
	case CaddyModeName(remote.CaddyInstall):
		if env.In["server_sudo"] != "true" {
			return nil, needSudo("the vauban-managed Caddy")
		}
		_, err := remote.Exec(ctx, env.Run, c.t, caddyInstallScript, b64(RenderCompose()), b64(caddyfile))
		return nil, err
	}
	return nil, fmt.Errorf("caddy cannot be set up (mode %q): see vauban audit", env.In["server_caddy_mode"])
}

type projectStep struct {
	t             remote.Target
	project, repo string
}

func (projectStep) ID() string { return "server:project" }
func (p projectStep) Describe() string {
	return "create ~deploy/" + ProjectsDir + "/" + p.project + " with an owner marker on " + p.t.Host
}

func (p projectStep) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	if !projectRe.MatchString(p.project) {
		return nil, fmt.Errorf("invalid project name %q", p.project)
	}
	mode, err := actAs(p.t, env, false, "the project directory")
	if err != nil {
		return nil, err
	}
	out, err := remote.Exec(ctx, env.Run, p.t, projectScript, mode, p.project, p.repo)
	return nil, remoteErr(out, err, p.project, p.repo)
}

type siteStep struct {
	t             remote.Target
	project, repo string
	domain        string
	port          int
}

func (siteStep) ID() string { return "server:site" }
func (s siteStep) Describe() string {
	return "write the Caddy site for " + s.domain + " on " + s.t.Host + " and reload Caddy"
}

func (s siteStep) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	sites := env.In["server_sites_dir"]
	container := env.In["server_caddy_container"]
	switch {
	case env.In["server_caddy_mode"] == CaddyModeName(remote.CaddyUserNeedsSetup) || env.In["server_caddy_mode"] == CaddyModeName(remote.CaddyBlocked):
		return nil, errors.New("the Caddy is not ready for vauban: run vauban audit")
	case !strings.HasPrefix(sites, "/") || strings.ContainsAny(sites, "\r\n"):
		return nil, fmt.Errorf("invalid Caddy sites dir %q: want one absolute path", sites)
	case !containerRe.MatchString(container):
		return nil, fmt.Errorf("invalid Caddy container name %q", container)
	case !domainRe.MatchString(s.domain):
		return nil, fmt.Errorf("invalid domain %q", s.domain)
	case !projectRe.MatchString(s.project):
		return nil, fmt.Errorf("invalid project name %q", s.project)
	case s.port < 1 || s.port > 65535:
		return nil, fmt.Errorf("invalid app port %d", s.port)
	}
	mode, err := actAs(s.t, env, false, "the Caddy site file")
	if err != nil {
		return nil, err
	}
	run := func(action string, extra ...string) ([]byte, error) {
		args := append([]string{action, mode, sites, s.project, s.repo, container}, extra...)
		return remote.Exec(ctx, env.Run, s.t, siteScript, args...)
	}
	if out, err := run("write", b64(RenderSite(s.project, s.domain, s.port))); err != nil {
		return nil, remoteErr(out, err, s.project, s.repo)
	}
	if _, err := run("validate"); err != nil {
		// Never leave a site file Caddy rejects; nothing is reloaded.
		_, rmErr := run("remove")
		err = fmt.Errorf("caddy rejected the site for %s, removed it, nothing reloaded:\n%w", s.domain, trimLines(err, 20))
		if rmErr != nil {
			err = fmt.Errorf("%w\n(removing the site file also failed: %w)", err, rmErr)
		}
		return nil, err
	}
	if _, err := run("reload"); err != nil {
		return nil, fmt.Errorf("caddy reload failed: %w", trimLines(err, 20))
	}
	return nil, nil
}
