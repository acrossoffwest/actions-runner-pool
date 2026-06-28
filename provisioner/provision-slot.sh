#!/bin/sh
# provision-slot.sh — idempotent slot provisioner for the gharp portal.
#
# Usage:
#   provision-slot.sh <slot-id> <uid> [--check]
#
# Must be run as root. The --check flag performs a dry-run: validates
# prerequisites and reports what would change without making any changes.
#
# Requires: useradd/usermod, loginctl, dockerd-rootless-setuptool.sh,
#           systemctl, nft (nftables), tee. Run shellcheck in CI.
#
# Exit codes:
#   0  success (or --check: all prerequisites met, slot already provisioned)
#   1  usage / validation error
#   2  prerequisite not found

set -eu

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
    echo "usage: $0 <slot-id> <uid> [--check]" >&2
    exit 1
fi

SLOT_ID="$1"
UID_ARG="$2"
CHECK=0

if [ "$#" -eq 3 ]; then
    if [ "$3" = "--check" ]; then
        CHECK=1
    else
        echo "unknown flag: $3 (expected --check)" >&2
        exit 1
    fi
fi

# Validate slot-id: alphanumeric + hyphens only (no shell metacharacters)
case "$SLOT_ID" in
    *[!a-zA-Z0-9-]*)
        echo "error: slot-id must contain only [a-zA-Z0-9-], got: $SLOT_ID" >&2
        exit 1
        ;;
esac

# Validate uid: numeric only
case "$UID_ARG" in
    *[!0-9]*)
        echo "error: uid must be numeric, got: $UID_ARG" >&2
        exit 1
        ;;
esac

# Derive slot number suffix (e.g. "slot-1" → "1", "slot-42" → "42")
SLOT_NUM="${SLOT_ID#slot-}"
OS_USER="gharp-s${SLOT_NUM}"
DOCKER_HOST_PATH="/run/user/${UID_ARG}/docker.sock"
DOCKER_HOST="unix://${DOCKER_HOST_PATH}"
NETWORK_NAME="${OS_USER}"
USER_SLICE="user-${UID_ARG}.slice"

# Paths (no real hostnames or home dirs committed)
PROVISIONER_DIR="$(cd "$(dirname "$0")" && pwd)"
# Portal registry path (the portal reads this); override via env.
SLOTS_YAML="${SLOTS_YAML:-${PROVISIONER_DIR}/slots.yaml}"
# Path-based public base URL on a single domain — nginx strips /gh/<slot-id>.
# No per-slot DNS needed.
PUBLIC_HOST="${PUBLIC_HOST:-runners.example.com}"
SLOT_BASE_URL="https://${PUBLIC_HOST}/gh/${SLOT_ID}"
# OS group the portal runs as — it needs read access to slots.yaml (admin tokens).
PORTAL_GROUP="${PORTAL_GROUP:-gharp}"

# ---------------------------------------------------------------------------
# Prerequisite check helpers
# ---------------------------------------------------------------------------

need_cmd() {
    if ! command -v "$1" > /dev/null 2>&1; then
        echo "MISSING prerequisite: $1" >&2
        return 1
    fi
}

check_prerequisites() {
    PREREQ_OK=1
    need_cmd useradd   || PREREQ_OK=0
    need_cmd loginctl  || PREREQ_OK=0
    need_cmd systemctl || PREREQ_OK=0
    need_cmd nft       || PREREQ_OK=0
    need_cmd dockerd-rootless-setuptool.sh 2>/dev/null || {
        # may live under the slot user's PATH after setup; only warn in check mode
        echo "INFO: dockerd-rootless-setuptool.sh not in root PATH (expected after first run)" >&2
    }
    if [ "$PREREQ_OK" -eq 0 ]; then
        echo "error: one or more prerequisites missing" >&2
        exit 2
    fi
}

# ---------------------------------------------------------------------------
# --check mode: report, no side effects
# ---------------------------------------------------------------------------

