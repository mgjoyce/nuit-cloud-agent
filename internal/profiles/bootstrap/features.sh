# Features for bootstrap.sh. Each is a pair: f_<name>_ok (exit 0 if already satisfied, changing nothing) and
# f_<name>_do (make it so; exit 3 = "a person has to do this", with a message). Profiles may set the defaults below.
LOG_MAX_SIZE="${LOG_MAX_SIZE:-10m}"
LOG_MAX_FILE="${LOG_MAX_FILE:-3}"
NGINX_DIR="${NGINX_DIR:-/home/$NC_USER/nginx}"
JOURNAL_MAX="${JOURNAL_MAX:-50M}"

# The user the platform logs in as over SSH. Creating it (and installing the platform's key) is deliberately by hand.
f_user_ok() { id "$NC_USER" >/dev/null 2>&1; }
f_user_do() { echo "  create $NC_USER and install the platform's SSH key (ssh-copy-id) first, then run this again" >&2; return 3; }

f_docker_ok() { command -v docker >/dev/null 2>&1 && systemctl is-enabled --quiet docker 2>/dev/null; }
f_docker_do() {
  if ! command -v docker >/dev/null 2>&1; then
    command -v curl >/dev/null 2>&1 || { apt-get update -qq && apt-get install -y -qq curl ca-certificates; }
    curl -fsSL https://get.docker.com | sh
  fi
  systemctl enable --now docker
}

# The platform drives `docker` as the user: the docker group and nothing else (no sudo).
f_docker_group_ok() { id -nG "$NC_USER" 2>/dev/null | tr ' ' '\n' | grep -qx docker; }
f_docker_group_do() { usermod -aG docker "$NC_USER"; }

# Cap every container's log; one chatty service filling the disk is the likeliest outage. Docker applies it only to
# containers created afterwards, so nuit-nginx (stateless: bind mounts) is removed here and recreated by the nginx feature.
f_docker_logs_ok() { grep -q '"max-size"' "$(p /etc/docker/daemon.json)" 2>/dev/null; }
f_docker_logs_do() {
  if [ -e "$(p /etc/docker/daemon.json)" ]; then echo "  /etc/docker/daemon.json exists without a log cap; left alone — add log-opts max-size/max-file yourself" >&2; return 3; fi
  write_if_missing /etc/docker/daemon.json <<JSON
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "$LOG_MAX_SIZE", "max-file": "$LOG_MAX_FILE" }
}
JSON
  systemctl restart docker
  docker rm -f nuit-nginx >/dev/null 2>&1 || true
}

f_updates_ok() { dpkg -s unattended-upgrades >/dev/null 2>&1 && [ -e "$(p /etc/apt/apt.conf.d/20auto-upgrades)" ]; }
f_updates_do() {
  dpkg -s unattended-upgrades >/dev/null 2>&1 || { apt-get update -qq && apt-get install -y -qq unattended-upgrades; }
  printf 'APT::Periodic::Update-Package-Lists "1";\nAPT::Periodic::Unattended-Upgrade "1";\n' | write_if_missing /etc/apt/apt.conf.d/20auto-upgrades
}

# Certificate checks and token expiry depend on the clock.
f_timesync_ok() { systemctl is-active --quiet systemd-timesyncd || systemctl is-active --quiet chrony || systemctl is-active --quiet ntpsec; }
f_timesync_do() { apt-get install -y -qq systemd-timesyncd; timedatectl set-ntp true 2>/dev/null || true; }

# Unused images cleared weekly by a systemd timer (a minimal Debian has no cron). Running containers' images are kept.
f_prune_ok() { [ -e "$(p /etc/systemd/system/nuit-docker-prune.timer)" ]; }
f_prune_do() {
  write_if_missing /etc/systemd/system/nuit-docker-prune.service <<'UNIT'
[Unit]
Description=Remove unused Docker images older than a week

[Service]
Type=oneshot
ExecStart=/usr/bin/docker image prune --all --force --filter until=168h
UNIT
  write_if_missing /etc/systemd/system/nuit-docker-prune.timer <<'UNIT'
[Unit]
Description=Weekly removal of unused Docker images

[Timer]
OnCalendar=weekly
Persistent=true

[Install]
WantedBy=timers.target
UNIT
  systemctl daemon-reload
  systemctl enable --now nuit-docker-prune.timer
}

# nginx in Docker on 80/443, config and certificates managed by the platform. certs is mounted at the SAME path inside
# the container because the platform writes host paths into the nginx config.
f_nginx_ok() { docker inspect nuit-nginx >/dev/null 2>&1; }
f_nginx_do() {
  install -d -o "$NC_USER" -g "$NC_USER" "$NGINX_DIR" "$NGINX_DIR/conf.d" "$NGINX_DIR/certs"
  docker run -d --name nuit-nginx -p 80:80 -p 443:443 \
    -v "$NGINX_DIR/conf.d:/etc/nginx/conf.d" -v "$NGINX_DIR/certs:$NGINX_DIR/certs" \
    --restart unless-stopped nginx:alpine
}

# ---- Raspberry Pi ----------------------------------------------------------------------------------------------

# Boot files moved to /boot/firmware in Bookworm.
boot_dir() { if [ -e "$(p /boot/firmware/cmdline.txt)" ]; then p /boot/firmware; else p /boot; fi; }

# 64-bit OS: many images (Pi-hole, Unbound, Technitium, the dyndns probe) are arm64-first, and a 32-bit userland is
# no help to the Pi 3's 1 GB. The OS cannot be switched in place, so this is for a person.
f_pi_arch_ok() { [ "$(uname -m)" = aarch64 ]; }
f_pi_arch_do() { echo "  this is a $(uname -m) system; reinstall Raspberry Pi OS Lite (64-bit) — it cannot be switched in place" >&2; return 3; }

# Docker's memory limits (a service's limits.memoryMb) need the memory cgroup, off by default on Raspberry Pi OS.
f_pi_cgroup_ok() { grep -q 'cgroup_enable=memory' "$(boot_dir)/cmdline.txt" 2>/dev/null && grep -q 'cgroup_memory=1' "$(boot_dir)/cmdline.txt"; }
f_pi_cgroup_do() {
  f="$(boot_dir)/cmdline.txt"; [ -e "$f" ] || { echo "  no cmdline.txt found" >&2; return 1; }
  rewrite "$f" sh -c 'printf "%s cgroup_enable=memory cgroup_memory=1\n" "$(tr -d "\n" <"$1")"' _ "$f"
  REBOOT=1
}

# Headless: give the GPU the minimum so the 1 GB goes to containers.
f_pi_gpumem_ok() { grep -q '^gpu_mem=' "$(boot_dir)/config.txt" 2>/dev/null; }
f_pi_gpumem_do() {
  f="$(boot_dir)/config.txt"; [ -e "$f" ] || { echo "  no config.txt found" >&2; return 1; }
  rewrite "$f" sh -c 'cat "$1"; printf "\n# nuit-cloud: headless\ngpu_mem=16\n"' _ "$f"
  REBOOT=1
}

# Swap in compressed RAM, not on the SD card (slow, and swapping wears the card out). Replaces the SD swap file.
f_pi_zram_ok() { dpkg -s zram-tools >/dev/null 2>&1 && ! systemctl is-enabled --quiet dphys-swapfile 2>/dev/null; }
f_pi_zram_do() {
  dpkg -s zram-tools >/dev/null 2>&1 || { apt-get update -qq && apt-get install -y -qq zram-tools; }
  printf 'ALGO=zstd\nPERCENT=50\n' | write_if_missing /etc/default/zramswap
  systemctl disable --now dphys-swapfile 2>/dev/null || true
  systemctl enable --now zramswap
}

# Fewer SD-card writes and a bounded journal.
f_pi_journal_ok() { [ -e "$(p /etc/systemd/journald.conf.d/nuit-cloud.conf)" ]; }
f_pi_journal_do() {
  printf '[Journal]\nSystemMaxUse=%s\nSyncIntervalSec=5m\n' "$JOURNAL_MAX" | write_if_missing /etc/systemd/journald.conf.d/nuit-cloud.conf
  systemctl restart systemd-journald
}

