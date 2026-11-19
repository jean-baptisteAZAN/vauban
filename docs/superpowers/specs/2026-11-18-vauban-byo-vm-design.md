# vauban — "bring your own VM" (v0.2) design

Date: 2026-11-18
Status: draft, existing-Caddy mode decided (guided), awaiting approval
Builds on: `2026-11-18-vauban-design.md` (MVP, released as v0.1.x)

## Intent

Many people already run a VPS (or a VM on any provider) hosting several
projects behind one Caddy. They want vauban's "set up once, never think about it
again" flow without vauban creating a VM. In this mode vauban connects to an
existing server over SSH with a key the user picks, audits it read-only, shows
what is already there and what is missing, installs only what is missing (with
consent), and deploys the project **next to** the others without touching them.

## Goals

- `target: existing` in `vauban.yaml`, chosen in `vauban plan` (new first
  question: "Create a new VM" vs "Use an existing server").
- Connection: host, SSH port (default 22), SSH user, SSH key picked from
  `~/.ssh/*` private keys that have a matching `.pub` (plus "use ssh-agent").
- `vauban audit` (also run at the start of `plan`/`apply` in this mode): a
  read-only report over SSH.
- Install only what is missing, after confirmation; leave existing things alone.
- Multiple projects per server, each isolated; one shared Caddy (Docker
  container) routes domains to projects.
- The same GitHub deploy pipeline as the MVP (CI → GHCR → SSH deploy, secrets).
- `vauban destroy` in this mode removes **only this project** from the server
  and its GitHub secrets — never the server, never other projects, never Caddy.

## Non-goals (v0.2)

- nginx, Traefik, systemd-installed Caddy, or any non-container reverse proxy:
  detected and reported, then vauban stops with a clear message.
- Non-Debian/Ubuntu servers for *installation* (audit still works; install
  refuses with a message).
- Managing DNS, TLS outside Caddy, backups, databases.
- Migrating a project between servers.

## Anti-goals

- Changing anything on the server that the user did not confirm.
- Restarting, stopping, or reconfiguring other containers/projects (Caddy
  reload is the only shared action, and it is graceful).
- Automatic SSH or firewall hardening (opt-in only: other services may depend
  on the current config).
- Storing the user's private key path in the repo beyond `vauban.yaml`
  (the path is not secret; the key content never leaves the machine).

## Design

### Config

```yaml
project: shop
target: existing            # new; default "cloud" keeps MVP behaviour
server:
  host: 203.0.113.10
  port: 22
  user: jb
  key: ~/.ssh/id_ed25519    # or "agent"
domain: shop.example.com    # required in this mode (no sslip default: a shared IP)
app_port: 3000
```

`cloud`/`region`/`size` are absent in this mode. A domain is **required**
because several projects share one IP; `plan` prints the DNS A record to create.

### Audit (read-only, over SSH)

Runs a fixed, read-only script (no writes, no sudo prompts) and parses its
output. Checks, each `ok / missing / warn / blocked`:

| Check | How (read-only) | Result use |
|---|---|---|
| OS | `/etc/os-release` | install allowed only on Debian/Ubuntu |
| sudo | `sudo -n true` | without passwordless sudo: audit + deploy only if everything is present |
| Docker engine | `docker version` | install from Docker's apt repo if missing |
| Compose plugin | `docker compose version` | install if missing |
| Ports 80/443 | `ss -ltnp` + `docker ps --format` | who listens: vauban/user Caddy container, other proxy, nothing |
| Caddy container | `docker ps` image `caddy:*` publishing 80/443 | reuse; find its Caddyfile mount via `docker inspect` |
| Deploy user | `id deploy` | create (docker group, no sudo) if missing |
| Firewall | `ufw status` / nft | report only; opening 80/443 is offered, nothing else |
| SSH password auth | `sshd -T` (if sudo) | report; hardening opt-in |
| Disk space | `df -h /` | warn under 5 GB free |
| Other projects | `ls ~deploy/apps`, `docker ps` count | report only |

The report is shown in `plan` (styled table, plain in non-TTY).

### Shared Caddy (Docker container)

- **Existing Caddy container** (image `caddy`, publishing 80/443) — *guided,
  vauban never edits the user's Caddy config or compose*:
  - The audit reads `docker inspect` (mounts, networks) and checks three things:
    a host directory is mounted at `/etc/caddy/sites` in the container, the
    Caddyfile contains `import /etc/caddy/sites/*.caddy`, and the container is on
    the Docker network `vauban`.
  - If all three hold: ready, vauban only manages `sites/<project>.caddy`.
  - Otherwise `plan`/`apply` stop before any server change and print the exact
    one-time change to make (the Caddyfile `import` line, the compose `volumes:`
    entry for the sites directory, the external `vauban` network), tailored to
    the paths found by `docker inspect`; vauban creates the `vauban` network
    itself (non-disruptive) so the user's `docker compose up -d` succeeds. The
    user applies it once and reruns `vauban apply`.
  - vauban never recreates, restarts or reconfigures the user's Caddy container.
