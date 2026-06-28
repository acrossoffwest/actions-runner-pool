# gharp Portal — Slot Provisioner

Operator-run scripts that create and configure isolated sandbox slots. Each slot hosts exactly one user's gharp runner-pool instance. These scripts run as **root**, outside the web process, and are never invoked by the Portal itself.

## Prerequisites

Install these on the host before provisioning:

| Tool | Purpose |
|------|---------|
| `useradd` | Create the slot OS user |
| `loginctl` (systemd-logind) | Enable user linger |
| `systemctl` | Apply cgroup limits; manage user units |
| `dockerd-rootless-setuptool.sh` | Set up rootless Docker per slot user |
| `nft` (nftables) | Egress firewall rules |

> **Run shellcheck in CI** — shellcheck is not installed on the provisioning host by default. Add `shellcheck provisioner/provision-slot.sh` to your CI pipeline.

## Scripts

### `provision-slot.sh <slot-id> <uid> [--check]`

Idempotently provisions a slot. Re-running is safe; each step detects whether it is already done.

**Arguments:**

| Arg | Example | Description |
|-----|---------|-------------|
| `<slot-id>` | `slot-1` | Alphanumeric + hyphens. Used to derive OS username (`gharp-s<N>`), Docker network name, and port. |
| `<uid>` | `1001` | Numeric OS UID for the slot user. Must be unused before first run. |
| `--check` | | Dry-run. Reports what would change. Zero side effects. Safe to run repeatedly. |

**What it does (in order):**

1. **OS user** — `useradd` with `--shell /usr/sbin/nologin`. Home at `/home/gharp-s<N>`.
2. **Linger** — `loginctl enable-linger` so rootless services survive logout.
3. **Rootless Docker** — `dockerd-rootless-setuptool.sh install` run as the slot user. Socket at `/run/user/<uid>/docker.sock`.
4. **Docker network** — `docker network create --driver bridge gharp-s<N>` (idempotent).
5. **Cgroup v2 caps** — `systemctl set-property user-<uid>.slice CPUQuota=… MemoryMax=… TasksMax=…`.
6. **nftables egress** — Per-slot chain. DROPs: `169.254.169.254`, `169.254.169.253` (cloud metadata). Cross-slot traffic should be isolated at the network level; tighten with subnet DROP rules matching other slots if needed.
7. **systemd user unit** — Installs `~/.config/systemd/user/gharp.service` for the slot user with all required env vars (see below). Runner image pinned to `>=v2.329.0`. No host Docker socket mounted.
8. **slots.yaml** — Appends the new slot entry to `provisioner/slots.yaml`. Skips if slot-id already present.

**Environment variables (cgroup limits):**

Override before running to tune per-slot limits:

```sh
export GHARP_CPU_QUOTA="200%"   # default
export GHARP_MEM_MAX="4G"       # default
export GHARP_TASKS_MAX="1024"   # default
./provision-slot.sh slot-1 1001
```

## Operator Workflow

```sh
# 1. Verify prerequisites
./provision-slot.sh slot-1 1001 --check

# 2. Provision (as root)
sudo ./provision-slot.sh slot-1 1001

# 3. Set the real ADMIN_TOKEN (never hardcode; use a secrets manager)
#    Edit /home/gharp-s1/.gharp-env:
#      ADMIN_TOKEN=<generated-secret>
#    The Portal reads this token from config; slots.yaml never holds secrets.

# 4. Update base_url in provisioner/slots.yaml to the real public HTTPS URL for
#    that slot's gharp webhook endpoint.

# 5. Reload the Portal slot registry
curl -X POST https://<portal>/admin/slots/reload \
    -H "Authorization: Bearer <admin-session>" \
    --data csrf=<token>
```

## slots.yaml

`slots.yaml` is the registry the Portal reads at startup and on admin reload. The provisioner appends entries automatically.

Copy `slots.example.yaml` as a reference:

```sh
cp slots.example.yaml slots.yaml
```

Fields:

| Field | Required | Description |
|-------|----------|-------------|
| `id` | yes | Unique slot identifier (e.g. `slot-1`) |
| `os_user` | yes | OS username (e.g. `gharp-s1`) |
| `uid` | yes | Numeric OS UID |
| `docker_host` | yes | Rootless Docker socket (`unix://…` or `tcp://…`) |
| `network` | yes | Docker network name |
| `base_url` | yes | Public HTTPS URL for this slot's gharp (webhook target) |
| `internal_addr` | yes | `host:port` the Portal proxies to (loopback only) |
| `cpu_limit` | no | Informational mirror of the cgroup CPUQuota |
| `mem_limit` | no | Informational mirror of MemoryMax |
| `max_runners` | no | Concurrent runner cap (default 4) |

## Security Notes

- The web process **never** invokes this script. Lifecycle management uses a narrow allow-listed command (`start-gharp <slot-id>` / `stop-gharp <slot-id>`).
- `ALLOW_PUBLIC_REPOS=false` is enforced in the systemd unit env; public-repo PR code never runs.
- Runner containers are ephemeral (one job → container destroyed).
- Host Docker socket is **not** mounted inside runner containers.
- Egress firewall blocks cloud metadata endpoints (`169.254.169.254`, `169.254.169.253`).
- `ADMIN_TOKEN` is a per-slot secret stored in `/home/<os-user>/.gharp-env` (mode 0600, owned by slot user). It is never stored in `slots.yaml` or the Portal DB.
- Runner image must be `>= v2.329.0` (GitHub's minimum since 2026-03-16).

## Idempotency

Every step checks current state before acting. Re-running `provision-slot.sh` with the same arguments is safe. Use `--check` to verify the expected state without making changes.

## Path Conventions

All paths in this script use variables derived from `<slot-id>` and `<uid>`. No hardcoded home directories, usernames, or IP addresses appear in tracked files. Use placeholders in any documentation you commit.
