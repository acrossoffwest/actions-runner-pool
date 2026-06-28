# gharp Multi-Tenant Portal — Full Specification

**Status:** Draft for implementation
**Date:** 2026-06-27
**Owner:** (operator)
**Approach:** B — thin multi-tenant portal in front of per-user single-tenant gharp instances, each in its own kernel-isolated sandbox slot.

---

## 1. Summary

Today `gharp` (`actions-runner-pool`) is a single-tenant, no-login service that
serves ONE GitHub App and spawns ephemeral GitHub Actions runner containers via
one Docker daemon. The goal is a **multi-user control plane**:

- An **admin** logs in, invites/manages users.
- Each **user** logs in (GitHub OAuth), connects their own GitHub App(s), and
  runs their own runners.
- Each user sees **only their own** Apps, runners, and jobs.
- Each user's runner workloads are **kernel-isolated** from every other user.

We achieve this WITHOUT rewriting gharp's internals. We build a new **Portal**
service that handles auth, user management, slot assignment, and lifecycle, and
we run **one unmodified gharp instance per user** inside a **pre-provisioned,
isolated sandbox slot**. The Portal never performs privileged operations; slots
are prepared once by an operator-run provisioner.

## 2. Goals / Non-Goals

### Goals
- GitHub OAuth login; roles `admin` and `user`.
- Admin: invite (allowlist) users, list, disable/enable, assign a slot.
- User: self-service — connect a GitHub App and manage runners via their gharp
  dashboard, proxied through the Portal.
- Per-user data isolation (each sees only their own).
- Per-user kernel-level resource isolation (separate OS user + rootless Docker +
  network + cgroup limits + egress firewall).
- Portal web process runs **unprivileged**.
- Reuse the existing gharp binary unchanged (or near-unchanged).

### Non-Goals (this version)
- Multiple GitHub Apps per single user (deferred — Phase 4; one App per user now).
- Billing / quota accounting beyond simple per-slot caps.
- Auto-scaling the number of slots (operator provisions a fixed pool).
- Self-service slot provisioning by users (provisioning stays operator-only).
- Public-repo runners (inherited gharp guard keeps `ALLOW_PUBLIC_REPOS=false`).

## 3. Repository / Directory Layout

Workspace root: `github-runner-server/`

```
github-runner-server/
├── actions-runner-pool/        # gharp fork (upstream, kept ~unmodified)
│   └── internal/httpapi/handlers/templates/   # WS-F redesign target
├── gharp-portal/               # NEW Go service (the Portal)
│   ├── cmd/portal/main.go
│   ├── internal/
│   │   ├── config/             # WS-A
│   │   ├── store/              # WS-A  (sqlite: users, slots, assignments, sessions, audit)
│   │   ├── auth/               # WS-B  (GitHub OAuth, sessions, CSRF, role middleware)
│   │   ├── slots/              # WS-C  (slot registry load + assignment logic)
│   │   ├── lifecycle/          # WS-D  (start/stop/health of gharp per slot)
│   │   ├── proxy/              # WS-D  (reverse proxy to a user's gharp)
│   │   └── httpapi/            # WS-E  (router, login + admin + proxy handlers, templates)
│   ├── go.mod
│   └── README.md
├── provisioner/                # WS-C  (slot provisioning scripts — operator/root)
│   ├── provision-slot.sh
│   ├── slots.example.yaml
│   └── README.md
└── docs/specs/                 # this spec
```

The Portal is a **separate Go module** from gharp. It mirrors gharp's
conventions: standard library + `net/http`, `html/template`, `modernc.org/sqlite`
(pure-Go, no cgo) or the same driver gharp uses, table-driven tests, no heavy
framework, single static binary.

## 4. Architecture

