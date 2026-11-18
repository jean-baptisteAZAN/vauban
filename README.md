<div align="center">

# vauban

**Point it at a repo, answer three questions, push. Set up once, never think about it again.**

CI, a container image, a hardened VM with automatic HTTPS and the GitHub deploy wiring, from one command.

[![CI](https://img.shields.io/github/actions/workflow/status/jean-baptisteAZAN/vauban/ci.yml?branch=main&label=ci)](https://github.com/jean-baptisteAZAN/vauban/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jean-baptisteAZAN/vauban?sort=semver)](https://github.com/jean-baptisteAZAN/vauban/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/jean-baptisteAZAN/vauban)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/jean-baptisteAZAN/vauban)](https://goreportcard.com/report/github.com/jean-baptisteAZAN/vauban)

<img src="docs/demo.gif" alt="vauban doctor, plan and apply in a terminal" width="800">

<sub>Demo runs against fake tofu/gh — no real VM created (cloud API calls are stubbed too; see <a href="docs/demo/setup.sh">docs/demo</a>).</sub>

</div>

vauban is a fast, single-binary Go CLI. It looks at your repository (Go, Node/TypeScript or Python), shows what is missing to ship it, and in one `vauban apply` sets up a Dockerfile, GitHub Actions CI, an OpenTofu-managed Debian 13 VM on Scaleway, DigitalOcean or Hetzner, Caddy with automatic HTTPS, a GHCR + SSH deploy workflow and the GitHub secrets that tie it all together. Then every push to `main` ships.

## Contents

- [Why vauban](#why-vauban)
- [Features](#features)
- [Install](#install)
- [Quickstart](#quickstart)
- [How it works](#how-it-works)
- [What gets created](#what-gets-created)
- [Supported clouds and stacks](#supported-clouds-and-stacks)
- [Security defaults](#security-defaults)
- [Safety and exit codes](#safety-and-exit-codes)
- [Destroy](#destroy)
- [Known limitations](#known-limitations)
- [Performance](#performance)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)

## Why vauban

Shipping a small app should be a one-time chore, not a hobby.

- **Delegate to the official tools.** vauban does not carry a pile of hand-written templates or its own cloud client. It drives `docker`, `tofu`, `gh`, `ssh-keygen` and the cloud providers' official Terraform providers, and your infrastructure stays as reviewable OpenTofu code in your repo.
- **Wire them together.** Its own job is the glue: Terraform outputs into CI, SSH keys where each side needs them, secrets into GitHub.
- **Safe by default.** Nothing is written or run without appearing in `vauban plan` first, existing files are never overwritten, destroying anything needs the project name typed, and every default favours a setup that runs unattended: automatic TLS, automatic security updates, automatic restarts.

*The name: [Vauban](https://en.wikipedia.org/wiki/S%C3%A9bastien_Le_Prestre_de_Vauban) was Louis XIV's military engineer, who fortified sites with a standard system adapted to each terrain.*

## Features

- 🔍 **Detects your stack**: Go, Node/TypeScript or Python, the package manager, existing Dockerfile, workflows, lint and test config, the GitHub remote.
- 🧾 **Plans before it acts**: `vauban plan` lists every file it will write and every command it will run.
- 🐳 **Container image**: a multi-stage Dockerfile for your stack.
- ✅ **CI**: lint and tests with the tooling your repo already has.
- 🏰 **Hardened VM**: Debian 13 on Scaleway, DigitalOcean or Hetzner, managed with OpenTofu, locked down with cloud-init.
- 🔒 **Automatic HTTPS**: Caddy in front of your app, on your domain or on `<ip>.sslip.io` with zero DNS setup.
- 🚀 **Push to deploy**: image built and pushed to GHCR, deployed over SSH by immutable commit sha.
- 🔑 **GitHub wiring**: deploy key generated and `DEPLOY_*` secrets set through `gh`.
- ♻️ **Idempotent and resumable**: run `apply` twice and nothing changes; interrupt it and it picks up where it stopped.
- ⚡ **Lightweight**: one 17 MB binary, `doctor` answers in about 5 ms.

## Install

```sh
# Homebrew (macOS)
brew install jean-baptisteAZAN/tap/vauban

# Go
go install github.com/jean-baptisteAZAN/vauban@latest

# Or grab a binary (macOS and Linux, amd64 and arm64) from
# https://github.com/jean-baptisteAZAN/vauban/releases
```

vauban uses `gh`, `tofu` (or `terraform`), `docker` and `ssh-keygen` when present and reports the missing ones; it never installs anything.

## Quickstart

```sh
vauban doctor   # report: stack detected, what is missing, which tools are installed (bare `vauban` is an alias)
vauban plan     # pick cloud/region/size, see every file and command; only saves vauban.yaml
vauban apply    # confirm, then write files, create the VM, push GitHub secrets
git add -A && git commit -m "ship it" && git push   # vauban never pushes for you
```

vauban deploys from GitHub Actions, so `plan` and `apply` first check for a GitHub `origin` remote and stop (exit 1) before asking anything if there is none, printing the `gh repo create <name> --private --source . --push` command to create one (it needs at least one commit). `doctor` suggests the same command as its next step.

`plan` saves your answers to `vauban.yaml` (commit it) as soon as they are complete and valid, so they are kept even if the plan then fails. After that `plan` and `apply` are non-interactive and reproducible. Without a terminal, pass `--yes` to `apply` (and `--cloud` if there is no `vauban.yaml`). Anything that destroys or replaces cloud resources needs the project name typed in a terminal, or `--confirm <project>` (`apply` and `destroy`).

Useful flags: `--cloud scaleway|digitalocean|hetzner`, `--region`, `--size`, `--domain`, `--port`, `--reconfigure`, `--force` (overwrite generated files only), `--confirm <project>`, `--dir <path>`. Every command has `--help`.

## How it works

1. **`doctor`** scans the repo on disk (no network): stack, what exists, what is missing, which tools are installed.
2. **`plan`** asks for cloud, region, size, domain and port (regions and sizes come from the cloud's API), saves `vauban.yaml`, and prints every file and command `apply` would run. Nothing else changes.
3. **`apply`** checks everything up front (GitHub remote, `gh` login, `tofu`, `ssh-keygen`, cloud credentials), asks for confirmation, then writes the files, generates the deploy key, runs `tofu init/plan/apply`, reads the server IP from `tofu output` and sets the GitHub secrets.

After that, vauban is out of the loop: your next `git push` does the rest.

```mermaid
flowchart LR
    dev["git push<br/>to main"] --> ci["GitHub Actions<br/>ci.yml: lint + test"]
    ci -- "on success" --> deploy["deploy.yml<br/>docker build"]
    deploy --> ghcr[("GHCR<br/>image:sha")]
    deploy -- "SSH (DEPLOY_SSH_KEY)" --> vm
    ghcr -- "docker compose pull" --> vm
    subgraph vm["VM · Debian 13 (OpenTofu + cloud-init)"]
        caddy["Caddy<br/>:80 / :443, auto HTTPS"] --> app["your app<br/>:$PORT, not published"]
    end
    user(("visitors")) -- "HTTPS" --> caddy
```

## What gets created

| Path | What |
|---|---|
| `Dockerfile` (+ `.dockerignore`) | Multi-stage image for your stack |
| `.github/workflows/ci.yml` | Lint and tests with the tooling your repo already has |
| `infra/` | OpenTofu code for the VM and its firewall, plus the cloud-init script that installs the deploy key for the `deploy` user (on DigitalOcean and Hetzner the key is also registered as a cloud SSH key) |
| `deploy/compose.yaml`, `deploy/Caddyfile` | Caddy (automatic HTTPS) in front of your app, both `restart: unless-stopped` |
| `.github/workflows/deploy.yml` | Builds and pushes the image to GHCR, then deploys over SSH |
| GitHub secrets | `DEPLOY_HOST`, `DEPLOY_SSH_KEY`, `DEPLOY_DOMAIN`, set through `gh` |

Existing files are never overwritten: they are reported as `skip (exists)` unless you pass `--force`. Every file and command appears in `vauban plan` before `apply` does it. Changing `app_port` later therefore needs `vauban apply --force` to regenerate the deploy files; when only files, the domain or secrets change, `apply` ends with `push to redeploy`.

Without a domain, HTTPS works out of the box on `<ip>.sslip.io`. With one (`--domain`), `apply` prints the DNS records to create once the server exists: an A record to its IPv4 and, when it has one, an AAAA record to its IPv6. App secrets: add a GitHub secret named `APP_<NAME>` and the deploy job writes `<NAME>` into `~/app/.env` on the VM (mode 600).

## Supported clouds and stacks

Stacks: **Go**, **Node/TypeScript**, **Python**. Your app must listen on `0.0.0.0:$PORT`: vauban sets `PORT` to `app_port` in the container.

| Cloud | Credentials (first match wins) |
|---|---|
| Scaleway | `SCW_ACCESS_KEY`, `SCW_SECRET_KEY`, `SCW_DEFAULT_PROJECT_ID`, else the active profile in `~/.config/scw/config.yaml` |
| DigitalOcean | `DIGITALOCEAN_TOKEN`, else the `doctl` config |
| Hetzner | `HCLOUD_TOKEN`, else `~/.config/hcloud/cli.toml` |

Node: the `start` script must not call pnpm, yarn or bun: the image runs `npm start`.

GitHub access comes from `gh auth login`. vauban stores no credentials itself.

> [!TIP]
> The `DEPLOY_*` secrets are repository secrets. To restrict them to deploys from `main`, move them to a GitHub Environment with a branch protection rule and set `environment:` on the deploy job.

## Security defaults

- Debian 13 VM, non-root `deploy` user, `PermitRootLogin no`, `PasswordAuthentication no`.
- The cloud firewall is the perimeter: tcp 22, 80, 443 and udp 443 (HTTP/3) only. `ufw` on the VM is defense in depth for host services, since Docker-published ports bypass it.
- Unattended security upgrades with a night-time reboot, Docker log rotation, a daily image prune.
- The app is not published on the host; only Caddy can reach it.
- When a CI workflow exists, deploys only run after it succeeds on a push to this repository (pull requests and forks cannot deploy); without one, deploy runs on every push to `main`.
- Images are deployed by immutable commit sha, never `latest`.
- Changes that destroy or replace cloud resources need the project name typed (or `--confirm <project>`); `--yes` and `--force` never approve them.
- No secret value is printed, logged, or written to the repo or to `.vauban/state.json`. The deploy key lives at `~/.ssh/vauban_<project>_ed25519` (0600).

Found a vulnerability? See [SECURITY.md](SECURITY.md).

## Safety and exit codes

`apply` is idempotent and resumable: running it twice changes nothing, and an interrupted run picks up where it stopped. Ctrl-C stops cleanly, tells you to rerun `vauban apply`, and exits 130.

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Failure, including no GitHub remote or a failed preflight (nothing was applied) |
| 2 | Usage problem: `apply` without a terminal and without `--yes`, or with a mismatched `--confirm`; `destroy` without a terminal and without `--confirm <project>` (or a mismatched name); `plan` with no `vauban.yaml` and no `--cloud` outside a terminal |
| 3 | Destructive infra change not confirmed (nothing was applied): rerun and type the project name, or pass `--confirm <project>` |
| 130 | Interrupted (Ctrl-C); state is kept, rerun to resume |

## Destroy

```sh
vauban destroy                      # asks you to type the project name
vauban destroy --confirm my-project # without a terminal
```

Runs `tofu destroy`, deletes the deploy secrets and forgets vauban's state. Generated files and the local deploy key stay. `--yes` and `--force` do not skip the confirmation.

## Known limitations

- OpenTofu state is local (`infra/terraform.tfstate`, gitignored). **Back it up**: losing it orphans your VM. Remote state is planned.
- cloud-init only runs on first boot: changing it does not reach an existing VM. Recreate the VM to apply it.
- Changing cloud means `vauban destroy` first.
- Node apps start with `npm start`.
- GitHub Actions and GHCR only; macOS and Linux only; one service per repo.

## Performance

Measured on an Apple Silicon Mac with `go build -trimpath -ldflags "-s -w"`:

| | Result | Target |
|---|---|---|
| Binary size | 17.4 MB (17,415,346 bytes) | < 20 MB |
| `vauban doctor` startup, `testdata/go-app`, median of 20 runs | 5.3 ms (min 5.2, max 6.1) | < 50 ms |

## Roadmap

Ideas for after the MVP, not promises:

- **v2: bring your own VM.** Point vauban at an existing server over SSH: audit what is there, install only what is missing, share one Caddy between several projects, and make hardening opt-in.
- Remote OpenTofu state.
- More clouds.

Have an idea? [Open a feature request](https://github.com/jean-baptisteAZAN/vauban/issues/new?template=feature_request.yml).

## Contributing

Bug reports, ideas and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the dev setup, and please follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

[MIT](LICENSE)
