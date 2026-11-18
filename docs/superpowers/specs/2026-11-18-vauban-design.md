# vauban — design (MVP)

Date: 2026-11-18
Status: draft, awaiting approval

## Intent

Guiding principle (author): **set up everything once, then never think about
it again.** Every default favours a setup that keeps running unattended
(automatic TLS, automatic security updates, automatic restarts, disk cleanup).

`vauban` is a fast, lightweight, good-looking CLI that looks at the current
repository, reports what is missing to ship it (CI, lint, tests, container,
infrastructure), and adds it. It does not maintain a library of hand-written
templates for everything: wherever an official tool or API exists, vauban
delegates to it. Its own value is the **wiring** between tools: secrets pushed
into GitHub, Terraform outputs injected into CI, SSH keys generated once and
placed where each side needs them.

It is first a personal tool for the author, published on their GitHub. It must
feel polished enough to show.

## Goals

- `vauban` / `vauban doctor`: detect the stack and report what exists and what
  is missing, in under a second, with no network calls required.
- `vauban plan`: ask a few questions (cloud, region, size), then show every
  file it will write and every external command it will run. Nothing changes.
- `vauban apply`: execute the plan after confirmation. Idempotent: running it
  twice changes nothing the second time. Resumable after a failure.
- Support three clouds: **Scaleway, DigitalOcean, Hetzner**.
- Support three stacks for MVP: **Go, Node/TypeScript, Python**.
- End state for a fresh repo: push to `main` → GitHub Actions lints, tests,
  builds a container image, pushes it to GHCR, deploys it to a VM on the chosen
  cloud over SSH.

## Non-goals (MVP)

- AWS, GCP, Azure, Kubernetes, managed databases, serverless.
- GitLab CI or other CI systems (GitHub Actions only).
- Monorepos with several deployable services.
- Managing DNS zones (vauban only prints or, where trivially possible, creates
  one A/AAAA record; see Runtime).
- Windows support (macOS and Linux only).
- Remote Terraform state (see Decisions; planned for v2).

## Anti-goals (failure even if it works)

- Slow startup or a heavy binary (target: < 50 ms for `doctor`, < 20 MB binary).
- Writing or overwriting any file without showing it in `plan` first.
- Silently overwriting a file that already exists (existing files are skipped
  and reported, never replaced, unless `--force`).
- Printing or writing secrets in clear text in the repo or logs.
- Insecure VM defaults (password SSH, root login, open ports beyond 22/80/443).

## Constraints

- Language: Go 1.27, latest library versions at time of build.
- Single static binary, released with goreleaser (GitHub Releases + Homebrew tap).
- External tools are used when present and checked up front: `gh`, `tofu`
  (falls back to `terraform`), `docker`, `ssh-keygen`. Missing tools are
  reported by `doctor` with install hints; vauban never installs them.

## Approaches considered

| Approach | Trade-off | Outcome |
|---|---|---|
| **Go + Charm** (cobra + fang, huh, lipgloss, bubbletea) | Single ~10 MB binary, instant start, best-in-class CLI styling, official Go SDKs for all three clouds | **Chosen** |
| Rust + ratatui/inquire | Smaller/faster, but no official Scaleway/Hetzner SDKs, slower to build | Rejected |
| TypeScript/Bun + clack | Nicest prompts quickest, but 50–90 MB compiled binary | Rejected |

Infrastructure mechanism: vauban **generates OpenTofu/Terraform code** using
the official providers and delegates `tofu apply`, rather than creating VMs via
cloud CLIs/APIs, so the infrastructure stays as reviewable code in the repo.
Cloud SDKs are used only to validate credentials and list regions/sizes for
the prompts.

## Design

### Commands

```
vauban                 # = doctor
vauban doctor          # detection report
vauban plan            # interactive choices → plan, no changes
vauban apply           # plan + confirm + execute
vauban destroy         # tofu destroy + cleanup, typed confirmation
vauban version
Flags: --yes (no prompts, accept defaults/flags), --cloud scaleway|digitalocean|hetzner,
       --region, --size, --force, --dir <path>
```

Answers from `plan` are saved to `vauban.yaml` at repo root (committed), so
`apply` and later runs are non-interactive and reproducible.

### Architecture

- `internal/detect`: pure filesystem detection → `Project` struct
  (stack, package manager, existing Dockerfile, workflows, `*.tf`, lint/test
  config, git remote). No network.
- `internal/module`: one module per concern, each implementing:

  ```go
  type Module interface {
      ID() string
      Detect(p *Project) Status            // present / missing / partial
      Requires() []string                  // output keys from other modules
      Plan(p *Project, c *Config) ([]Step, error)
  }
  type Step interface {                    // WriteFile, RunCommand, SetSecret
      Describe() string
      Apply(ctx context.Context, in Outputs) (Outputs, error)
  }
  ```
  Modules are ordered by a small dependency graph; outputs of one step
  (e.g. `server_ip`) feed later steps.