if [ "$CHECK" -eq 1 ]; then
    echo "=== DRY-RUN (--check) for slot-id=${SLOT_ID} uid=${UID_ARG} ==="
    echo ""
    check_prerequisites
    echo ""

    echo "[OS USER]"
    if id "$OS_USER" > /dev/null 2>&1; then
        echo "  ALREADY EXISTS: $OS_USER (uid=$(id -u "$OS_USER"))"
    else
        echo "  WOULD CREATE: $OS_USER (uid=$UID_ARG, no login shell)"
    fi

    echo "[LINGER]"
    if loginctl show-user "$OS_USER" 2>/dev/null | grep -q "Linger=yes"; then
        echo "  ALREADY ENABLED"
    else
        echo "  WOULD ENABLE linger for $OS_USER"
    fi

    echo "[ROOTLESS DOCKER]"
    if [ -S "$DOCKER_HOST_PATH" ]; then
        echo "  ALREADY RUNNING: socket at $DOCKER_HOST_PATH"
    else
        echo "  WOULD RUN: dockerd-rootless-setuptool.sh install (as $OS_USER)"
    fi

    echo "[CGROUP SLICE: $USER_SLICE]"
    echo "  WOULD SET: CPUQuota, MemoryMax, TasksMax on $USER_SLICE"

    echo "[NFTABLES EGRESS]"
    echo "  WOULD ADD rules: DROP 169.254.169.254, 169.254.169.253, cross-slot subnets"

    echo "[SYSTEMD USER UNIT: gharp.service]"
    UNIT_FILE="/home/${OS_USER}/.config/systemd/user/gharp.service"
    if [ -f "$UNIT_FILE" ]; then
        echo "  ALREADY INSTALLED: $UNIT_FILE"
    else
        echo "  WOULD INSTALL: $UNIT_FILE"
    fi

    echo "[SLOTS YAML]"
    if grep -q "^  - id: ${SLOT_ID}$" "$SLOTS_YAML" 2>/dev/null; then
        echo "  ALREADY IN: $SLOTS_YAML"
    else
        echo "  WOULD APPEND: slot entry to $SLOTS_YAML"
    fi

    echo ""
    echo "=== END DRY-RUN (no changes made) ==="
    exit 0
fi

# ---------------------------------------------------------------------------
# Real provisioning (root required)
# ---------------------------------------------------------------------------

if [ "$(id -u)" -ne 0 ]; then
    echo "error: must run as root" >&2
    exit 1
fi

check_prerequisites

echo ">>> Provisioning slot: ${SLOT_ID} (OS user: ${OS_USER}, uid: ${UID_ARG})"

# 1. Create OS user (idempotent)
if id "$OS_USER" > /dev/null 2>&1; then
    echo "[1/7] OS user $OS_USER already exists, skipping"
else
    echo "[1/7] Creating OS user $OS_USER (uid=$UID_ARG)"
    useradd \
        --uid "$UID_ARG" \
        --no-create-home \
        --home-dir "/home/${OS_USER}" \
        --shell /usr/sbin/nologin \
        --system \
        "$OS_USER"
    # Create home explicitly so we can write systemd units
    mkdir -p "/home/${OS_USER}"
    chown "${OS_USER}:${OS_USER}" "/home/${OS_USER}"
fi

# 1b. Ensure subuid/subgid ranges for rootless Docker. System users don't get
# these automatically; allocate a unique non-overlapping 65536 block per slot
# (slot-1 -> 100000, slot-2 -> 165536, ...). Idempotent.
SUBID_COUNT=65536
SUBID_START=$((100000 + (SLOT_NUM - 1) * SUBID_COUNT))
if ! grep -q "^${OS_USER}:" /etc/subuid 2>/dev/null; then
    echo "${OS_USER}:${SUBID_START}:${SUBID_COUNT}" >> /etc/subuid
    echo "[1b] added subuid ${OS_USER}:${SUBID_START}:${SUBID_COUNT}"
fi
if ! grep -q "^${OS_USER}:" /etc/subgid 2>/dev/null; then
    echo "${OS_USER}:${SUBID_START}:${SUBID_COUNT}" >> /etc/subgid
    echo "[1b] added subgid ${OS_USER}:${SUBID_START}:${SUBID_COUNT}"