# A hung unattended Pi reboots itself (the SoC has a hardware watchdog).
f_pi_watchdog_ok() { [ -e "$(p /etc/systemd/system.conf.d/nuit-watchdog.conf)" ]; }
f_pi_watchdog_do() {
  printf '[Manager]\nRuntimeWatchdogSec=15\nRebootWatchdogSec=2min\n' | write_if_missing /etc/systemd/system.conf.d/nuit-watchdog.conf
  systemctl daemon-reexec
}

summary() {
  echo "Clock synchronised: $(timedatectl show -p NTPSynchronized --value 2>/dev/null || echo unknown)"
  echo "Still to do by hand: open 80 and 443 to the internet if this serves routes (firewall / router / cloud security group)."
  if command -v vcgencmd >/dev/null 2>&1; then echo "Power/thermal flags (0x0 is healthy): $(vcgencmd get_throttled 2>/dev/null)"; fi
}

# ─── Nuit Cloud Server (a machine the platform controls; see CONTEXT.md and ADR 0001) ──────────────────────────────────
# These are conservative on purpose: anything that could lock a person out of the machine (SSH settings) or needs a person
# at the hardware (a cable, the BIOS) is reported MANUAL and never changed behind their back.

# What the agent and the runtime need on a minimal Debian: a way to fetch things over TLS, and the network tools.
f_ncs_packages_ok() { dpkg -s ca-certificates >/dev/null 2>&1 && dpkg -s curl >/dev/null 2>&1 && dpkg -s iproute2 >/dev/null 2>&1 && dpkg -s iputils-arping >/dev/null 2>&1; }
f_ncs_packages_do() { apt-get update -qq && apt-get install -y -qq ca-certificates curl iproute2 iputils-arping; }

