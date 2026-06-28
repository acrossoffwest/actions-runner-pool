#!/bin/sh
# provision-slot-rootful.sh — slot provisioner using the host's ROOTFUL Docker.
#
# Use this on hosts where rootless Docker / Sysbox don't work (e.g. bleeding-edge
# kernels). Isolation here is per-slot: dedicated OS user, dedicated Docker
# network, per-runner CPU/memory/pids caps, and nftables egress — on the SHARED
# rootful daemon. Weaker than rootless (shared daemon), fine for mutually-trusted
# tenants. For an untrusted boundary, reimage to an LTS kernel + use rootless.
#
# Usage: provision-slot-rootful.sh <slot-id> <uid>   (run as root)
#
# Env overrides: SLOTS_YAML, PUBLIC_HOST, PORTAL_GROUP, RUNNER_IMAGE,
#                GHARP_MEM_MAX, GHARP_CPU_QUOTA, RUNNER_MEM, RUNNER_CPUS,
#                GHARP_MAX_RUNNERS (max concurrent runner containers; default 4).

set -eu

if [ "$#" -ne 2 ]; then
    echo "usage: $0 <slot-id> <uid>" >&2
    exit 1
fi

SLOT_ID="$1"
UID_ARG="$2"

case "$SLOT_ID" in *[!a-zA-Z0-9-]*) echo "error: bad slot-id" >&2; exit 1 ;; esac
case "$UID_ARG" in *[!0-9]*) echo "error: uid must be numeric" >&2; exit 1 ;; esac

if [ "$(id -u)" -ne 0 ]; then echo "error: must run as root" >&2; exit 1; fi

SLOT_NUM="${SLOT_ID#slot-}"
OS_USER="gharp-s${SLOT_NUM}"
NETWORK_NAME="${OS_USER}"
PORT="$((9000 + SLOT_NUM))"

PROVISIONER_DIR="$(cd "$(dirname "$0")" && pwd)"
SLOTS_YAML="${SLOTS_YAML:-${PROVISIONER_DIR}/slots.yaml}"
PUBLIC_HOST="${PUBLIC_HOST:-runners.example.com}"
SLOT_BASE_URL="https://${PUBLIC_HOST}/gh/${SLOT_ID}"
PORTAL_GROUP="${PORTAL_GROUP:-gharp}"
RUNNER_IMAGE="${RUNNER_IMAGE:-myoung34/github-runner:latest}"
GHARP_MEM_MAX="${GHARP_MEM_MAX:-4G}"
GHARP_CPU_QUOTA="${GHARP_CPU_QUOTA:-200%}"
RUNNER_MEM="${RUNNER_MEM:-2g}"
RUNNER_CPUS="${RUNNER_CPUS:-1.0}"
GHARP_MAX_RUNNERS="${GHARP_MAX_RUNNERS:-4}"

echo ">>> Provisioning (rootful) slot ${SLOT_ID} — user ${OS_USER}, port ${PORT}"

# 1. OS user, in the docker group (access to the shared rootful daemon).
if id "$OS_USER" >/dev/null 2>&1; then
    echo "[1/6] user $OS_USER exists"
else
    echo "[1/6] creating user $OS_USER (uid ${UID_ARG})"
    # Force the uid: the nftables egress chain below filters by skuid
    # ${UID_ARG}, so the OS user MUST own exactly that uid or the
    # cloud-metadata block would target the wrong account.
    useradd --system --uid "$UID_ARG" --no-create-home --home-dir "/home/${OS_USER}" \
        --shell /usr/sbin/nologin "$OS_USER"
    mkdir -p "/home/${OS_USER}"; chown "${OS_USER}:${OS_USER}" "/home/${OS_USER}"
fi
usermod -aG docker "$OS_USER"

# 2. Dedicated docker network for this slot's runner containers.
echo "[2/6] docker network $NETWORK_NAME"
docker network inspect "$NETWORK_NAME" >/dev/null 2>&1 || \
    docker network create --driver bridge "$NETWORK_NAME" >/dev/null

