# vauban

Point it at a repo, answer three questions, and push: CI, a container image, a hardened VM with automatic HTTPS, and the GitHub deploy wiring are set up once and then left alone.

<!-- demo: docs/demo.gif (recorded from docs/demo.tape with vhs) -->
*Demo GIF coming soon.*

## Why

Shipping a small app should be a one-time chore, not a hobby. Vauban does not carry a pile of hand-written templates: it delegates to the official tools (`docker`, `tofu`, `gh`, `ssh-keygen`, the cloud providers) and writes the glue between them: Terraform outputs into CI, SSH keys where each side needs them, secrets into GitHub. Every default favours a setup that runs unattended: automatic TLS, automatic security updates, automatic restarts.

## Install

```sh
# Homebrew (macOS)
brew install jean-baptisteAZAN/tap/vauban

# Go
go install github.com/jean-baptisteAZAN/vauban@latest

# Or grab a binary (macOS and Linux, amd64 and arm64) from
# https://github.com/jean-baptisteAZAN/vauban/releases
```

Vauban uses `gh`, `tofu` (or `terraform`), `docker` and `ssh-keygen` when present and reports the missing ones; it never installs anything.

## Quickstart

```sh
vauban          # report: stack detected, what is missing, which tools are installed
vauban plan     # pick cloud/region/size, see every file and command; changes nothing
vauban apply    # confirm, then write files, create the VM, push GitHub secrets
git add -A && git commit -m "ship it" && git push   # vauban never pushes for you
```

`plan` saves your answers to `vauban.yaml` (commit it). After that `plan` and `apply` are non-interactive and reproducible. Without a terminal, pass `--yes` (and `--cloud` if there is no `vauban.yaml`).

Useful flags: `--cloud scaleway|digitalocean|hetzner`, `--region`, `--size`, `--domain`, `--port`, `--reconfigure`, `--force`, `--dir <path>`.

## What gets created

| Path | What |
|---|---|
| `Dockerfile` (+ `.dockerignore`) | Multi-stage image for your stack |
| `.github/workflows/ci.yml` | Lint and tests with the tooling your repo already has |
| `infra/` | OpenTofu code for the VM, firewall and SSH key, plus the cloud-init script |
| `deploy/compose.yaml`, `deploy/Caddyfile` | Caddy (automatic HTTPS) in front of your app, both `restart: unless-stopped` |
| `.github/workflows/deploy.yml` | Builds and pushes the image to GHCR, then deploys over SSH |
| GitHub secrets | `DEPLOY_HOST`, `DEPLOY_SSH_KEY`, `DEPLOY_DOMAIN`, set through `gh` |

Existing files are never overwritten: they are reported as `skip (exists)` unless you pass `--force`. Every file and command appears in `vauban plan` before `apply` does it.

Without a domain, HTTPS works out of the box on `<ip>.sslip.io`. With one, `plan` prints the DNS A record to create. App secrets: add a GitHub secret named `APP_<NAME>` and the deploy job writes `<NAME>` into `deploy/.env` on the VM (mode 0600).

## Supported clouds and stacks

Stacks: **Go**, **Node/TypeScript**, **Python**.

| Cloud | Credentials (first match wins) |
|---|---|
| Scaleway | `SCW_ACCESS_KEY`, `SCW_SECRET_KEY`, `SCW_DEFAULT_PROJECT_ID`, else the active profile in `~/.config/scw/config.yaml` |
| DigitalOcean | `DIGITALOCEAN_TOKEN`, else the `doctl` config |
| Hetzner | `HCLOUD_TOKEN`, else `~/.config/hcloud/cli.toml` |

GitHub access comes from `gh auth login`. Vauban stores no credentials itself.

## Security defaults

- Debian stable VM, non-root `deploy` user, `PermitRootLogin no`, `PasswordAuthentication no`.
- Ports 22, 80 and 443 only, enforced twice: `ufw` on the VM and the cloud firewall.
- Unattended security upgrades with a night-time reboot, Docker log rotation, a daily image prune.
- The app is not published on the host; only Caddy can reach it.
- Deploys only run after CI succeeds on a push to this repository: pull requests and forks cannot deploy.
- Images are deployed by immutable commit sha, never `latest`.
- Changes that destroy or replace cloud resources need the project name typed (or `--force`).
- No secret value is printed, logged, or written to the repo or to `.vauban/state.json`. The deploy key lives at `~/.ssh/vauban_<project>_ed25519` (0600).

## Safety and exit codes

`apply` is idempotent and resumable: running it twice changes nothing, and an interrupted run picks up where it stopped. Ctrl-C stops cleanly, tells you to rerun `vauban apply`, and exits 130.

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Failure, including a failed preflight (nothing was applied) |
| 2 | Usage problem: no terminal and no `--yes` / `--confirm`, or no `vauban.yaml` and no `--cloud` |
| 3 | Destructive infra change not confirmed (nothing was applied) |
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
| `vauban` (doctor) startup, `testdata/go-app`, median of 20 runs | 5.3 ms (min 5.2, max 6.1) | < 50 ms |

## License

[MIT](LICENSE)
