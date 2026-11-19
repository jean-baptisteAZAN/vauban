// Package server prepares an existing machine (target: existing) over SSH:
// audit, Docker, the deploy user and its key. It replaces infra in that mode
// and never acts without passwordless sudo, nor when nothing is missing.
package server

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module/infra"
	"github.com/jean-baptisteAZAN/vauban/internal/remote"
)

//go:embed scripts/docker.sh
var dockerScript string

//go:embed scripts/deploy-user.sh
var deployUserScript string

//go:embed scripts/authorize-key.sh
var authorizeScript string

//go:embed scripts/network.sh
var networkScript string

//go:embed scripts/caddy-install.sh
var caddyInstallScript string

//go:embed scripts/project.sh
var projectScript string

//go:embed scripts/site.sh
var siteScript string

//go:embed scripts/destroy.sh
var destroyScript string

// DestroyScript is the script that removes one project from an existing
// server (see scripts/destroy.sh for its arguments and exit codes).
func DestroyScript() string { return destroyScript }

// Module implements engine.Module for an existing server.
type Module struct{}

// New returns the server module.
func New() *Module { return &Module{} }

func (*Module) ID() string         { return "server" }
func (*Module) Title() string      { return "server" }
func (*Module) Requires() []string { return nil }

// Detect reports present: nothing of this module lives in the repository (the
// server is checked by vauban audit), and "missing" would make vauban doctor
// show a failing server for cloud projects that never use this module.
func (*Module) Detect(*detect.Project) engine.Status { return engine.StatusPresent }

// Plan returns nothing for a cloud target.
func (*Module) Plan(p *detect.Project, c *config.Config) ([]engine.Step, error) {
	if !c.Existing() {
		return nil, nil
	}
	if c.Server == nil {
		return nil, errors.New("target existing needs a server: block")
	}
	t, err := remote.TargetFor(p.Root, c.Server)
	if err != nil {
		return nil, err
	}
	key, err := infra.NewDeployKey(c.Project, "server:deploy-key")
	if err != nil {
		return nil, err
	}
	return []engine.Step{
		auditStep{t: t, project: c.Project, repo: p.GitHubRepo},
		dockerStep{t: t},
		deployUserStep{t: t},
		key,
		authorizeStep{t: t, project: c.Project},
		networkStep{t: t},
		caddyStep{t: t},
		projectStep{t: t, project: c.Project, repo: p.GitHubRepo},
		siteStep{t: t, project: c.Project, repo: p.GitHubRepo, domain: c.Domain, port: c.AppPort},
	}, nil
}

// CaddyModeName is the stable name of a Caddy mode in the server_caddy_mode output.
func CaddyModeName(m remote.CaddyMode) string {
	switch m {
	case remote.CaddyInstall:
		return "install"
	case remote.CaddyVaubanReady:
		return "vauban-ready"
	case remote.CaddyUserReady:
		return "user-ready"
	case remote.CaddyUserNeedsSetup:
		return "user-needs-setup"
	case remote.CaddyBlocked:
		return "blocked"
	}
	return "unknown"
}

// auditStep runs the read-only audit; blockers stop the apply.
type auditStep struct {
	t             remote.Target
	project, repo string
}

func (auditStep) ID() string { return "server:audit" }
func (a auditStep) Describe() string {
	return "audit " + a.t.User + "@" + a.t.Host + " over ssh (read-only)"
}

