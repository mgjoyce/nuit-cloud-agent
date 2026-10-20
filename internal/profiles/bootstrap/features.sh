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
f_ncs_packages_ok() { dpkg -s ca-certificates >/dev/null 2>&1 && dpkg -s curl >/dev/null 2>&1 && dpkg -s iproute2 >/dev/null 2>&1; }
f_ncs_packages_do() { apt-get update -qq && apt-get install -y -qq ca-certificates curl iproute2; }

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
  mode="--policy-dir $dir/policy"
  [ -e "$(p "$OCISAAS_ETC/installed-bundle")" ] && mode="--keep-policy --restart"
  # shellcheck disable=SC2086
  "$dir/packaging/dedicated/install.sh" --bin "$dir/bin" --guest "$dir/guest" --artifacts-pub "$dir/artifacts.pub" \
    --policy-pub "$dir/policy.pub" $mode --cni-bin /usr/lib/cni --add-user "$NC_USER" \
    || { rm -rf "$work"; echo "  the bundle's install.sh failed (its output is above)" >&2; return 1; }
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