```
                       ┌──────────────────────────────────────────┐
   admin ─────────────▶│  PORTAL  (gharp-portal, UNPRIVILEGED)     │
   users ── OAuth ─────▶│  :443                                    │
                       │  • GitHub OAuth login + sessions          │
                       │  • roles admin/user, allowlist gate       │
                       │  • admin: user mgmt + slot assignment     │
                       │  • lifecycle: start/stop user's gharp     │
                       │  • reverse proxy /app/* → user's gharp    │
                       └───────┬───────────────────┬──────────────┘
            assign + proxy     │                   │
        ┌──────────────────────┘                   └──────────────────────┐
   SLOT 1 (OS user gharp-s1, rootless dockerd,     SLOT 2 (OS user gharp-s2, ...)
   net gharp-s1, cgroup caps, nftables egress)
   ┌───────────────────────────────────────┐      ┌───────────────────────────────┐
   │ gharp (user A) :open only to portal    │      │ gharp (user B)                 │
   │   DOCKER_HOST=unix://…/s1/docker.sock   │      │   DOCKER_HOST=…/s2/docker.sock │
   │   ALLOW_PUBLIC_REPOS=false              │      │   …                            │
   │   ephemeral runner containers in slot 1 │      │   ephemeral runners in slot 2  │
   └───────────────────────────────────────┘      └───────────────────────────────┘
        ▲ GitHub webhooks (workflow_job) go DIRECTLY to each gharp's public BASE_URL
```

Data-plane note: GitHub `workflow_job` webhooks hit each user's gharp directly
(its own `BASE_URL`), NOT the Portal. The Portal is control-plane only:
auth, management, lifecycle, and the human-facing reverse proxy.

## 5. Components & Responsibilities

| # | Component | Package / Path | Privilege |
|---|-----------|----------------|-----------|
| Portal core | config + store | `internal/config`, `internal/store` | none |
| Auth | OAuth, sessions, RBAC | `internal/auth` | none |
| Slots | registry + assignment | `internal/slots` | none (reads operator-provided registry) |
| Lifecycle | start/stop/health gharp | `internal/lifecycle` | invokes ONE allow-listed command per slot |
| Proxy | reverse proxy to gharp | `internal/proxy` | none |
| HTTP API/UI | router, handlers, templates | `internal/httpapi` | none |
| Provisioner | slot prep scripts | `provisioner/` | **root, operator-run, OUT of web process** |

### Privilege boundary (critical)
The web process must never run `useradd`, `dockerd`, `nft`, `sudo <arbitrary>`,
etc. The only "privileged-ish" thing Lifecycle may do is invoke a **single,
fixed, allow-listed** command to start/stop a user's gharp in an existing slot —
implemented as a per-slot `systemd --user` unit the slot's own user owns, or a
narrowly scoped sudoers entry for exactly `start-gharp <slot-id>` /
`stop-gharp <slot-id>`. No arguments beyond a validated slot id.

## 6. Data Model (Portal SQLite — separate DB from any gharp)