func (a auditStep) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	au, err := remote.RunAudit(ctx, env.Run, a.t, a.project, a.repo)
	if err != nil {
		return nil, err
	}
	if b := au.Blockers(); len(b) > 0 {
		return nil, fmt.Errorf("server audit found blockers:\n- %s", strings.Join(b, "\n- "))
	}
	if au.Caddy == remote.CaddyUserNeedsSetup {
		// Stop before any mutating step runs.
		if g := strings.TrimSpace(remote.CaddyGuide(au)); g != "" {
			return nil, errors.New(g)
		}
		return nil, errors.New("your Caddy is not set up for vauban: run vauban audit for the one-time change to make")
	}
	f := au.Facts
	return engine.Outputs{
		"server_ip":                  a.t.Host,
		"server_port":                strconv.Itoa(a.t.Port),
		"server_user":                a.t.User,
		"server_target":              TargetString(a.t.User, a.t.Host, a.t.Port),
		"server_caddy_mode":          CaddyModeName(au.Caddy),
		"server_caddy_guide":         remote.CaddyGuide(au),
		"server_sites_dir":           au.SitesDir,
		"server_caddy_container":     au.CaddyContainer,
		"server_sudo":                strconv.FormatBool(f.Sudo),
		"server_docker":              strconv.FormatBool(f.Docker),
		"server_compose":             strconv.FormatBool(f.Compose),
		"server_deploy_user":         strconv.FormatBool(f.DeployUser),
		"server_docker_group":        strconv.FormatBool(f.DockerGroup),
		"server_deploy_docker_group": strconv.FormatBool(f.DeployDockerGroup),
	}, nil
}

// needSudo is the error for an install that cannot run without sudo.
func needSudo(what string) error {
	return fmt.Errorf("%s is needed but the ssh user has no passwordless sudo: install it by hand, or give the user NOPASSWD sudo", what)
}

type dockerStep struct{ t remote.Target }

func (dockerStep) ID() string { return "server:docker" }
func (d dockerStep) Describe() string {
	return "install Docker Engine + Compose plugin on " + d.t.Host + " (sudo)"
}

func (d dockerStep) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	if env.In["server_docker"] == "true" && env.In["server_compose"] == "true" {
		return nil, nil
	}
	if env.In["server_sudo"] != "true" {
		return nil, needSudo("Docker Engine and the Compose plugin")
	}
	// Docker already present: compose-only mode, which never touches the engine.
	mode := "full"
	if env.In["server_docker"] == "true" {
		mode = "compose"
	}
	_, err := remote.Exec(ctx, env.Run, d.t, dockerScript, mode)
	return nil, err
}

type deployUserStep struct{ t remote.Target }

func (deployUserStep) ID() string { return "server:deploy-user" }
func (d deployUserStep) Describe() string {
	return "create user deploy (docker group) on " + d.t.Host + " (sudo)"
}

func (d deployUserStep) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	if env.In["server_deploy_user"] == "true" && env.In["server_deploy_docker_group"] == "true" {
		return nil, nil
	}
	if env.In["server_sudo"] != "true" {
		return nil, needSudo("the deploy user (in the docker group)")
	}
	_, err := remote.Exec(ctx, env.Run, d.t, deployUserScript)
	return nil, err
}

type authorizeStep struct {
	t       remote.Target
	project string
}

func (authorizeStep) ID() string { return "server:authorize-key" }
func (a authorizeStep) Describe() string {
	return "authorize the deploy key for user deploy on " + a.t.Host
}

func (a authorizeStep) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	pub := strings.TrimSpace(env.In["deploy_pubkey"])
	if pub == "" {
		path, err := infra.DeployKeyPath(a.project)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(path + ".pub")
		if err != nil {
			return nil, fmt.Errorf("read deploy public key: %w", err)
		}
		pub = strings.TrimSpace(string(b))
	}
	if err := checkPubkey(pub); err != nil {
		return nil, err
	}
	if a.t.User != "deploy" && env.In["server_sudo"] != "true" {
		return nil, needSudo("the deploy key in ~deploy/.ssh/authorized_keys")
	}
	_, err := remote.Exec(ctx, env.Run, a.t, authorizeScript, pub, a.project)
	return nil, err
}

var keyPrefixes = []string{"ssh-ed25519 ", "ssh-rsa ", "ecdsa-", "sk-"}

// checkPubkey accepts exactly one public key line.
func checkPubkey(pub string) error {
	if pub == "" || strings.ContainsAny(pub, "\r\n") {
		return errors.New("deploy public key is empty or spans several lines")
	}
	for _, p := range keyPrefixes {
		if strings.HasPrefix(pub, p) {
			return nil
		}
	}
	return errors.New("deploy public key is not an ssh public key (ssh-ed25519, ssh-rsa, ecdsa-*, sk-*)")
}
