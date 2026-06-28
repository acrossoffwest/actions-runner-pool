# gharp-portal 🪉

**A self-hosted, multi-tenant control plane for GitHub Actions runner pools.**

Put one server in front of [gharp](https://github.com/muhac/actions-runner-pool)
(an ephemeral, autoscaling self-hosted runner pool) and let *several people* —
each with their own GitHub account, App, and repositories — run their own
isolated runners on it. Admin invites users; each user signs in with GitHub, gets
their own kernel-isolated sandbox running their own gharp instance, and sees only
their own runners and jobs.

> gharp on its own is **single-tenant**: one GitHub App, one dashboard, no login.
> gharp-portal makes it **multi-user, isolated, and self-service** — ideal for two
> friends, a small team, or anyone hosting runners for multiple accounts/orgs on
> one box.

---

## Why

Self-hosted GitHub Actions runners are great, but:

- A raw runner (or a single gharp) serves **one** GitHub App / account.
- Running several people's runners on one server means either trusting them with
  each other's secrets, or standing up a VM per person.
- There's no login, no per-user view, no "give my friend their own runners"
  button.

gharp-portal fixes that: **one server, many tenants, real isolation, a UI.**

## What you get

- 🔑 **GitHub OAuth login** + roles (`admin` / `user`), invite-only.
- 🧑‍💼 **Admin console** — invite users, assign each an isolated slot, watch
  instance health, audit log.
- 📦 **Per-user isolated slot** — its own OS user, Docker network, cgroup
  CPU/memory caps, and nftables egress firewall (blocks cloud metadata).
- 🪞 **Each user sees only their own runners** — the portal reverse-proxies each
  user to *their* gharp dashboard; one user's GitHub App keys never touch
  another's instance or the portal DB.
- ▶️ **Start/stop your instance** from the UI; in-product **"Connect a repo"
  guide**.
- 🛡️ **Public-repo guard on by default** (fork-PR RCE protection); ephemeral
  runners (one job → fresh container).
- 🎨 Polished dark/light UI shared across every page.

## Architecture

```
                      ┌────────────────────────────────────────────┐
   admin ────────────▶│  PORTAL  (Go, unprivileged)  :8091          │
   users ── OAuth ───▶│  • GitHub OAuth login + sessions + CSRF     │
                      │  • roles, invite-only allowlist             │
                      │  • admin: users + slot assignment + audit   │
                      │  • lifecycle: start/stop a user's gharp     │
                      │  • reverse proxy  /app/*  → user's gharp     │
                      └───────┬──────────────────────┬──────────────┘
        assign + proxy        │                      │
        ┌─────────────────────┘                      └──────────────────┐
   SLOT 1  (OS user, Docker net, cgroup caps,    SLOT 2  (…)
            nftables egress)
   ┌──────────────────────────────────┐          ┌──────────────────────────────┐
   │ gharp (user A) :9001              │          │ gharp (user B) :9002          │
   │   own GitHub App + own runners    │          │   own GitHub App + runners    │
   │   ephemeral runner containers     │          │   …                           │
   └──────────────────────────────────┘          └──────────────────────────────┘
        ▲ GitHub webhooks (workflow_job) hit each gharp directly via
          https://<host>/gh/slot-N/  (nginx strips the prefix)
```

- The **portal** is the only thing users log into. It never holds runner-App
  private keys and never runs privileged operations.
- Each **slot** is pre-provisioned once by an operator script; the portal just
  assigns a free slot to a user and starts/stops their gharp via a single
  allow-listed `sudo` wrapper.
- **TLS** is terminated by a reverse proxy / CDN (e.g. Cloudflare); the origin
  speaks plain HTTP behind nginx.

## How a user goes live

1. **Admin** signs in (bootstrap admin via `BOOTSTRAP_ADMIN_LOGIN`), invites a
   GitHub login, assigns them a free slot.
2. **User** signs in with GitHub → lands on `/app` → **Start** their instance.
3. User clicks **Set up runners** → creates a GitHub App (gharp drives the
   manifest flow) → installs it on their repos/org.
4. In a workflow: `runs-on: [self-hosted]`. Push → a fresh ephemeral runner spins
   up in the user's slot, runs the job, and is destroyed.

## Repository layout

This repository is a monorepo: the **gharp runner pool** at the root (the engine
that runs inside each slot) plus the **portal** and its provisioner.

```
cmd/gharp/  internal/  docs/   # gharp — the ephemeral runner pool (runs in each slot)
gharp-portal/            # the multi-tenant portal (Go, stdlib + net/http + html/template + sqlite)
  cmd/portal/            #   entrypoint
  internal/
    config/  store/      #   config + SQLite (users, slots, assignments, sessions, audit)
    auth/                #   GitHub OAuth, sessions, CSRF, RBAC
    slots/               #   slots.yaml registry + assignment
    lifecycle/  proxy/   #   start/stop a user's gharp + reverse proxy
    httpapi/             #   router, handlers, templates, design tokens
    wiring/              #   composition root (adapters + auth→proxy context bridge)
provisioner/             # operator scripts that create kernel-isolated slots
  provision-slot.sh      #   rootless-Docker slots (LTS kernels)
  provision-slot-rootful.sh  # shared rootful-Docker slots (bleeding-edge kernels)
  host/                  #   gharp-svc wrapper + sudoers
docs/specs/              # portal design spec
```

## Deploy (quick start)

**Prereqs on the host:** Linux, Docker, nginx (or any reverse proxy), a domain
behind TLS (Cloudflare "Flexible" works), and a GitHub OAuth App for the portal.

```bash
# 1. Build the portal (cgo-free, static)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o gharp-portal ./gharp-portal/cmd/portal

# 2. Configure (env file, mode 600, root-owned)
cat >/etc/gharp-portal.env <<'ENV'
BASE_URL=https://runners.example.com
PORT=8091
BIND_ADDR=127.0.0.1                 # behind the reverse proxy
STORE_DSN=file:/var/lib/gharp-portal/portal.db
SLOTS_CONFIG=/var/lib/gharp-portal/slots.yaml
SESSION_TTL=7d
BOOTSTRAP_ADMIN_LOGIN=your-github-login
PORTAL_OAUTH_CLIENT_ID=...           # GitHub OAuth App (callback: $BASE_URL/auth/callback)
PORTAL_OAUTH_CLIENT_SECRET=...
ENV

# 3. Run it (systemd unit + nginx reverse proxy on :80 → 127.0.0.1:8091).
#    4. Provision slots (operator, root):
sudo SLOTS_YAML=/var/lib/gharp-portal/slots.yaml PUBLIC_HOST=runners.example.com \
  provisioner/provision-slot-rootful.sh slot-1 5001
#    add the nginx /gh/slot-1/ route, then POST /admin/slots/reload (or restart).
```

See [`docs/specs/`](docs/specs/) for the full design and
[`provisioner/`](provisioner/) for slot provisioning details (rootless vs.
rootful, isolation knobs, host prep).

## Security model

- **Web process is unprivileged.** Slot provisioning is operator-only; the portal
  can only start/stop slot units through one validated, allow-listed `sudo`
  helper.
- **Per-tenant secret separation.** A user's runner GitHub App private key lives
  only inside their slot's gharp — never in the portal DB or another slot.
- **Per-slot kernel isolation.** Dedicated OS user + Docker network + cgroup caps
  + nftables egress (cloud-metadata blocked). For an *untrusted* boundary use an
  LTS kernel + rootless Docker (`provision-slot.sh`); the rootful-shared model
  (`provision-slot-rootful.sh`) suits mutually-trusted tenants.
- **Public repos blocked** by default in every slot (`ALLOW_PUBLIC_REPOS=false`),
  closing the fork-PR RCE hole that makes self-hosted runners dangerous on public
  repos.
- Sessions are `HttpOnly; Secure; SameSite`; all mutations are CSRF-protected;
  the reverse proxy enforces slot ownership and injects the gharp token
  server-side.

## Tech

Go standard library only (no web framework), `html/template` + vanilla JS, pure-Go
SQLite (`modernc.org/sqlite`) — ships as a single static binary with templates and
CSS embedded.

## Status

Built and running in production. Authentication, proxying, slot isolation, and the
provisioner were cross-provider security-reviewed. Contributions welcome.

## License

The portal is provided as-is; the vendored gharp keeps its own (Apache-2.0)
license.