```sql
CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  github_id     INTEGER NOT NULL UNIQUE,     -- stable GitHub numeric id
  github_login  TEXT    NOT NULL,
  role          TEXT    NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user')),
  status        TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','invited')),
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Allowlist: a row may exist as 'invited' before the user first logs in.
-- First login matches by github_login (case-insensitive), then binds github_id.

CREATE TABLE slots (
  id            TEXT    PRIMARY KEY,          -- e.g. "slot-1"
  os_user       TEXT    NOT NULL,             -- e.g. "gharp-s1"
  docker_host   TEXT    NOT NULL,             -- e.g. "unix:///run/user/1001/docker.sock"
  network       TEXT    NOT NULL,             -- docker network name
  base_url      TEXT    NOT NULL,             -- public HTTPS for that gharp's webhooks
  internal_addr TEXT    NOT NULL,             -- host:port the portal proxies to (loopback)
  cpu_limit     TEXT    NOT NULL DEFAULT '',  -- informational mirror of cgroup cap
  mem_limit     TEXT    NOT NULL DEFAULT '',
  max_runners   INTEGER NOT NULL DEFAULT 4,
  status        TEXT    NOT NULL DEFAULT 'free' CHECK (status IN ('free','assigned','disabled')),
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE assignments (
  user_id      INTEGER NOT NULL UNIQUE REFERENCES users(id),
  slot_id      TEXT    NOT NULL UNIQUE REFERENCES slots(id),
  gharp_state  TEXT    NOT NULL DEFAULT 'stopped' CHECK (gharp_state IN ('stopped','starting','running','error')),
  assigned_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE sessions (
  token       TEXT PRIMARY KEY,               -- random 32-byte, base64url
  user_id     INTEGER NOT NULL REFERENCES users(id),
  csrf        TEXT NOT NULL,
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at  DATETIME NOT NULL
);

CREATE TABLE audit_log (
  id          INTEGER PRIMARY KEY,
  actor_id    INTEGER REFERENCES users(id),
  action      TEXT NOT NULL,                  -- e.g. "user.invite", "slot.assign", "gharp.start"
  target      TEXT NOT NULL DEFAULT '',
  detail      TEXT NOT NULL DEFAULT '',
  at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

**No GitHub App credentials are stored in the Portal DB.** Each user's runner
App credentials live only inside that user's gharp SQLite, inside their slot.
This is the blast-radius separation: compromising the Portal yields user
identities and slot routing, never the runner App private keys.

### Store interface contract (WS-A defines; WS-B/C/D/E code against it)

```go
package store

type User struct { ID int64; GitHubID int64; Login, Role, Status string; CreatedAt, UpdatedAt time.Time }
type Slot struct { ID, OSUser, DockerHost, Network, BaseURL, InternalAddr, CPULimit, MemLimit, Status string; MaxRunners int }
type Assignment struct { UserID int64; SlotID, GharpState string; AssignedAt time.Time }
type Session struct { Token string; UserID int64; CSRF string; ExpiresAt time.Time }