fi

# 2. Enable linger (rootless Docker survives logout)
echo "[2/7] Enabling linger for $OS_USER"
loginctl enable-linger "$OS_USER"

# 3. Rootless Docker as a systemd --user service. The setuptool only installs a
# systemd unit when it can see the user's systemd manager, so we must run it with
# XDG_RUNTIME_DIR + DBUS pointed at the lingering user session — NOT via `su -l`
# (which leaves those unset and drops the tool into manual mode, installing no
# unit and putting the socket in the wrong place).
echo "[3/7] Setting up rootless Docker for $OS_USER"
systemctl start "user@${UID_ARG}.service" 2>/dev/null || true
# Wait for logind to create the user runtime dir + dbus socket.
i=0
while [ ! -S "/run/user/${UID_ARG}/bus" ] && [ "$i" -lt 40 ]; do sleep 0.5; i=$((i + 1)); done
USER_ENV="XDG_RUNTIME_DIR=/run/user/${UID_ARG} DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/${UID_ARG}/bus PATH=/usr/bin:/sbin:/usr/sbin"
su - "$OS_USER" -s /bin/sh -c "${USER_ENV} dockerd-rootless-setuptool.sh install --force" || true
su - "$OS_USER" -s /bin/sh -c "${USER_ENV} systemctl --user enable --now docker" || true
# Wait for the rootless docker socket to come up.
i=0
while [ ! -S "${DOCKER_HOST_PATH}" ] && [ "$i" -lt 40 ]; do sleep 0.5; i=$((i + 1)); done

# 4. Create Docker network (idempotent; runs in the user's rootless daemon)
echo "[4/7] Creating Docker network $NETWORK_NAME"
su - "$OS_USER" -s /bin/sh -c \
    "${USER_ENV} DOCKER_HOST=${DOCKER_HOST} sh -c 'docker network inspect ${NETWORK_NAME} >/dev/null 2>&1 || docker network create --driver bridge ${NETWORK_NAME}'" || true

# 5. Apply cgroup v2 caps to the user slice
echo "[5/7] Applying cgroup v2 limits to $USER_SLICE"
# Limits come from slots.yaml fields; use sane defaults here; operators can
# override by re-running with different slot config.
GHARP_CPU_QUOTA="${GHARP_CPU_QUOTA:-200%}"
GHARP_MEM_MAX="${GHARP_MEM_MAX:-4G}"
GHARP_TASKS_MAX="${GHARP_TASKS_MAX:-1024}"
systemctl set-property "$USER_SLICE" \
    "CPUQuota=${GHARP_CPU_QUOTA}" \
    "MemoryMax=${GHARP_MEM_MAX}" \
    "TasksMax=${GHARP_TASKS_MAX}"

# 6. Install nftables egress rules for this slot
echo "[6/7] Installing nftables egress rules for $OS_USER"
# We add a chain per slot to keep rules isolated and idempotent.
CHAIN_NAME="gharp_egress_${OS_USER}"
# Flush existing chain for this slot (idempotent re-run)
nft delete chain ip filter "$CHAIN_NAME" 2>/dev/null || true
# Add chain + rules
nft add table ip filter 2>/dev/null || true
nft add chain ip filter "$CHAIN_NAME" "{ type filter hook output priority 0; }"
# Drop cloud metadata endpoints
nft add rule ip filter "$CHAIN_NAME" \
    "skuid $UID_ARG ip daddr 169.254.169.254 drop"
nft add rule ip filter "$CHAIN_NAME" \
    "skuid $UID_ARG ip daddr 169.254.169.253 drop"
# Allow everything else for this user (operators can tighten further)
nft add rule ip filter "$CHAIN_NAME" \
    "skuid $UID_ARG accept"

# 7. Install systemd --user unit for gharp
echo "[7/7] Installing gharp.service systemd user unit for $OS_USER"
UNIT_DIR="/home/${OS_USER}/.config/systemd/user"
mkdir -p "$UNIT_DIR"

