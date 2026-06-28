# Host prep (rootful-shared slots)

For hosts where rootless Docker / Sysbox do not work (bleeding-edge kernels).
Run as root, once per host, then `provision-slot-rootful.sh <slot-id> <uid>` per slot.

1. Install `/usr/local/sbin/gharp-svc` (root:root 0755) — the validating wrapper.
2. Install `sudoers.gharp-portal` to `/etc/sudoers.d/gharp-portal` (0440) — lets the
   unprivileged portal user start/stop ONLY the slot units via the wrapper.
3. Portal calls `start-gharp <slot>` / `stop-gharp <slot>` (thin wrappers that
   `sudo -n gharp-svc ...`).
4. Firewall the slot ports (9000-9099) to loopback and apply per-slot egress via a
   boot service (`gharp-nft.service` -> `gharp-nft-apply.sh`).

Isolation: per-slot OS user + Docker network + cgroup caps + nftables egress on a
SHARED rootful daemon. Mutually-trusted tenants only; for an untrusted boundary use
an LTS kernel + rootless Docker (`provision-slot.sh`).