type Store interface {
  // users
  UpsertUserOnLogin(githubID int64, login string) (User, error)  // binds invited→active
  GetUserByGitHubID(id int64) (User, bool, error)
  InviteUser(login, role string) (User, error)
  ListUsers() ([]User, error)
  SetUserStatus(id int64, status string) error
  // slots
  ListSlots() ([]Slot, error)
  GetSlot(id string) (Slot, bool, error)
  UpsertSlot(Slot) error                       // used by registry loader (WS-C)
  // assignments
  AssignFreeSlot(userID int64) (Assignment, error)  // atomically picks a free slot
  AssignSlot(userID int64, slotID string) (Assignment, error)
  GetAssignmentByUser(userID int64) (Assignment, bool, error)
  SetGharpState(userID int64, state string) error
  // sessions
  CreateSession(userID int64, ttl time.Duration) (Session, error)
  GetSession(token string) (Session, bool, error)
  DeleteSession(token string) error
  // audit
  Audit(actorID int64, action, target, detail string) error
}
```

## 7. Authentication & Authorization

- **Login:** GitHub OAuth (Authorization Code). Portal has ITS OWN GitHub OAuth
  App (env: `PORTAL_OAUTH_CLIENT_ID`, `PORTAL_OAUTH_CLIENT_SECRET`,
  callback `${BASE_URL}/auth/callback`). Scope: `read:user` only (identity).
- **Gate:** after OAuth, look up `github_id`/`login` in `users`. If not present
  and status not `invited` → `403 not invited`. Otherwise bind id, set status
  `active`, issue session.
- **Bootstrap admin:** env `BOOTSTRAP_ADMIN_LOGIN`. On first login matching it,
  the user is created/promoted to `admin`.
- **Sessions:** opaque random token in `HttpOnly; Secure; SameSite=Lax` cookie.
  Server-side `sessions` row, TTL default 7d (`SESSION_TTL`). Rotate on login.
- **CSRF:** double-submit token (`csrf` in session, required header/field on all
  state-changing POST/PATCH). Mirrors gharp's care here.
- **RBAC middleware:** `RequireUser` (any active), `RequireAdmin` (role admin).
- **Logout:** `POST /auth/logout` deletes session, clears cookie.

## 8. Slots & Isolation (security spine)

### Provisioner (operator-run, root, OUTSIDE the web process)
`provisioner/provision-slot.sh <slot-id> <uid>` performs, idempotently:
1. Create OS user `gharp-s<N>` (no login shell, dedicated home).
2. Enable **rootless Docker** for that user (`dockerd-rootless-setuptool.sh`),
   socket at `/run/user/<uid>/docker.sock`. Lingering enabled so it survives
   logout (`loginctl enable-linger`).
3. Create a dedicated Docker **network** `gharp-s<N>`.
4. Apply **cgroup v2** caps to the user slice (`systemctl set-property
   user-<uid>.slice CPUQuota=… MemoryMax=… TasksMax=…`).
5. Install **nftables egress** rules for the slot: DROP to `169.254.169.254`,
   `169.254.169.253` (cloud metadata), and to other slots' subnets; allow
   general egress otherwise (or allowlist if hardening further).
6. Install a `systemd --user` unit `gharp.service` for that user that runs the
   gharp binary with the slot's env (`DOCKER_HOST`, `BASE_URL`,
   `GHARP_INSTANCE_ID=<slot-id>`, `ALLOW_PUBLIC_REPOS=false`, a per-slot
   `ADMIN_TOKEN` the Portal knows, `RUNNER_COMMAND` pinned to the slot network +
   resource caps + runner image >= v2.329.0).
7. Append the slot to `provisioner/slots.yaml` (the registry the Portal reads).

The Portal **reads** `slots.yaml` at startup (and on admin "reload slots") and
upserts rows. It never writes slots itself.

### Per-runner job isolation (inherited from gharp)
Each runner is `EPHEMERAL=1` (one job → container destroyed). Combined with the
slot's rootless daemon + network + cgroups + egress firewall, this gives both
per-job and per-tenant isolation. `RUNNER_COMMAND` must set the slot network,
`--memory`/`--cpus`/`--pids-limit`, and must NOT mount the host Docker socket.

### slots.yaml format (WS-C)
```yaml
slots:
  - id: slot-1
    os_user: gharp-s1
    uid: 1001
    docker_host: unix:///run/user/1001/docker.sock
    network: gharp-s1
    base_url: https://s1.gharp.example.com
    internal_addr: 127.0.0.1:9001
    cpu_limit: "2.0"
    mem_limit: "4g"
    max_runners: 4