# Wired by default: at least one physical, non-wireless interface that is up and has an address. A cable is a person's job.
f_ncs_wired_ok() {
  for d in "$(p /sys/class/net)"/*; do
    [ -e "$d" ] || continue
    n="$(basename "$d")"
    case "$n" in lo) continue ;; esac
    [ -e "$d/wireless" ] || [ -e "$d/phy80211" ] && continue          # wireless
    [ -L "$d" ] && case "$(readlink "$d")" in *virtual*) continue ;; esac   # bridges, veths, tunnels
    [ "$(cat "$d/operstate" 2>/dev/null)" = up ] && return 0
  done
  return 1
}
f_ncs_wired_do() { echo "  no wired connection is up: connect a cable (or record a decision to allow wireless in the portal), then run this again" >&2; return 3; }

# Root over SSH with a password, or password logins at all, are left to a person: changing sshd before key login is proven
# can lock everyone out. Satisfied once neither is allowed.
sshd_effective() { # sshd_effective <keyword>: the value sshd would use, or empty
  if command -v sshd >/dev/null 2>&1; then sshd -T 2>/dev/null | awk -v k="$1" 'tolower($1)==k {print tolower($2); exit}'; fi
}
f_ncs_ssh_ok() {
  root="$(sshd_effective permitrootlogin)"; pass="$(sshd_effective passwordauthentication)"
  [ -n "$root" ] && [ "$root" != yes ] && [ "$pass" = no ]
}
f_ncs_ssh_do() {
  echo "  set PermitRootLogin no (or prohibit-password) and PasswordAuthentication no in /etc/ssh/sshd_config.d/, AFTER you have shown that key login as $NC_USER works" >&2
  return 3
}

# A bounded journal on every server (the Pi profile's is the same file).
f_journal_ok() { [ -e "$(p /etc/systemd/journald.conf.d/nuit-cloud.conf)" ]; }
f_journal_do() {
  printf '[Journal]\nSystemMaxUse=%s\n' "$JOURNAL_MAX" | write_if_missing /etc/systemd/journald.conf.d/nuit-cloud.conf
  systemctl restart systemd-journald
}

# The Nuit Container Service runs each container as a microVM: it needs /dev/kvm. If the CPU supports it and the module is
# simply not loaded, load it; if virtualisation is off in the firmware, only a person can turn it on.
f_kvm_ok() { [ -e "$(p /dev/kvm)" ]; }
f_kvm_do() {
  modprobe kvm_intel 2>/dev/null || modprobe kvm_amd 2>/dev/null || true
  [ -e "$(p /dev/kvm)" ] && return 0
  echo "  no /dev/kvm: turn on Intel VT-x / AMD-V in the BIOS/UEFI (or enable nested virtualisation on the host), then run this again" >&2
  return 3
}

# cgroup v2 (the unified hierarchy): the runtime enforces each tenant's limits through it. The default on current Debian.
f_cgroup2_ok() { [ -e "$(p /sys/fs/cgroup/cgroup.controllers)" ]; }
f_cgroup2_do() { echo "  boot with the unified cgroup hierarchy (systemd.unified_cgroup_hierarchy=1 on the kernel command line), then reboot" >&2; return 3; }

# The Nuit Container Service runtime (a dedicated containerd service beside any Docker, the shim, the CNI bridge, the signed
# guest artifacts), installed from the oci-saas bundle. The agent hands this feature what it needs when the platform asks for a
# profile-apply, already downloaded and checked against the SHA-256 in the signed task:
#   NCS_RUNTIME_BUNDLE            the bundle tarball (oci-saas-bundle.tar.gz)
#   NCS_RUNTIME_BUNDLE_SHA256     its SHA-256 (recorded on the machine, so a different pinned bundle shows as drift)
#   NCS_RUNTIME_ROOT_FINGERPRINT  the fingerprint of the root key the bundle's keys must chain to. artifacts.pub comes from the
#                                 bundle, so checking the guest artifacts against it alone would be circular: this pins the anchor
#   NCS_CONTAINERD_TARBALL        the pinned official containerd 2.x release (Debian's own package is 1.7)
#   NCS_CRICTL_TARBALL            the pinned crictl release (Debian 13 has no cri-tools package)
# Run by hand without them, the feature says what is missing. The first install gives the node the bundle's initial policy, which
# admits namespace nuit-cloud-platform only; the platform's hourly policy-sync replaces it once the node is a registered pool host.
OCISAAS_ETC="${OCISAAS_ETC:-/etc/oci-saas}"
# The bundle's smoke test (a pod in a microVM answers HTTP) is kept here so the agent's runtime-smoke task can run it later: root-owned, copied from the verified bundle.
OCISAAS_SMOKE="${OCISAAS_SMOKE:-/usr/local/lib/nuit-cloud/oci-saas-smoke.sh}"
f_oci_saas_runtime_ok() {
  [ -x "$(p /usr/local/bin/crictl-ocisaas)" ] && systemctl is-active --quiet containerd-ocisaas \
    && [ -e "$(p /run/oci-saas/containerd.sock)" ] \
    && [ -s "$(p "$OCISAAS_ETC/artifacts.pub")" ] && [ -s "$(p "$OCISAAS_ETC/policy.pub")" ] && [ -x "$(p "$OCISAAS_SMOKE")" ] || return 1
  # when the platform names a bundle, the one installed must be it (an upgrade then shows as drift)
  [ -z "${NCS_RUNTIME_BUNDLE_SHA256:-}" ] || [ "$(cat "$(p "$OCISAAS_ETC/installed-bundle")" 2>/dev/null)" = "$NCS_RUNTIME_BUNDLE_SHA256" ]
}

# The fingerprint of the root key in a keys file (`root <hex>`): the SHA-256 of the key's bytes.
root_fingerprint_of() { # root_fingerprint_of <artifacts.pub>
  awk '$1=="root" {print $2; exit}' "$1" | xxd -r -p | sha256sum | awk '{print $1}'
}

f_oci_saas_runtime_do() {
  if [ -z "${NCS_RUNTIME_BUNDLE:-}" ] || [ -z "${NCS_CONTAINERD_TARBALL:-}" ] || [ -z "${NCS_CRICTL_TARBALL:-}" ] || [ -z "${NCS_RUNTIME_ROOT_FINGERPRINT:-}" ]; then
    echo "  the runtime comes from the oci-saas bundle: ask for this profile from the platform, which gives the agent the bundle, containerd and the root fingerprint to check it against" >&2; return 3
  fi
  [ -f "$NCS_RUNTIME_BUNDLE" ] && [ -f "$NCS_CONTAINERD_TARBALL" ] || { echo "  the bundle or the containerd tarball is not where the agent said it put it" >&2; return 1; }
  [ -e "$(p /dev/kvm)" ] || { echo "  /dev/kvm is missing: the kvm feature comes first" >&2; return 3; }
  sv="$(systemctl --version 2>/dev/null | awk 'NR==1 {print $2}')"
  [ "${sv:-0}" -ge 254 ] 2>/dev/null || { echo "  the runtime needs systemd 254 or newer (this is ${sv:-unknown})" >&2; return 3; }

  # A containerd already at /usr/bin that is not 2.x belongs to something else (Docker's package); it is not replaced.
  have="$("$(p /usr/bin/containerd)" --version 2>/dev/null || true)"
  if [ -n "$have" ] && ! printf '%s' "$have" | grep -qE ' v?2\.'; then
    echo "  /usr/bin/containerd is $have; the runtime needs 2.x and will not replace another service's containerd: a person decides" >&2; return 3
  fi

  apt-get update -qq && apt-get install -y -qq erofs-utils e2fsprogs iptables containernetworking-plugins xxd python3-minimal || return 1

  work="$(mktemp -d)"
  tar -xzf "$NCS_RUNTIME_BUNDLE" -C "$work" || { rm -rf "$work"; echo "  the bundle is not a readable tarball" >&2; return 1; }
  dir="$(find "$work" -mindepth 1 -maxdepth 1 -type d | head -1)"
  [ -n "$dir" ] && [ -x "$dir/packaging/dedicated/install.sh" ] && [ -f "$dir/artifacts.pub" ] && [ -f "$dir/policy.pub" ] \
    || { rm -rf "$work"; echo "  the bundle does not have the layout of an oci-saas bundle" >&2; return 1; }
  got="$(root_fingerprint_of "$dir/artifacts.pub")"
  if [ "$got" != "$NCS_RUNTIME_ROOT_FINGERPRINT" ]; then
    rm -rf "$work"; echo "  the bundle's keys chain to root $got, not the root the platform pinned: nothing was installed" >&2; return 1
  fi

  if [ -z "$have" ]; then
    tar -xzf "$NCS_CONTAINERD_TARBALL" -C "$work" bin/containerd || { rm -rf "$work"; echo "  could not unpack containerd" >&2; return 1; }
    install -m 0755 "$work/bin/containerd" "$(p /usr/bin/containerd)"
  fi

  if [ ! -x "$(p /usr/local/bin/crictl)" ] && ! command -v crictl >/dev/null 2>&1; then
    work2="$(mktemp -d)"
    tar -xzf "$NCS_CRICTL_TARBALL" -C "$work2" crictl || { rm -rf "$work" "$work2"; echo "  could not unpack crictl" >&2; return 1; }
    install -m 0755 "$work2/crictl" "$(p /usr/local/bin/crictl)"; rm -rf "$work2"
  fi

  # first install: the bundle's own policy; every later run keeps the policy the platform has published and restarts
  mode="--policy-dir $dir/policy"; restart_after=
  if [ -e "$(p "$OCISAAS_ETC/installed-bundle")" ]; then
    if grep -q -- '--keep-policy' "$dir/packaging/dedicated/install.sh"; then
      mode="--keep-policy --restart"
    else
      # an older bundle's install.sh has no --keep-policy: hand it a copy of the policy files the node has now
      mkdir -p "$work/keep-policy" && cp "$(p /var/lib/oci-saas/policy)"/policy.json* "$work/keep-policy/" \
        || { rm -rf "$work"; echo "  could not read the installed policy to keep it; nothing was changed" >&2; return 1; }
      mode="--policy-dir $work/keep-policy"; restart_after=1
    fi
  fi
  # shellcheck disable=SC2086
  "$dir/packaging/dedicated/install.sh" --bin "$dir/bin" --guest "$dir/guest" --artifacts-pub "$dir/artifacts.pub" \
    --policy-pub "$dir/policy.pub" $mode --cni-bin /usr/lib/cni --add-user "$NC_USER" \
    || { rm -rf "$work"; echo "  the bundle's install.sh failed (its output is above)" >&2; return 1; }
  [ -z "$restart_after" ] || systemctl restart containerd-ocisaas   # running pods keep running
  mkdir -p "$(p "$OCISAAS_ETC")" "$(dirname "$(p "$OCISAAS_SMOKE")")"
  install -m 0755 "$dir/packaging/dedicated/smoke.sh" "$(p "$OCISAAS_SMOKE")"
  printf '%s\n' "${NCS_RUNTIME_BUNDLE_SHA256:-unknown}" >"$(p "$OCISAAS_ETC/installed-bundle")"
  rm -rf "$work"
}

# Tenants' volumes are disk images under the runtime's directory (default /var/lib/oci-saas) and need room to grow. If the
# filesystem that directory would be on has less than MIN_VOLUMES_GIB free and another real filesystem has far more, the
# directory is put on that one with a bind mount (the runtime sees an ordinary directory). It never touches a directory
# that already has content, and the fstab line is `nofail`, so a mistake cannot stop the machine booting.
VOLUMES_DIR="${VOLUMES_DIR:-/var/lib/oci-saas}"
MIN_VOLUMES_GIB="${MIN_VOLUMES_GIB:-20}"

# Free space in KiB on the filesystem that holds <path>, or on its nearest existing parent.
free_kib_of() {
  d="$1"; while [ ! -e "$d" ] && [ "$d" != / ]; do d="$(dirname "$d")"; done
  df -Pk "$d" 2>/dev/null | awk 'NR==2 {print $4}'
}
# The real filesystems worth putting volumes on, one per line: "<free KiB> <mount point>", roomiest first. Not the boot
# partitions, not virtual or removable filesystems, and not the filesystem the volumes directory is already on.
roomy_mounts() {
  here="$(free_kib_of "$(p "$VOLUMES_DIR")")"
  awk '$3 ~ /^(ext[234]|xfs|btrfs|f2fs)$/ {print $2}' "$(p /proc/mounts)" 2>/dev/null | while read -r m; do
    case "$m" in /boot|/boot/*|/run|/run/*|/snap/*|/var/lib/docker/*) continue ;; esac
    free="$(df -Pk "$(p "$m")" 2>/dev/null | awk 'NR==2 {print $4}')"
    [ -n "$free" ] && [ "$free" != "$here" ] && echo "$free $m"
  done | sort -rn
}
f_ncs_volumes_ok() {
  mountpoint -q "$(p "$VOLUMES_DIR")" 2>/dev/null && return 0              # already its own mount (a bind mount or a volume)
  free="$(free_kib_of "$(p "$VOLUMES_DIR")")"
  [ -n "$free" ] && [ "$free" -ge $((MIN_VOLUMES_GIB * 1024 * 1024)) ]       # or it already has room
}
f_ncs_volumes_do() {
  dest="$(p "$VOLUMES_DIR")"
  if [ -d "$dest" ] && [ -n "$(ls -A "$dest" 2>/dev/null)" ]; then
    echo "  $VOLUMES_DIR already has content and not enough room: move it by hand, then bind-mount the new place onto it" >&2; return 3
  fi
  here="$(free_kib_of "$dest")"
  pick="$(roomy_mounts | awk -v here="${here:-0}" -v min=$((MIN_VOLUMES_GIB * 1024 * 1024)) '$1 >= min && $1 >= here * 4 {print; exit}')"
  if [ -z "$pick" ]; then echo "  no filesystem has $MIN_VOLUMES_GIB GiB and four times the room of the one $VOLUMES_DIR is on: grow one, or add a disk" >&2; return 3; fi
  mnt="${pick#* }"; src="${mnt%/}/oci-saas"
  echo "  putting $VOLUMES_DIR on $mnt ($((${pick%% *} / 1024 / 1024)) GiB free) as $src" >&2
  mkdir -p "$(p "$src")" "$dest"
  chmod 755 "$(p "$src")" "$dest"
  line="$src $VOLUMES_DIR none bind,nofail,x-systemd.requires-mounts-for=$mnt 0 0"
  grep -qsF " $VOLUMES_DIR " "$(p /etc/fstab)" 2>/dev/null || printf '%s\n' "$line" >>"$(p /etc/fstab)"
  systemctl daemon-reload 2>/dev/null || true
  mount "$VOLUMES_DIR"
}

# ─── The management address the platform allocated (ticket #67) ──────────────────────────────────────────────────────────────
# Turns the machine's DHCP address into the static one the platform allocated for it. This can cut a machine off from the platform,
# so it is built to be undone without anyone's help:
#   * a systemd timer that restores the old configuration is armed BEFORE anything changes, and cancelled only once the machine has
#     reached the platform from the new address; if the agent, the network or the machine dies in between, the timer still fires;
#   * the change is additive while it is tried: the new address is added beside the old one, and the old one is removed only after
#     that works; the interface is never taken down (VLANs and containers on it stay up);
#   * only ifupdown (Debian's default) is handled; NetworkManager, systemd-networkd or an address in another subnet are reported
#     MANUAL and left alone.
# The agent passes NCS_MANAGEMENT_ADDRESS (from the signed task) and NCS_PLATFORM_URL (what to reach to prove it works).
ADDR_DIR="${ADDR_DIR:-/var/lib/nuit-cloud-agent/address}"
ADDR_ROLLBACK_SECONDS="${ADDR_ROLLBACK_SECONDS:-180}"
ADDR_STATIC_FILE="/etc/network/interfaces.d/nuit-cloud-address"

addr_is_ipv4() { printf '%s' "$1" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}$' && ! printf '%s' "$1" | grep -Eq '(^|\.)(25[6-9]|2[6-9][0-9]|[3-9][0-9][0-9])(\.|$)'; }

# The interface and gateway of the default route, and the machine's own address on it (set A_IF, A_GW, A_CUR, A_PREFIX).
addr_discover() {
  A_IF="$(ip -4 route show default 2>/dev/null | awk '/^default/ {for (i = 1; i < NF; i++) if ($i == "dev") {print $(i + 1); exit}}')"
  A_GW="$(ip -4 route show default 2>/dev/null | awk '/^default/ {for (i = 1; i < NF; i++) if ($i == "via") {print $(i + 1); exit}}')"
  A_CUR=""; A_PREFIX=""
  [ -n "$A_IF" ] || return 1
  cur="$(ip -4 -o addr show dev "$A_IF" scope global 2>/dev/null | awk '{print $4; exit}')"
  A_CUR="${cur%/*}"; A_PREFIX="${cur#*/}"
}