# 3. Secrets + runner command (per-slot network + caps + gharp placeholders).
echo "[3/6] env file + admin token"
ADMIN_TOKEN_VALUE="$(openssl rand -base64 32 | tr -d '/+=' | cut -c1-43)"
ENV_FILE="/home/${OS_USER}/.gharp-env"
umask 077
{
    printf 'ADMIN_TOKEN=%s\n' "$ADMIN_TOKEN_VALUE"
    printf 'RUNNER_COMMAND=["docker","run","--rm","--network","%s","--memory","%s","--cpus","%s","--pids-limit","512","--name","{{.ContainerName}}","-e","REPO_URL={{.RepoURL}}","-e","RUNNER_TOKEN={{.RegistrationToken}}","-e","RUNNER_NAME={{.RunnerName}}","-e","LABELS={{.Labels}}","-e","EPHEMERAL=1","{{.Image}}"]\n' \
        "$NETWORK_NAME" "$RUNNER_MEM" "$RUNNER_CPUS"
} > "$ENV_FILE"
chown "${OS_USER}:${OS_USER}" "$ENV_FILE"; chmod 600 "$ENV_FILE"

# 4. nftables egress: block cloud metadata for this slot user.
echo "[4/6] nftables egress"
CHAIN="gharp_egress_${OS_USER}"
nft delete chain ip filter "$CHAIN" 2>/dev/null || true
nft add table ip filter 2>/dev/null || true
nft add chain ip filter "$CHAIN" "{ type filter hook output priority 0; }"
nft add rule ip filter "$CHAIN" "skuid ${UID_ARG} ip daddr 169.254.169.254 drop"
nft add rule ip filter "$CHAIN" "skuid ${UID_ARG} ip daddr 169.254.169.253 drop"

# 5. systemd system service for the slot's gharp (talks to the rootful daemon).
echo "[5/6] systemd unit gharp-${SLOT_ID}.service"
cat > "/etc/systemd/system/gharp-${SLOT_ID}.service" <<UNIT
[Unit]
Description=gharp runner pool (rootful slot ${SLOT_ID})
After=docker.service
Requires=docker.service

[Service]
User=${OS_USER}
Group=${OS_USER}
SupplementaryGroups=docker
EnvironmentFile=${ENV_FILE}
Environment=DOCKER_HOST=unix:///var/run/docker.sock
Environment=BASE_URL=${SLOT_BASE_URL}
Environment=GHARP_INSTANCE_ID=${SLOT_ID}
Environment=ALLOW_PUBLIC_REPOS=false
# Admin writes (retry/cancel/credential rotation/notifications) are enabled:
# in the portal model each slot is owned by exactly one tenant and the portal
# proxy enforces ownership + injects this instance's ADMIN_TOKEN as the bearer,
# so "admin" here means the slot owner managing their own pool.
Environment=ALLOW_ADMIN_EDIT=true
Environment=MAX_CONCURRENT_RUNNERS=${GHARP_MAX_RUNNERS}
Environment=PORT=${PORT}
Environment=STORE_DSN=file:/home/${OS_USER}/gharp.db
Environment=RUNNER_IMAGE=${RUNNER_IMAGE}
ExecStart=/usr/local/bin/gharp
Restart=on-failure
RestartSec=5
# Per-slot resource caps on the gharp process (runner containers are capped via
# RUNNER_COMMAND --memory/--cpus since they run under the shared daemon).
MemoryMax=${GHARP_MEM_MAX}
CPUQuota=${GHARP_CPU_QUOTA}
WorkingDirectory=/home/${OS_USER}

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable "gharp-${SLOT_ID}.service" >/dev/null 2>&1 || true

# 6. Register the slot in the portal registry.
echo "[6/6] slots.yaml"
if ! grep -q "^  - id: ${SLOT_ID}$" "$SLOTS_YAML" 2>/dev/null; then
    [ -f "$SLOTS_YAML" ] || printf 'slots:\n' > "$SLOTS_YAML"
    cat >> "$SLOTS_YAML" <<YAMLENTRY
  - id: ${SLOT_ID}
    os_user: ${OS_USER}
    uid: ${UID_ARG}
    docker_host: unix:///var/run/docker.sock
    network: ${NETWORK_NAME}
    base_url: ${SLOT_BASE_URL}
    internal_addr: 127.0.0.1:${PORT}
    cpu_limit: "${GHARP_CPU_QUOTA}"
    mem_limit: "${GHARP_MEM_MAX}"
    max_runners: ${GHARP_MAX_RUNNERS}
    admin_token: ${ADMIN_TOKEN_VALUE}
YAMLENTRY
fi
chown "root:${PORTAL_GROUP}" "$SLOTS_YAML" 2>/dev/null || true
chmod 640 "$SLOTS_YAML"

echo ""
echo ">>> slot ${SLOT_ID} provisioned (rootful)."
echo "    base_url : ${SLOT_BASE_URL}  -> nginx /gh/${SLOT_ID}/ -> 127.0.0.1:${PORT}"
echo "    next: add nginx route, reload portal registry, start: systemctl start gharp-${SLOT_ID}"