```

## 9. Lifecycle & Reverse Proxy (WS-D)

### Lifecycle
- `Start(userID)`: resolve assignment → slot → invoke the allow-listed
  `start-gharp <slot-id>` (or `systemctl --user -M gharp-s<N>@ start gharp`),
  poll the slot's `internal_addr` `/healthz` until ready or timeout, set
  `gharp_state`.
- `Stop(userID)`: allow-listed `stop-gharp <slot-id>`; set state `stopped`.
- `Health()`: periodic `GET internal_addr/healthz` for assigned+running slots;
  set `error` on failure; expose on admin dashboard.
- Disabling a user → Stop their gharp.

### Reverse proxy
- Routes under `/app/*` (authenticated user only) → `httputil.ReverseProxy` to
  the user's `internal_addr`.
- Inject the slot's gharp `ADMIN_TOKEN` as `Authorization: Bearer` server-side so
  the user never sees or types it; strip any client-sent Authorization.
- Rewrite/strip the `/app` path prefix. Stream SSE/long-poll correctly
  (gharp polls every 10s; ensure no buffering breaks it).
- Block proxy access if the requesting user isn't the slot's owner.

## 10. HTTP API & Routes (WS-E)

Public:
- `GET /` → if session: redirect by role; else login page.
- `GET /login`, `GET /auth/start`, `GET /auth/callback`, `POST /auth/logout`.
- `GET /healthz` (portal self).

User (RequireUser):
- `GET /app` → proxy shell (user's gharp dashboard).
- `ANY /app/*` → reverse proxy.
- `POST /app/start`, `POST /app/stop` → lifecycle (own gharp).

Admin (RequireAdmin):
- `GET /admin` → user list + slot status + audit tail.
- `POST /admin/users` (invite: login, role).
- `POST /admin/users/{id}/status` (enable/disable).
- `POST /admin/users/{id}/assign` (auto free slot or explicit slot).
- `POST /admin/slots/reload` (re-read slots.yaml).
- `GET /admin/audit`.

All mutations require CSRF + RequireAdmin. JSON for XHR, server-rendered HTML
shells for pages.

## 11. UI (WS-E + WS-F)

Shared design system: CSS custom-property tokens defined once, reused across
Portal pages and the gharp dashboard redesign (dark mode default-to-system,
status color system, accessible, responsive). Source the visual direction from
the user's Claude-design mockup (to be exported/provided).

- **WS-E (Portal UI):** login page; admin console (users table with invite,
  enable/disable, assign-slot; slot status panel; audit tail); user proxy shell
  with a start/stop control + embedded gharp dashboard.
- **WS-F (gharp dashboard redesign):** reskin `dashboard.html`, `setup.html`,
  `setup_done.html` in `actions-runner-pool` per the new design, wired to the
  existing gharp endpoints (`/stats`, `/jobs`, `/jobs/{id}/retry|cancel`,
  `/admin/app-config`). No backend change to gharp. Add org/account grouping
  using `repo` owner; smooth polling diff updates; job detail drawer; empty
  states; read-only handling.

## 12. Security Requirements (must-hold)

1. Web process unprivileged; no arbitrary root; lifecycle uses one fixed
   allow-listed command with a validated slot-id argument only.
2. Runner App private keys never enter the Portal DB or process.
3. `ALLOW_PUBLIC_REPOS=false` enforced per slot; public-repo PR code never runs.
4. Ephemeral runners only; no host Docker socket in runner containers.
5. Per-slot egress firewall blocks cloud metadata + cross-slot traffic.
6. Sessions HttpOnly+Secure+SameSite; CSRF on all mutations; OAuth `state` param
   validated; tokens are CSPRNG.
7. Proxy enforces ownership; strips client Authorization; injects server token.
8. No secrets logged or rendered; admin actions audited.

## 13. Testing Strategy

- **TDD throughout** (red→green→refactor). Table-driven Go tests per package.
- WS-A: store CRUD, atomic `AssignFreeSlot` (concurrency test — no double
  assign), session expiry, invited→active binding.
- WS-B: OAuth callback (mock GitHub), state validation, gate logic
  (not-invited 403, bootstrap admin), session/CSRF middleware.
- WS-C: slots.yaml parse/validate, registry upsert, assignment selection;
  provisioner script: `shellcheck` + a dry-run/`--check` mode + idempotency
  assertions (no destructive re-run).
- WS-D: lifecycle state machine (mock command runner + mock health), reverse
  proxy (path strip, token injection, ownership block, streaming) via
  `httptest`.
- WS-E: handler tests (auth required, CSRF, role gating), template render
  smoke tests; Playwright e2e for login→admin→assign and user→start→proxy
  (mirroring gharp's existing Playwright setup).
- WS-F: Playwright dashboard tests adapted from gharp's `tests/dashboard.spec.ts`.
- CI mirrors gharp: `go test ./...`, `golangci-lint`, `codeql`, fuzz where apt
  (e.g. proxy path handling, OAuth state).

## 14. Phases

1. **Phase 1 — Portal MVP:** WS-A + WS-B + minimal WS-D (single manually
   configured slot) + minimal WS-E (login, admin list/invite/assign, proxy).
   One slot, end-to-end path proven.
2. **Phase 2 — Slot pool + provisioner:** WS-C full (provisioner scripts,
   slots.yaml, auto free-slot assignment, egress/cgroups), multi-user.
3. **Phase 3 — UI polish:** WS-E admin/console design + WS-F gharp dashboard
   redesign on the shared design system.
4. **Phase 4 — Future:** multiple Apps per user, quotas/metrics, self-invite.

## 15. Workstreams for the AI Team (parallelization map)

Each workstream owns **file-disjoint** packages to avoid collisions. WS-A
publishes the `store` interface + types FIRST (commit the interface stub early);
all others code against the contract in §6.

| WS | Name | Owns (paths) | Depends on | Done when |
|----|------|--------------|-----------|-----------|
| **A** | Core & Store | `gharp-portal/internal/config`, `internal/store`, `cmd/portal` scaffold, `go.mod` | — | store iface + sqlite impl + config load, all unit tests green; main boots and serves `/healthz` |
| **B** | Auth | `gharp-portal/internal/auth` | A (store iface) | OAuth login, sessions, CSRF, RBAC middleware, gate + bootstrap admin; tests green with mocked GitHub |
| **C** | Slots & Provisioner | `gharp-portal/internal/slots`, `provisioner/` | A (store iface) | slots.yaml loader+validate, registry upsert, `AssignFreeSlot` logic; `provision-slot.sh` + `slots.example.yaml`, shellcheck clean, `--check` idempotent |
| **D** | Lifecycle & Proxy | `gharp-portal/internal/lifecycle`, `internal/proxy` | A (store iface) | start/stop/health state machine (mock runner), reverse proxy with path-strip + token inject + ownership + streaming; httptest green |
| **E** | HTTP/UI & Wiring | `gharp-portal/internal/httpapi` (router, handlers, templates) | A,B,C,D | routes wired, admin+login+proxy-shell pages, handler+role+CSRF tests, Playwright login→admin→assign green |
| **F** | gharp Dashboard Redesign | `actions-runner-pool/internal/httpapi/handlers/templates/*` | design tokens (shared) | dashboard+setup pages reskinned, wired to existing gharp API, Playwright dashboard tests green; NO gharp backend change |

### Integration contract (anti-collision)
- WS-A commits `internal/store/store.go` (the `Store` interface + structs from
  §6) and `internal/config/config.go` (env keys from §7/§8) **before** heavy work,
  so B/C/D/E compile against stable signatures.
- Each WS works on its own git branch `ws-<letter>-<name>`; integration via PRs
  to a shared `integration` branch reviewed by the lead.
- Shared design tokens (a single `tokens.css`) authored once (WS-E) and copied
  into the gharp templates by WS-F — agree the variable names up front.
- No WS edits another WS's files. Cross-cutting needs go through the lead via the
  task list / SendMessage.

### Coordination protocol
- Shared task list (Task* tools) tracks each WS's subtasks and status.
- Daily-equivalent sync: each WS posts progress via TaskUpdate; blockers via
  SendMessage to the lead.
- Definition of done per WS: code + tests green + lint clean + short README note.

## 16. Risks & Mitigations

| Risk | Mitigation |
|------|-----------|
| Rootless Docker + linger flakiness on the host | Provisioner `--check` validates daemon reachable before marking slot ready |
| Reverse-proxy breaks gharp's 10s polling (buffering) | Disable proxy buffering; httptest streaming test |
| Privilege creep into web process | Hard rule §12.1; lifecycle limited to one allow-listed command; code review gate |
| Slot exhaustion (more users than slots) | Admin sees free/total; invite blocked when no free slot; operator adds slots |
| gharp version/runner < 2.329.0 blocked by GitHub (2026-03-16) | Provisioner pins runner image ≥ 2.329.0; documented |
| Two panes edit same file | File-disjoint workstreams + branch-per-WS + integration contract |

## 17. Glossary

- **gharp** — the upstream single-tenant runner-pool service (`actions-runner-pool`).
- **Portal** — the new multi-tenant control plane (this project).
- **Slot** — a pre-provisioned, kernel-isolated sandbox (OS user + rootless
  Docker + network + cgroup caps + egress firewall) that hosts one user's gharp.
- **Assignment** — the 1:1 binding of a user to a slot.
- **Provisioner** — operator-run, root scripts that create slots; never invoked
  by the web process.
```