# The ifupdown files that define <interface> (the main file and interfaces.d/*).
addr_files_for() {
  for f in "$(p /etc/network/interfaces)" "$(p /etc/network/interfaces.d)"/*; do
    [ -f "$f" ] && grep -Eq "^[[:space:]]*iface[[:space:]]+$1[[:space:]]" "$f" && printf '%s\n' "$f"
  done
  return 0
}

f_ncs_address_ok() {
  [ -n "${NCS_MANAGEMENT_ADDRESS:-}" ] && addr_discover || return 1
  ip -4 -o addr show dev "$A_IF" 2>/dev/null | awk '{print $4}' | grep -qx "$NCS_MANAGEMENT_ADDRESS/$A_PREFIX" || return 1
  grep -Eq "^[[:space:]]*address[[:space:]]+$NCS_MANAGEMENT_ADDRESS/" "$(p "$ADDR_STATIC_FILE")" 2>/dev/null || return 1
  for f in $(addr_files_for "$A_IF"); do
    [ "$f" = "$(p "$ADDR_STATIC_FILE")" ] && continue
    ! grep -Eq "^[[:space:]]*iface[[:space:]]+$A_IF[[:space:]]+inet[[:space:]]+dhcp" "$f" || return 1
  done
  return 0
}

# Comment out the stanza that defines <interface> (inet) in <file>, keeping every other line. A stanza ends at the next line
# that begins a new one (auto, allow-*, iface, mapping, source, no-*) or at a line with no indentation that is not a comment.
addr_neutralise() { # addr_neutralise <file> <interface>
  awk -v ifc="$2" '
    /^[[:space:]]*iface[[:space:]]+/ { in_stanza = ($2 == ifc && $3 == "inet") }
    /^(auto|allow-[a-z]+|mapping|source|source-directory|no-auto-down|no-scripts)([[:space:]]|$)/ { in_stanza = 0 }
    { if (in_stanza && $0 !~ /^[[:space:]]*#/ && $0 !~ /^[[:space:]]*$/) print "# nuit-cloud: " $0; else print }
  ' "$1"
}

# Everything else on the machine that depends on the network: every IPv4 address (VLAN interfaces, tenant addresses), every route
# and every policy rule. Taken before the change, so the change can be proved not to have lost any of it, and so the rollback
# can put back what was lost.
addr_snapshot() { # addr_snapshot <dir>
  ip -4 -o addr show 2>/dev/null | awk '{print $2, $4}' | sort >"$1/before.addrs"
  ip -4 route show 2>/dev/null | sort >"$1/before.routes"
  ip -4 rule show 2>/dev/null | sed 's/^[0-9]*:[[:space:]]*//' | sort >"$1/before.rules"
}
# What the snapshot had that is gone now, ignoring what the change was meant to replace (the old address and routes that name it).
# Writes one line per loss ("addr <if> <cidr>", "route <route>", "rule <rule>") to <dir>/lost and returns 1 if there is any.
addr_lost() { # addr_lost <dir> <old address or empty>
  ld="$1"; lold="$2"
  ip -4 -o addr show 2>/dev/null | awk '{print $2, $4}' | sort >"$ld/after.addrs"
  ip -4 route show 2>/dev/null | sort >"$ld/after.routes"
  ip -4 rule show 2>/dev/null | sed 's/^[0-9]*:[[:space:]]*//' | sort >"$ld/after.rules"
  : >"$ld/lost"
  for kind in addrs routes rules; do
    comm -23 "$ld/before.$kind" "$ld/after.$kind" | while IFS= read -r line; do
      if [ -n "$lold" ]; then case "$line" in *" $lold/"*|*"src $lold"*|*" $lold "*) continue ;; esac; fi
      printf '%s %s\n' "${kind%s}" "$line" >>"$ld/lost"
    done
  done
  [ ! -s "$ld/lost" ]
}
# Put back what is missing: an address by "ip addr add", a route by "ip route replace". Rules are reported, not restored.
addr_heal() { # addr_heal <dir>
  while IFS= read -r l; do
    case "$l" in
      "addr "*) set -- ${l#addr }; ip addr add "$2" dev "$1" 2>/dev/null || true ;;
      "route "*) ip route replace ${l#route } 2>/dev/null || true ;;
    esac
  done <"$1/lost"
}