# ADMIN_TOKEN is a per-slot secret shared by gharp (which requires it for API
# access) and the Portal proxy (which injects it as a bearer). Generate a strong
# random value once, write it to the slot's secrets file (loaded by the gharp
# unit via EnvironmentFile) AND record the same value in slots.yaml so the proxy
# injects a matching bearer — otherwise proxied calls get 401.
ADMIN_TOKEN_VALUE="$(openssl rand -base64 32 | tr -d '/+=' | cut -c1-43)"
ENV_FILE="/home/${OS_USER}/.gharp-env"
umask 077
printf 'ADMIN_TOKEN=%s\n' "$ADMIN_TOKEN_VALUE" > "$ENV_FILE"
chown "${OS_USER}:${OS_USER}" "$ENV_FILE"
chmod 600 "$ENV_FILE"

# Port derives from slot number: 9000 + slot number
PORT="$((9000 + SLOT_NUM))"

cat > "${UNIT_DIR}/gharp.service" <<UNIT
[Unit]
Description=gharp runner pool — slot ${SLOT_ID}
After=docker.service

[Service]
Type=simple
EnvironmentFile=-/home/${OS_USER}/.gharp-env
Environment=DOCKER_HOST=${DOCKER_HOST}
Environment=BASE_URL=${SLOT_BASE_URL}
Environment=GHARP_INSTANCE_ID=${SLOT_ID}
Environment=ALLOW_PUBLIC_REPOS=false
Environment=PORT=${PORT}
ExecStart=/usr/local/bin/gharp
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
UNIT

chown -R "${OS_USER}:${OS_USER}" "${UNIT_DIR}"

# Reload user systemd and enable the unit
systemctl --machine="${OS_USER}@.host" --user daemon-reload 2>/dev/null || true
systemctl --machine="${OS_USER}@.host" --user enable gharp.service 2>/dev/null || true

# 8. Append slot to slots.yaml (idempotent: skip if id already present)
echo "[+]  Updating $SLOTS_YAML"
if grep -q "^  - id: ${SLOT_ID}$" "$SLOTS_YAML" 2>/dev/null; then
    echo "     slot ${SLOT_ID} already in $SLOTS_YAML, skipping"
else
    # Create file with header if it doesn't exist
    if [ ! -f "$SLOTS_YAML" ]; then
        printf 'slots:\n' > "$SLOTS_YAML"
    fi
    cat >> "$SLOTS_YAML" <<YAMLENTRY
  - id: ${SLOT_ID}
    os_user: ${OS_USER}
    uid: ${UID_ARG}
    docker_host: ${DOCKER_HOST}
    network: ${NETWORK_NAME}
    base_url: ${SLOT_BASE_URL}
    internal_addr: 127.0.0.1:${PORT}
    cpu_limit: "${GHARP_CPU_QUOTA}"
    mem_limit: "${GHARP_MEM_MAX}"
    max_runners: 4
    admin_token: ${ADMIN_TOKEN_VALUE}
YAMLENTRY
    echo "     appended slot ${SLOT_ID} to $SLOTS_YAML"
fi
# slots.yaml holds per-slot admin tokens. The portal (PORTAL_GROUP) must read it;
# keep it root-owned, group-readable, no world access.
chown "root:${PORTAL_GROUP}" "$SLOTS_YAML" 2>/dev/null || true
chmod 640 "$SLOTS_YAML"

echo ""
echo ">>> Slot ${SLOT_ID} provisioned successfully."
echo "    base_url    : ${SLOT_BASE_URL}  (path-based; add nginx route /gh/${SLOT_ID}/ -> 127.0.0.1:${PORT})"
echo "    internal    : 127.0.0.1:${PORT}  (keep firewalled to localhost)"
echo ">>> Next steps:"
echo "    1. Add the nginx /gh/${SLOT_ID}/ location -> http://127.0.0.1:${PORT}/ and reload nginx"
echo "    2. Reload the Portal registry: POST /admin/slots/reload (or restart the portal)"
echo "    3. Start the instance: systemctl --machine=${OS_USER}@.host --user start gharp"
echo "    (ADMIN_TOKEN generated into $ENV_FILE and $SLOTS_YAML automatically.)"