- Modules for MVP:
  1. `container`: Dockerfile + `.dockerignore`. Delegates to `docker init` only
     if it can be driven non-interactively (to verify in an early spike);
     otherwise uses small embedded per-stack Dockerfiles (multi-stage,
     non-root, distroless/slim).
  2. `ci`: `.github/workflows/ci.yml` (lint + test + build) using official
     setup actions and the project's own scripts/tools (golangci-lint, ruff,
     the `lint`/`test` scripts in package.json).
  3. `infra`: `infra/` OpenTofu code for the chosen cloud: one VM, firewall,
     SSH key, cloud-init.
  4. `deploy`: `.github/workflows/deploy.yml` (build → push GHCR → SSH →
     `docker compose pull && up -d`) + `deploy/compose.yaml`.
  5. `wiring`: generate ed25519 deploy key, `tofu apply`, read `tofu output`,
     `gh secret set DEPLOY_HOST / DEPLOY_SSH_KEY / DEPLOY_USER`.
- `internal/cloud/{scaleway,digitalocean,hetzner}`: credential check and
  region/size listing via official SDKs; Terraform provider block + VM
  resource snippets per cloud.
- `internal/state`: `.vauban/state.json` (gitignored) records completed steps
  and outputs for idempotence and resume.
- `internal/ui`: lipgloss theme, huh forms, bubbletea spinners; plain output
  when not a TTY or `NO_COLOR` is set.

### VM defaults (cloud-init)

Debian stable, Docker Engine + compose plugin from Docker's apt repo,
non-root `deploy` user in the `docker` group with the deploy public key,
`PermitRootLogin no`, `PasswordAuthentication no`, ufw allowing 22/80/443,
unattended-upgrades on. Cloud firewall mirrors the same rules. Smallest
reasonable size per cloud as default (e.g. Hetzner CX22, DO s-1vcpu-1gb,
Scaleway DEV1-S / equivalent current small instance).

### Runtime on the VM (author decision: Caddy in MVP)

- `deploy/compose.yaml` runs two services: `caddy` (official image, ports
  80/443, persistent volume for certificates) and `app` (GHCR image, not
  published on the host, reachable only by Caddy on the compose network).
  Both `restart: unless-stopped`.
- `deploy/Caddyfile`: `{$DOMAIN} { reverse_proxy app:{$APP_PORT} }`, with
  automatic HTTPS via Let's Encrypt.
- Domain: asked in `plan`. If the user has none, default to
  `<ip-with-dashes>.sslip.io` so HTTPS works with zero DNS setup (builder's
  call; the user can switch to a real domain later by editing `vauban.yaml`
  and re-running `apply`). If a domain is given, `plan` prints the A record to
  create; creating it automatically is out of MVP scope.
- App port: read from the Dockerfile's `EXPOSE`; otherwise asked in `plan`,
  defaulting to 8080 (Go), 3000 (Node), 8000 (Python).
- App env/secrets: GitHub secrets prefixed `APP_` are written by the deploy
  job into `deploy/.env` on the VM (prefix stripped, file mode 0600).
- Unattended upkeep: unattended-upgrades with automatic reboot at night,
  a daily `docker image prune` timer, Docker log rotation in `daemon.json`.

### Credentials

Read from the standard env vars / config each cloud's own CLI uses:
`SCW_ACCESS_KEY`/`SCW_SECRET_KEY`/`SCW_DEFAULT_PROJECT_ID` or `~/.config/scw/config.yaml`;
`DIGITALOCEAN_TOKEN` or doctl config; `HCLOUD_TOKEN` or hcloud config.
GitHub via `gh auth status`. vauban stores no credentials itself.

## Decisions (author's calls, open to correction)

- **Terraform state is local** (`infra/terraform.tfstate`, gitignored) for MVP,
  with a clear warning in `plan`. Remote state on the cloud's S3-compatible
  storage is v2.
- **Registry is GHCR** for all clouds (no per-cloud registry), using
  `GITHUB_TOKEN` in the workflow; the VM logs in during the deploy job.
- **Existing files are never overwritten**; vauban reports them as present
  and skips that module's file.
- **Lint/tests**: CI runs whatever lint/test tooling the repo already has. If
  no lint config exists, vauban adds a default one (golangci-lint for Go, ruff
  for Python; for Node it only warns). It never writes tests.
- **Deploy key**: kept at `~/.ssh/vauban_<project>_ed25519` (0600);
  `state.json` stores only the path, never secrets. The author's own public key
  (`~/.ssh/id_ed25519.pub` if present) is also installed for the `deploy` user.
- **`apply` stops after files + GitHub secrets**, then prints the
  `git add … && git commit && git push` to run. vauban never pushes.
- **Config drift**: if `vauban.yaml` changes after an apply, `plan` shows the
  `tofu plan` diff with destroy/replace actions highlighted in red; `apply`
  then requires typing the project name. A `vauban destroy` command is added.
- **Existing CI**: when vauban generated or recognises a CI workflow, deploy
  runs via `workflow_run` after it succeeds; otherwise on push to `main`.
- **Size/startup targets are soft**: measured from the first build; if cloud
  SDKs blow the budget, fall back to small direct HTTP clients.
- **Tests**: unit tests for detect/plan (golden files for generated output),
  fake runner for commands; three sample repos under `testdata/` (Go, Node,
  Python). Real cloud runs are manual.

## Left to the builder

- Exact styling (palette, layout of the doctor report).
- Exact current default instance types per cloud (check provider docs at
  build time).
- Whether `docker init` is usable non-interactively (spike first).