f_ncs_address_do() {
  target="${NCS_MANAGEMENT_ADDRESS:-}"
  [ -n "$target" ] || { echo "  no management address was given: allocate one in the portal, then apply this profile from there" >&2; return 3; }
  addr_is_ipv4 "$target" || { echo "  $target is not an IPv4 address" >&2; return 3; }
  command -v ifup >/dev/null 2>&1 && [ -f "$(p /etc/network/interfaces)" ] || { echo "  this machine does not use ifupdown (no /etc/network/interfaces): set $target by hand" >&2; return 3; }
  for m in NetworkManager systemd-networkd; do
    ! systemctl is-active --quiet "$m" 2>/dev/null || { echo "  $m manages the network here; this feature only handles ifupdown: set $target by hand" >&2; return 3; }
  done
  command -v systemd-run >/dev/null 2>&1 || { echo "  systemd-run is needed for the safety timer; set $target by hand" >&2; return 3; }
  addr_discover && [ -n "$A_GW" ] && [ -n "$A_CUR" ] || { echo "  could not find the default route and the address on it" >&2; return 3; }
  addr_is_ipv4 "$A_CUR" && addr_is_ipv4 "$A_GW" || { echo "  the default route is not plain IPv4" >&2; return 3; }
  # same subnet only: another subnet changes the gateway and routes, which a person should see
  net_of() { awk -v ip="$1" -v pl="$2" 'BEGIN {split(ip, o, "."); v = ((o[1] * 256 + o[2]) * 256 + o[3]) * 256 + o[4]; b = 2 ^ (32 - pl); print int(v / b)}'; }
  [ "$(net_of "$target" "$A_PREFIX")" = "$(net_of "$A_CUR" "$A_PREFIX")" ] || { echo "  $target is not in the machine's current subnet ($A_CUR/$A_PREFIX): change subnets by hand" >&2; return 3; }
  [ "$target" != "$A_GW" ] || { echo "  $target is the gateway's own address" >&2; return 3; }
  files="$(addr_files_for "$A_IF")"
  [ -n "$files" ] || { echo "  $A_IF is not defined in /etc/network/interfaces: it is configured some other way; set $target by hand" >&2; return 3; }
  printf '%s\n' "$files" | grep -q "$ADDR_STATIC_FILE\$" && files="$(printf '%s\n' "$files" | grep -v "$ADDR_STATIC_FILE\$")"
  printf '%s\n' "$A_IF" | grep -Eq '^[a-zA-Z0-9._-]{1,15}$' || { echo "  odd interface name $A_IF" >&2; return 3; }

  added=0
  if [ "$A_CUR" != "$target" ]; then
    # somebody else may already answer for it
    command -v arping >/dev/null 2>&1 || { apt-get install -y -qq iputils-arping >/dev/null 2>&1 || true; }
    if command -v arping >/dev/null 2>&1; then
      arping -D -q -c 2 -w 3 -I "$A_IF" "$target" >/dev/null 2>&1 || { echo "  another device already answers for $target on the network: nothing was changed" >&2; return 1; }
    fi
    added=1
  fi

  # backups, then the timer that undoes everything
  d="$(p "$ADDR_DIR")"; rm -rf "$d/backup"; mkdir -p "$d/backup"; chmod 700 "$d"
  addr_snapshot "$d"
  : >"$d/backup/files"
  for f in $files; do cp -p "$f" "$d/backup/$(printf '%s' "$f" | tr '/' '_')" && printf '%s\n' "$f" >>"$d/backup/files"; done
  dhcp_was=0; for f in $files; do grep -Eq "^[[:space:]]*iface[[:space:]]+$A_IF[[:space:]]+inet[[:space:]]+dhcp" "$f" && dhcp_was=1; done
  cat >"$d/rollback.sh" <<ROLLBACK
#!/bin/sh
# Put the machine's network back as it was before the management address was applied. Run by the safety timer, or at once if the
# change could not be proved to work. Written by the nuit-cloud agent's ncs_address feature.
PATH="\$PATH:/usr/local/sbin:/usr/sbin:/sbin"
echo "nuit-cloud: undoing the management address change"
while IFS= read -r f; do cp -p "$d/backup/\$(printf '%s' "\$f" | tr '/' '_')" "\$f"; done <"$d/backup/files"
rm -f "$(p "$ADDR_STATIC_FILE")"
ip addr add $A_CUR/$A_PREFIX dev $A_IF 2>/dev/null
[ "$added" = 1 ] && ip addr del $target/$A_PREFIX dev $A_IF 2>/dev/null
ip route replace default via $A_GW dev $A_IF 2>/dev/null
[ "$dhcp_was" = 1 ] && ifup --force $A_IF 2>/dev/null
# and whatever else the machine had on its network: VLAN interfaces, tenant addresses, routes (put back if they are missing)
if [ -f "$d/before.addrs" ]; then while read -r ifc cidr; do ip addr add "\$cidr" dev "\$ifc" 2>/dev/null; done <"$d/before.addrs"; fi
if [ -f "$d/before.routes" ]; then while IFS= read -r line; do ip route replace \$line 2>/dev/null; done <"$d/before.routes"; fi
rm -f "$d/armed"
exit 0
ROLLBACK
  chmod 700 "$d/rollback.sh"
  systemd-run --quiet --unit=nuit-cloud-address-rollback --collect --on-active="${ADDR_ROLLBACK_SECONDS}s" /bin/sh "$d/rollback.sh" || { echo "  could not arm the safety timer; nothing was changed" >&2; return 1; }
  : >"$d/armed"

  # 1. live, additive: the new address beside the old one
  if [ "$added" = 1 ]; then
    sysctl -q -w "net.ipv4.conf.$A_IF.promote_secondaries=1" >/dev/null 2>&1 || true
    ip addr add "$target/$A_PREFIX" dev "$A_IF" || { sh "$d/rollback.sh"; echo "  could not add $target to $A_IF" >&2; return 1; }
  fi
  # 2. persistent: the DHCP stanza out, the static one in
  for f in $files; do
    addr_neutralise "$f" "$A_IF" >"$f.nuit-new" && mv "$f.nuit-new" "$f" || { sh "$d/rollback.sh"; echo "  could not rewrite $f" >&2; return 1; }
  done
  mkdir -p "$(dirname "$(p "$ADDR_STATIC_FILE")")"
  cat >"$(p "$ADDR_STATIC_FILE")" <<STATIC
# Written by the nuit-cloud agent: the management address the platform allocated for this machine. The previous configuration is
# commented out where it was, and kept in $ADDR_DIR/backup.
iface $A_IF inet static
    address $target/$A_PREFIX
    gateway $A_GW
STATIC
  # 3. the DHCP client stops, so it does not take the address back or rewrite the routes (the address stays on the interface)
  for pf in "/run/dhclient.$A_IF.pid" "/run/dhcpcd/$A_IF.pid" "/run/udhcpc.$A_IF.pid"; do
    [ -f "$(p "$pf")" ] && kill "$(cat "$(p "$pf")")" 2>/dev/null || true
  done
  ip -4 -o addr show dev "$A_IF" 2>/dev/null | awk '{print $4}' | grep -qx "$target/$A_PREFIX" || ip addr add "$target/$A_PREFIX" dev "$A_IF" 2>/dev/null || true
  ip route replace default via "$A_GW" dev "$A_IF" 2>/dev/null || true
  # 4. the old address goes, only now
  if [ "$added" = 1 ]; then
    ip addr del "$A_CUR/$A_PREFIX" dev "$A_IF" 2>/dev/null || true
    # removing the primary address takes its secondaries with it unless the kernel promotes them (the sysctl above, which a
    # restricted system may refuse): so the new address and the route are asserted again, which is harmless when they survived
    ip -4 -o addr show dev "$A_IF" 2>/dev/null | awk '{print $4}' | grep -qx "$target/$A_PREFIX" || ip addr add "$target/$A_PREFIX" dev "$A_IF" 2>/dev/null || true
    ip route replace default via "$A_GW" dev "$A_IF" 2>/dev/null || true
  fi

  # 5. nothing else may have been lost (VLAN interfaces, tenant addresses, routes): what is missing is put back, and if it still
  #    is, everything is undone
  old_for_check=""; [ "$added" = 1 ] && old_for_check="$A_CUR"
  if ! addr_lost "$d" "$old_for_check"; then
    addr_heal "$d"
    if ! addr_lost "$d" "$old_for_check"; then
      lost_list="$(tr '\n' ';' <"$d/lost")"
      sh "$d/rollback.sh"
      echo "  the change would have lost other network configuration ($lost_list), so the old configuration was put back" >&2
      return 1
    fi
  fi

  # 6. proof: the platform must be reachable from the new address
  proved=0
  if [ -n "${NCS_PLATFORM_URL:-}" ]; then
    for _ in 1 2 3 4 5 6; do
      if curl -fsS --max-time 15 --interface "$target" -o /dev/null "$NCS_PLATFORM_URL/api/agent/download/VERSION" 2>/dev/null; then proved=1; break; fi
      sleep 5
    done
  else
    ping -c 2 -W 3 -I "$target" "$A_GW" >/dev/null 2>&1 && proved=1
  fi
  if [ "$proved" != 1 ]; then
    sh "$d/rollback.sh"
    echo "  the platform could not be reached from $target, so the old configuration was put back" >&2
    return 1
  fi
  systemctl stop nuit-cloud-address-rollback.timer >/dev/null 2>&1 || true
  rm -f "$d/armed"
  echo "  $A_IF now uses $target/$A_PREFIX as a static address (was $A_CUR; the old configuration is in $ADDR_DIR/backup)"
}