- **No reverse proxy and 80/443 free**: install a vauban-managed Caddy:
  container `vauban-caddy` (image `caddy:2`), `restart: unless-stopped`,
  network `vauban`, volumes for data/config, Caddyfile containing only
  `import /etc/caddy/sites/*.caddy`, in `/opt/vauban/caddy/`.
- **80/443 held by anything else**: stop with a message naming the process.
- Each project adds `sites/<project>.caddy`:
  `<domain> { reverse_proxy <project>-app:<app_port> }`, then
  `docker exec <caddy> caddy reload --config /etc/caddy/Caddyfile` (graceful,
  validated first with `caddy validate`; on validation failure the site file is
  removed and nothing is reloaded).

### Project layout on the server

- `~deploy/apps/<project>/compose.yaml` + `.env` (0600). The app service joins
  the external network `vauban` with alias `<project>-app`; no host ports.
- Deploy key per project (generated locally as in the MVP), appended to
  `~deploy/.ssh/authorized_keys` with a `# vauban:<project>` comment so destroy
  can remove exactly that line.
- Ownership marker `~deploy/apps/<project>/.vauban-owner` holding the GitHub
  repo slug (`owner/name`). If the directory exists with a different owner (or
  without a marker), vauban refuses: another project already uses that name.
  Same check for `sites/<project>.caddy` without a matching marker.

### Deploy pipeline

Same workflow as the MVP with the trust-boundary guards (push-only, same repo,
immutable sha, tip-of-main check, ephemeral docker config). Differences:
remote directory `~/apps/<project>`, no Caddy service in the project compose
(Caddy is shared and its site file is managed by `vauban apply`, not CI),
secrets `DEPLOY_HOST` (from config), `DEPLOY_SSH_KEY`, `DEPLOY_DOMAIN`.

### Steps (engine)

New module `server` replaces `infra` when `target: existing`: steps
`server:audit` (read-only), `server:install-docker`, `server:install-compose`,
`server:deploy-user`, `server:deploy-key`, `server:network`, `server:caddy`
(reuse/connect or install), `server:site`; each idempotent, each shown in
`plan`, each run over SSH via the engine Runner (`ssh` binary, `BatchMode=yes`,
`StrictHostKeyChecking=accept-new`, host key recorded in state on first
connect and pinned afterwards).

Privileges: setup steps that install or create system things (Docker, Compose,
deploy user, `/opt/vauban/caddy`) need passwordless sudo (`sudo -n`). If one of
those things is missing and sudo is unavailable, `apply` stops before any
change and says what to install by hand. Once the server is set up, project
steps (network, site file, deploy) need only membership of the `docker` group.

Deploy files are mode-aware: generated compose/workflow for `target: existing`
differ from cloud mode (no Caddy service, `~/apps/<project>`). If existing
files were generated for the other mode, vauban refuses to overwrite them
unless `--force`.

Server upkeep (OS updates, Docker upgrades, disk cleanup) is out of scope:
`vauban audit` reports it (pending updates, low disk) but never acts.

### Destroy (this mode)

Typed confirmation as in the MVP. Over SSH: `docker compose down` in the
project dir, remove `~deploy/apps/<project>`, remove `sites/<project>.caddy`
and reload Caddy, remove the `# vauban:<project>` authorized_keys line; then
delete the GitHub secrets and local state. Never removes Caddy, the network,
the deploy user, Docker, or other projects. Project Docker volumes are kept by
default (`docker compose down` without `-v`); `vauban destroy --purge` also
removes them, with a second line in the confirmation naming the volumes.
Destroy only touches a project directory whose `.vauban-owner` matches this
repo.

## Decisions (author's calls, open to correction)

- Shared Caddy is a Docker container only (author's setup); the vauban-managed
  one lives in `/opt/vauban/caddy`.
- Existing Caddy: guided one-time change by the user (decided by the user),
  never edited or recreated by vauban.
- Domain required in this mode (no sslip default on a shared IP).
- Deploy user `deploy` shared by all vauban projects on the server; one deploy
  key per project.
- Host key: trust on first use, then pinned in `.vauban/state.json` and used for
  every later SSH (vauban and CI via a `DEPLOY_KNOWN_HOSTS` secret).
- Install steps target Debian/Ubuntu only.

## Left to the builder

- Exact audit script and output format (must be read-only and parseable).
- How to present partial-sudo situations in the audit table.
- Test strategy for SSH steps (fake runner + a throwaway Debian container with
  sshd for an end-to-end test).