# ─── The tenant VLAN slice (ticket #67; ports deploy/server-vlan5.sh) ───────────────────────────────────────────────────────
# Adds a VLAN sub-interface on the machine's default-route interface carrying the slice the platform allocated: the node's own
# address and the tenant block as secondary addresses, plus the rules that send replies for connections that arrived on the VLAN
# back out by the VLAN gateway (without them a reply from a tenant address leaves by the wrong interface and the router drops it).
# The agent passes NCS_VLAN_ID, NCS_VLAN_ADDRESS, NCS_VLAN_PREFIX, NCS_VLAN_GATEWAY and NCS_VLAN_TENANT_ADDRESSES.
# The VLAN is assumed to be carried to the port already, so a gateway that does not answer is a note, not a failure. What can hurt
# is the change taking the machine's own connectivity with it, so as for the management address: a timer that undoes it is armed
# first; nothing existing is touched (a new interface only); a snapshot proves no address or route was lost; and the platform must
# still be reachable afterwards, or everything is undone.
VLAN_TABLE="${VLAN_TABLE:-105}"
VLAN_MARK="${VLAN_MARK:-0x5}"

vlan_file() { printf '/etc/network/interfaces.d/nuit-cloud-vlan%s' "$NCS_VLAN_ID"; }

vlan_render() { # vlan_render <interface>  -> the stanza on stdout
  _if="$1"; _vif="$_if.$NCS_VLAN_ID"
  cat <<EOF
# Managed by the nuit-cloud agent (ncs_vlan): the tenant VLAN slice the platform allocated for this machine.
auto ${_vif}
iface ${_vif} inet static
    address ${NCS_VLAN_ADDRESS}/${NCS_VLAN_PREFIX}
    pre-up modprobe 8021q 2>/dev/null || true
    pre-up ip link show ${_vif} >/dev/null 2>&1 || ip link add link ${_if} name ${_vif} type vlan id ${NCS_VLAN_ID}
EOF
  for a in $NCS_VLAN_TENANT_ADDRESSES; do
    echo "    post-up ip addr add ${a}/${NCS_VLAN_PREFIX} dev ${_vif} 2>/dev/null || true"
  done
  cat <<EOF
    post-up sysctl -qw net.ipv4.conf.${_if}/${NCS_VLAN_ID}.rp_filter=2
    post-up iptables -t mangle -C PREROUTING -i ${_vif} -m conntrack --ctstate NEW -j CONNMARK --set-mark ${VLAN_MARK} 2>/dev/null || iptables -t mangle -A PREROUTING -i ${_vif} -m conntrack --ctstate NEW -j CONNMARK --set-mark ${VLAN_MARK}
    post-up iptables -t mangle -C PREROUTING ! -i ${_vif} -m connmark --mark ${VLAN_MARK} -j CONNMARK --restore-mark 2>/dev/null || iptables -t mangle -A PREROUTING ! -i ${_vif} -m connmark --mark ${VLAN_MARK} -j CONNMARK --restore-mark
    post-up ip route replace default via ${NCS_VLAN_GATEWAY} dev ${_vif} table ${VLAN_TABLE}
    post-up ip rule show | grep -q "fwmark ${VLAN_MARK} lookup ${VLAN_TABLE}" || ip rule add fwmark ${VLAN_MARK} lookup ${VLAN_TABLE} priority 1050
    pre-down ip rule del fwmark ${VLAN_MARK} lookup ${VLAN_TABLE} 2>/dev/null || true
    pre-down iptables -t mangle -D PREROUTING ! -i ${_vif} -m connmark --mark ${VLAN_MARK} -j CONNMARK --restore-mark 2>/dev/null || true
    pre-down iptables -t mangle -D PREROUTING -i ${_vif} -m conntrack --ctstate NEW -j CONNMARK --set-mark ${VLAN_MARK} 2>/dev/null || true
    pre-down ip route flush table ${VLAN_TABLE} 2>/dev/null || true
    post-down ip link del ${_vif} 2>/dev/null || true
EOF
}

vlan_inputs_ok() {
  printf '%s' "${NCS_VLAN_ID:-}" | grep -Eq '^[0-9]{1,4}$' || return 1
  printf '%s' "${NCS_VLAN_PREFIX:-}" | grep -Eq '^[0-9]{1,2}$' || return 1
  addr_is_ipv4 "${NCS_VLAN_ADDRESS:-}" && addr_is_ipv4 "${NCS_VLAN_GATEWAY:-}" || return 1
  [ -n "${NCS_VLAN_TENANT_ADDRESSES:-}" ] || return 1
  for a in $NCS_VLAN_TENANT_ADDRESSES; do addr_is_ipv4 "$a" || return 1; done
  return 0
}

f_ncs_vlan_ok() {
  vlan_inputs_ok && addr_discover || return 1
  _file="$(p "$(vlan_file)")"
  _vif="$A_IF.$NCS_VLAN_ID"
  # exactly what this feature writes, or an equivalent configuration made by hand (deploy/server-vlan5.sh): the same sub-interface with
  # the same address and tenant block named in it. A working VLAN made by hand is adopted as it is, never rewritten.
  if ! vlan_render "$A_IF" | cmp -s - "$_file" 2>/dev/null; then
    grep -Eq "^[[:space:]]*iface[[:space:]]+$_vif[[:space:]]+inet[[:space:]]+static" "$_file" 2>/dev/null || return 1
    grep -Eq "^[[:space:]]*address[[:space:]]+$NCS_VLAN_ADDRESS/$NCS_VLAN_PREFIX" "$_file" || return 1
    # the tenant block must be exactly the same set, not a part of it
    _in_file="$(grep -o 'ip addr add [0-9.]*' "$_file" | awk '{print $4}' | sort | tr '\n' ' ')"
    _wanted="$(printf '%s\n' $NCS_VLAN_TENANT_ADDRESSES | sort | tr '\n' ' ')"
    [ "$_in_file" = "$_wanted" ] || return 1
  fi
  _have="$(ip -4 -o addr show dev "$_vif" 2>/dev/null | awk '{print $4}')"
  for a in "$NCS_VLAN_ADDRESS" $NCS_VLAN_TENANT_ADDRESSES; do printf '%s\n' "$_have" | grep -qx "$a/$NCS_VLAN_PREFIX" || return 1; done
  return 0
}

f_ncs_vlan_do() {
  vlan_inputs_ok || { echo "  the VLAN slice was not given, or is not valid: allocate one in the portal, then apply this profile from there" >&2; return 3; }
  command -v ifup >/dev/null 2>&1 && [ -f "$(p /etc/network/interfaces)" ] || { echo "  this machine does not use ifupdown: set the VLAN up by hand (deploy/server-vlan5.sh)" >&2; return 3; }
  for m in NetworkManager systemd-networkd; do
    ! systemctl is-active --quiet "$m" 2>/dev/null || { echo "  $m manages the network here; this feature only handles ifupdown" >&2; return 3; }
  done
  command -v systemd-run >/dev/null 2>&1 && command -v iptables >/dev/null 2>&1 || { echo "  systemd-run (the safety timer) and iptables (reply routing) are needed" >&2; return 3; }
  addr_discover && [ -n "$A_GW" ] || { echo "  could not find the default route and its interface" >&2; return 3; }
  printf '%s\n' "$A_IF" | grep -Eq '^[a-zA-Z0-9_-]{1,10}$' || { echo "  the interface name $A_IF cannot carry a VLAN sub-interface here" >&2; return 3; }
  vif="$A_IF.$NCS_VLAN_ID"
  # ifup of a VLAN sub-interface first brings up its parent interface unless ifupdown already has it recorded as up; on a machine whose
  # parent was configured another way that would run its address configuration again. Found on a real kernel: only proceed if ifupdown
  # itself holds the parent up.
  command -v ifquery >/dev/null 2>&1 && ifquery --state "$A_IF" >/dev/null 2>&1 || {
    echo "  $A_IF is not held up by ifupdown here (ifquery --state says it is not), and ifup of its VLAN would try to bring it up itself and could disturb its address: set the VLAN up by hand (deploy/server-vlan5.sh)" >&2
    return 3
  }
  # the VLAN's own addresses must not already be somewhere else on this machine
  for a in "$NCS_VLAN_ADDRESS" $NCS_VLAN_TENANT_ADDRESSES; do
    if ip -4 -o addr show 2>/dev/null | awk -v v="$vif" '$2 != v {print $4}' | grep -qx "$a/$NCS_VLAN_PREFIX"; then echo "  $a is already on another interface of this machine: nothing was changed" >&2; return 1; fi
  done

  file="$(p "$(vlan_file)")"
  d="$(p "$ADDR_DIR")/vlan"; rm -rf "$d"; mkdir -p "$d"; chmod 700 "$d"
  addr_snapshot "$d"
  existing=0; [ -f "$file" ] && { cp -p "$file" "$d/previous"; existing=1; }
  cat >"$d/rollback.sh" <<ROLLBACK
#!/bin/sh
# Take the tenant VLAN off the machine again. Run by the safety timer, or at once if the change could not be proved to work.
PATH="\$PATH:/usr/local/sbin:/usr/sbin:/sbin"
echo "nuit-cloud: undoing the tenant VLAN change"
ifdown --force $vif 2>/dev/null
if [ "$existing" = 1 ]; then cp -p "$d/previous" "$file"; else rm -f "$file"; fi
ip link del $vif 2>/dev/null
if [ -f "$d/before.addrs" ]; then while read -r ifc cidr; do ip addr add "\$cidr" dev "\$ifc" 2>/dev/null; done <"$d/before.addrs"; fi
if [ -f "$d/before.routes" ]; then while IFS= read -r line; do ip route replace \$line 2>/dev/null; done <"$d/before.routes"; fi
rm -f "$d/armed"
exit 0
ROLLBACK
  chmod 700 "$d/rollback.sh"
  systemd-run --quiet --unit=nuit-cloud-vlan-rollback --collect --on-active="${ADDR_ROLLBACK_SECONDS}s" /bin/sh "$d/rollback.sh" || { echo "  could not arm the safety timer; nothing was changed" >&2; return 1; }
  : >"$d/armed"

  mkdir -p "$(dirname "$file")"
  vlan_render "$A_IF" >"$file"
  [ -e "$(p /etc/modules-load.d/8021q.conf)" ] || { mkdir -p "$(p /etc/modules-load.d)"; echo 8021q >"$(p /etc/modules-load.d/8021q.conf)"; }   # load the VLAN module at boot
  systemctl restart systemd-modules-load.service >/dev/null 2>&1 || true   # the agent's own sandbox may not load modules; systemd can
  if ! ifup "$vif" >"$d/ifup.out" 2>&1; then
    sh "$d/rollback.sh"
    echo "  ifup $vif failed ($(tr '\n' ' ' <"$d/ifup.out" | cut -c1-200)), so the machine was put back" >&2
    return 1
  fi

  # nothing that was there may be gone, and the platform must still be reachable
  if ! addr_lost "$d" ""; then
    addr_heal "$d"
    if ! addr_lost "$d" ""; then
      lost_list="$(tr '\n' ';' <"$d/lost")"
      sh "$d/rollback.sh"
      echo "  the change would have lost other network configuration ($lost_list), so the machine was put back" >&2
      return 1
    fi
  fi
  proved=0
  if [ -n "${NCS_PLATFORM_URL:-}" ]; then
    for _ in 1 2 3 4 5 6; do
      if curl -fsS --max-time 15 -o /dev/null "$NCS_PLATFORM_URL/api/agent/download/VERSION" 2>/dev/null; then proved=1; break; fi
      sleep 5
    done
  else
    ping -c 2 -W 3 "$A_GW" >/dev/null 2>&1 && proved=1
  fi
  if [ "$proved" != 1 ]; then
    sh "$d/rollback.sh"
    echo "  the platform could not be reached after the change, so the machine was put back" >&2
    return 1
  fi
  systemctl stop nuit-cloud-vlan-rollback.timer >/dev/null 2>&1 || true
  rm -f "$d/armed"
  if ping -c 2 -W 2 -I "$vif" "$NCS_VLAN_GATEWAY" >/dev/null 2>&1; then
    echo "  $vif carries $NCS_VLAN_ADDRESS and the tenant block; the VLAN gateway $NCS_VLAN_GATEWAY answers"
  else
    echo "  $vif carries $NCS_VLAN_ADDRESS and the tenant block. Note: the VLAN gateway $NCS_VLAN_GATEWAY did not answer yet; the VLAN is assumed to reach this port, so this is left in place"
  fi
}

# ─── SSH hardening, with the platform in the check ──────────────────────────────────────────────────────────────────────────────
# Writes /etc/ssh/sshd_config.d/00-nuit-cloud.conf (PermitRootLogin no, PasswordAuthentication no; the 00- makes it win, since sshd
# keeps the first value it sees). This is the one change that can lock everyone out, so:
#   * it refuses unless the login account's authorized_keys holds at least one of the keys the platform names
#     (NCS_SSH_REQUIRE_FINGERPRINTS, SHA256 fingerprints), sshd accepts keys at all, sshd's configuration is valid and includes
#     sshd_config.d, and the keys file is one sshd will honour (owned by the account, not group- or world-writable);
#   * before anything changes a timer is armed (nuit-cloud-ssh-rollback) that takes the file away and reloads sshd;
#   * after the reload it checks what sshd now uses and that it still listens, then leaves the timer ARMED: the platform tests sshd
#     from outside (answers, offers a key and no password) and only then sends `ssh-confirm`, which cancels the timer. If the
#     platform's test fails or never happens, passwords come back by themselves.
SSH_DROPIN="${SSH_DROPIN:-/etc/ssh/sshd_config.d/00-nuit-cloud.conf}"
SSH_CONFIRM_SECONDS="${NCS_SSH_CONFIRM_SECONDS:-600}"

f_ncs_ssh_harden_ok() { f_ncs_ssh_ok; }

ssh_reload() { systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null; }

f_ncs_ssh_harden_do() {
  required="${NCS_SSH_REQUIRE_FINGERPRINTS:-}"
  [ -n "$required" ] || { echo "  the platform named no administrator key that must still be able to log in: apply this profile from the portal" >&2; return 3; }
  for f in $required; do printf '%s' "$f" | grep -Eq '^SHA256:[A-Za-z0-9+/]{43}$' || { echo "  '$f' is not a key fingerprint (SHA256:…)" >&2; return 3; }; done
  command -v sshd >/dev/null 2>&1 && command -v ssh-keygen >/dev/null 2>&1 && command -v systemd-run >/dev/null 2>&1 || { echo "  sshd, ssh-keygen and systemd-run are needed" >&2; return 3; }
  sshd -t 2>/dev/null || { echo "  sshd's configuration is not valid as it stands: nothing was changed" >&2; return 3; }
  grep -Eqi '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf' "$(p /etc/ssh/sshd_config)" 2>/dev/null \
    || { echo "  /etc/ssh/sshd_config does not include sshd_config.d/*.conf, so the override would do nothing: edit it by hand" >&2; return 3; }
  [ "$(sshd_effective pubkeyauthentication)" = yes ] || { echo "  sshd does not accept keys (PubkeyAuthentication is not yes): turning passwords off would lock everyone out" >&2; return 3; }

  home="$(getent passwd "$NC_USER" 2>/dev/null | cut -d: -f6)"
  [ -n "$home" ] || { echo "  there is no account $NC_USER" >&2; return 3; }
  keys="$home/.ssh/authorized_keys"
  case "$(sshd_effective authorizedkeysfile)" in *.ssh/authorized_keys*) ;; *) echo "  sshd reads keys from somewhere other than .ssh/authorized_keys: set it up by hand" >&2; return 3 ;; esac
  [ -s "$keys" ] || { echo "  $keys is missing or empty: $NC_USER has no key to log in with" >&2; return 3; }
  owner="$(stat -c %U "$keys" 2>/dev/null || stat -f %Su "$keys" 2>/dev/null)"
  [ "$owner" = "$NC_USER" ] || [ "$owner" = root ] || { echo "  $keys is owned by $owner, so sshd ignores it" >&2; return 3; }
  [ -z "$(find "$keys" \( -perm -020 -o -perm -002 \) 2>/dev/null)" ] || { echo "  $keys is writable by others, so sshd ignores it" >&2; return 3; }
  have="$(ssh-keygen -lf "$keys" 2>/dev/null | awk '{print $2}')"
  found=0; for f in $required; do printf '%s\n' "$have" | grep -qx "$f" && found=1; done
  [ "$found" = 1 ] || { echo "  none of the keys the platform expects ($required) is in $keys: add one before passwords are turned off" >&2; return 3; }

  d="$(p "$ADDR_DIR")/ssh"; rm -rf "$d"; mkdir -p "$d"; chmod 700 "$d"
  dropin="$(p "$SSH_DROPIN")"; existing=0
  [ -f "$dropin" ] && { cp -p "$dropin" "$d/previous"; existing=1; }
  cat >"$d/rollback.sh" <<ROLLBACK
#!/bin/sh
# Put SSH back as it was before it was hardened. Run by the safety timer, or at once if the change could not be proved to work.
PATH="\$PATH:/usr/local/sbin:/usr/sbin:/sbin"
echo "nuit-cloud: undoing the SSH hardening"
if [ "$existing" = 1 ]; then cp -p "$d/previous" "$dropin"; else rm -f "$dropin"; fi
sshd -t && { systemctl reload ssh || systemctl reload sshd; }
rm -f "$d/armed"
exit 0
ROLLBACK
  chmod 700 "$d/rollback.sh"
  systemd-run --quiet --unit=nuit-cloud-ssh-rollback --collect --on-active="${SSH_CONFIRM_SECONDS}s" /bin/sh "$d/rollback.sh" || { echo "  could not arm the safety timer; nothing was changed" >&2; return 1; }
  : >"$d/armed"

  mkdir -p "$(dirname "$dropin")"
  printf '# Written by the nuit-cloud agent: SSH is for keys only. The 00- prefix makes these win over later files.\nPermitRootLogin no\nPasswordAuthentication no\n' >"$dropin"
  if ! sshd -t 2>"$d/sshd-t.out"; then
    sh "$d/rollback.sh"; echo "  sshd rejected the new configuration ($(tr '\n' ' ' <"$d/sshd-t.out" | cut -c1-160)), so it was taken away" >&2; return 1
  fi
  ssh_reload || { sh "$d/rollback.sh"; echo "  sshd could not be reloaded, so the change was taken away" >&2; return 1; }
  sleep 2
  root="$(sshd_effective permitrootlogin)"; pass="$(sshd_effective passwordauthentication)"
  if [ "$root" = yes ] || [ "$pass" != no ]; then
    sh "$d/rollback.sh"; echo "  sshd still uses PermitRootLogin $root / PasswordAuthentication $pass (another file wins?), so the change was taken away" >&2; return 1
  fi
  port="$(sshd_effective port)"; port="${port:-22}"
  ss -ltnH 2>/dev/null | awk '{print $4}' | grep -q ":$port\$" || { sh "$d/rollback.sh"; echo "  sshd is not listening on port $port after the reload, so the change was taken away" >&2; return 1; }
  echo "  SSH now refuses passwords and root login (sshd uses PermitRootLogin $root, PasswordAuthentication $pass). Waiting for the platform to test sshd from outside and confirm within ${SSH_CONFIRM_SECONDS}s; otherwise passwords come back."
}

# ─── Taking a machine off Wi-Fi, with the platform in the check ──────────────────────────────────────────────────────────────────
# A Nuit Cloud Server is wired. This turns a machine's wireless interfaces off and removes what remembers the network (the stanzas
# in /etc/network/interfaces*, and wpa_supplicant files that hold a passphrase), but only when a wired interface carries the
# default route AND the platform can still be reached with the wireless interfaces down. Order matters: the interfaces go down
# first and come straight back if the platform cannot be reached, and the configuration is removed only after that proof, because
# removing a passphrase cannot be undone.
wifi_ifaces() {
  for d in "$(p /sys/class/net)"/*; do
    [ -e "$d" ] || continue
    if [ -e "$d/wireless" ] || [ -e "$d/phy80211" ]; then basename "$d"; fi
  done
}
wifi_files() { # the ifupdown files and wpa_supplicant files that mention wireless
  for f in "$(p /etc/network/interfaces)" "$(p /etc/network/interfaces.d)"/* "$(p /etc/wpa_supplicant)"/*.conf; do
    [ -f "$f" ] && printf '%s\n' "$f"
  done
}
f_ncs_wifi_off_ok() {
  ifs="$(wifi_ifaces)"
  for i in $ifs; do
    [ "$(cat "$(p /sys/class/net)/$i/operstate" 2>/dev/null)" = up ] && return 1
    for f in $(wifi_files); do
      case "$f" in
        */wpa_supplicant/*) grep -Eq '^[[:space:]]*(psk|password)=' "$f" && return 1 ;;
        *) grep -Eq "^[[:space:]]*(auto|allow-hotplug|iface)[[:space:]]+$i([[:space:]]|\$)|^[[:space:]]*wpa-(ssid|psk|conf)" "$f" && return 1 ;;
      esac
    done
  done
  return 0
}
wifi_proof() {
  if [ -n "${NCS_PLATFORM_URL:-}" ]; then
    for _ in 1 2 3 4 5 6; do
      curl -fsS --max-time 15 -o /dev/null "$NCS_PLATFORM_URL/api/agent/download/VERSION" 2>/dev/null && return 0
      sleep 5
    done
    return 1
  fi
  return 1
}
wifi_default_dev() { ip -4 route show default 2>/dev/null | awk '{for(i=1;i<NF;i++) if($i=="dev"){print $(i+1); exit}}'; }
f_ncs_wifi_off_do() {
  ifs="$(wifi_ifaces)"
  [ -n "$ifs" ] || { echo "  no wireless interface here"; return 0; }
  dev="$(wifi_default_dev)"
  case " $(echo $ifs) " in *" $dev "*) echo "  the default route is on the wireless interface $dev: connect a cable first, then run this again" >&2; return 3 ;; esac
  [ -n "$dev" ] || { echo "  there is no default route: nothing is carrying the machine's traffic" >&2; return 3; }
  f_ncs_wired_ok || { echo "  no wired connection is up: connect a cable, then run this again" >&2; return 3; }
  [ -n "${NCS_PLATFORM_URL:-}" ] || { echo "  the platform's address was not passed in, so the change cannot be proved safe" >&2; return 3; }
  # 1. wireless interfaces down (reversible)
  for i in $ifs; do ip link set "$i" down; done
  # 2. proof: the platform must still be reachable by the wired path
  if ! wifi_proof; then
    for i in $ifs; do ip link set "$i" up; done
    echo "  the platform could not be reached with wireless off, so the wireless interfaces were switched back on" >&2
    return 1
  fi
  # 3. only now remove what remembers the network (not undoable)
  for i in $ifs; do
    for f in $(wifi_files); do
      case "$f" in
        */wpa_supplicant/*) grep -Eq '^[[:space:]]*(psk|password)=' "$f" && rm -f "$f" ;;
        *)
          awk -v ifc="$i" '
            function starts(l) { return l ~ /^[[:space:]]*(auto|allow-[a-z]+|iface|source|source-directory|mapping)[[:space:]]/ }
            { if (starts($0)) skip = ($0 ~ "^[[:space:]]*(auto|allow-[a-z]+|iface)[[:space:]]+" ifc "([[:space:]]|$)")
              if (skip) next
              print }' "$f" >"$f.nuit-cloud.new" && cat "$f.nuit-cloud.new" >"$f"; rm -f "$f.nuit-cloud.new" ;;
      esac
    done
  done
  command -v rfkill >/dev/null 2>&1 && rfkill block wifi 2>/dev/null || true
  echo "  wireless interfaces ($(echo $ifs)) are off, nothing remembers the network, and the platform answers over $dev"
}
