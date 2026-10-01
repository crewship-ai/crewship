#!/usr/bin/env bash
# Installs a private helper per database instance. Does not start/restart services
# unless --activate is explicit. Run only as a reviewed host administrator.
set -Eeuo pipefail
INSTANCE=""; SERVER_UNIT=""; SERVER_UID=1000; CAPACITY_BYTES=17179869184; HEADROOM_BYTES=1073741824; BINARY=""; ACTIVATE=0
# Plain or template units (crewship-ws@3.service); instance part may not be empty.
SERVER_UNIT_RE='^[a-zA-Z0-9][a-zA-Z0-9_.-]*(@[a-zA-Z0-9_.-]+)?\.service$'
while (($#)); do
 case "$1" in
 --instance) INSTANCE="${2:?}"; shift 2;;
 --server-unit) SERVER_UNIT="${2:?}"; shift 2;;
 --server-uid) SERVER_UID="${2:?}"; shift 2;;
 --capacity-bytes) CAPACITY_BYTES="${2:?}"; shift 2;;
 --headroom-bytes) HEADROOM_BYTES="${2:?}"; shift 2;;
 --binary) BINARY="${2:?}"; shift 2;;
 --activate) ACTIVATE=1; shift;;
 *) echo "usage: sudo $0 --instance immutable-database-id --server-unit crewship-1.service|crewship-ws@1.service --binary /path/to/crewship-quota-helper [--server-uid 1000] [--capacity-bytes N] [--headroom-bytes N] [--activate]" >&2;exit 2;;
 esac
done
[[ $EUID == 0 && $INSTANCE =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$ && $SERVER_UNIT =~ $SERVER_UNIT_RE ]] || { echo 'root and safe instance/server-unit required' >&2;exit 2; }
[[ $SERVER_UID =~ ^[0-9]{1,10}$ && $SERVER_UID != 1001 && $SERVER_UID != 1002 && $SERVER_UID -gt 0 && $SERVER_UID -le 4294967295 ]] || { echo 'invalid trusted server UID' >&2;exit 2; }
[[ $CAPACITY_BYTES =~ ^[0-9]{1,13}$ && $HEADROOM_BYTES =~ ^[0-9]{1,13}$ && $CAPACITY_BYTES -ge 33554432 && $CAPACITY_BYTES -le 1099511627776 && $HEADROOM_BYTES -le 1099511627776 ]] || { echo 'invalid aggregate reservation/floor' >&2;exit 2; }
[[ $BINARY == /* && -f $BINARY && ! -L $BINARY && -x $BINARY ]] || { echo 'absolute regular executable binary required' >&2;exit 2; }
for tool in systemctl mount umount mkfs.ext4 losetup python3; do command -v "$tool" >/dev/null || { echo "missing $tool" >&2;exit 2; };done
SCRIPT_DIR=$(cd -- "$(dirname -- "$0")" && pwd)
# Refuse symlink aliases in every destination ancestor before installation.
python3 - "$INSTANCE" "$SERVER_UNIT" <<'PY'
import os, stat, sys
for target in ['/usr/local/libexec','/etc/crewship-quota','/var/lib/crewship-quota/'+sys.argv[1],'/run/crewship-quota/'+sys.argv[1],'/etc/systemd/system/'+sys.argv[2]+'.d']:
    cur='/'
    for part in target.strip('/').split('/'):
        cur=os.path.join(cur,part)
        if not os.path.lexists(cur): continue
        s=os.lstat(cur)
        if not stat.S_ISDIR(s.st_mode) or s.st_uid != 0 or s.st_mode & 0o022:
            raise SystemExit('untrusted installation directory: '+cur)
for target in ['/usr/local/libexec/crewship-quota-helper','/etc/systemd/system/crewship-quota-helper@.service','/etc/crewship-quota/'+sys.argv[1]+'.env','/etc/systemd/system/'+sys.argv[2]+'.d/quota-helper.conf']:
    if not os.path.lexists(target): continue
    s=os.lstat(target)
    if not stat.S_ISREG(s.st_mode) or s.st_uid != 0 or s.st_mode & 0o022:
        raise SystemExit('untrusted installation file: '+target)
PY
install -d -o root -g root -m 0755 /usr/local/libexec /etc/crewship-quota /var/lib/crewship-quota /run/crewship-quota
install -d -o root -g root -m 0700 "/var/lib/crewship-quota/$INSTANCE"
install -d -o root -g root -m 0755 "/run/crewship-quota/$INSTANCE" "/etc/systemd/system/$SERVER_UNIT.d"
# BEGIN shared-install
# The helper binary and its unit are shared by every instance on the host.
# Both are staged under temporary names first; each existing file is kept
# as a hard-link backup before an atomic rename replaces it, and any later
# failure puts the previous pair back, so no instance restarts into a new
# binary under an old unit or the reverse.
SHARED_DESTS=()
shared_rollback() {
 local dest
 for dest in "${SHARED_DESTS[@]}"; do
  if [[ -e $dest.crewship-prev ]]; then mv -f -- "$dest.crewship-prev" "$dest"; else rm -f -- "$dest"; fi
 done
 SHARED_DESTS=()
}
on_shared_error() {
 trap - ERR
 shared_rollback
 systemctl daemon-reload || true
 echo "installation failed; previous shared helper files restored" >&2
 exit "$1"
}
arm_shared_rollback() { trap 'on_shared_error $?' ERR; }
# install_shared SRC DEST MODE [SRC DEST MODE...]
install_shared() {
 local -a staged=() dests=()
 local tmp i
 while (($#)); do
  tmp=$(mktemp "${2%/*}/.${2##*/}.XXXXXX") || { rm -f -- "${staged[@]}"; return 1; }
  staged+=("$tmp"); dests+=("$2")
  install -o root -g root -m "$3" "$1" "$tmp" || { rm -f -- "${staged[@]}"; return 1; }
  shift 3
 done
 for i in "${!dests[@]}"; do
  if [[ -e ${dests[i]} ]]; then ln -f -- "${dests[i]}" "${dests[i]}.crewship-prev"; else rm -f -- "${dests[i]}.crewship-prev"; fi
  SHARED_DESTS+=("${dests[i]}")
  mv -f -- "${staged[i]}" "${dests[i]}"
 done
}
commit_shared() {
 local dest
 trap - ERR
 for dest in "${SHARED_DESTS[@]}"; do rm -f -- "$dest.crewship-prev"; done
 SHARED_DESTS=()
}
# END shared-install
# Binary is shared code only; catalog/socket/config remain per instance.
arm_shared_rollback
install_shared "$BINARY" /usr/local/libexec/crewship-quota-helper 0755 \
 "$SCRIPT_DIR/../packaging/crewship-quota-helper@.service" /etc/systemd/system/crewship-quota-helper@.service 0644
printf 'SERVER_UID=%s\nCAPACITY_BYTES=%s\nHEADROOM_BYTES=%s\n' "$SERVER_UID" "$CAPACITY_BYTES" "$HEADROOM_BYTES" > "/etc/crewship-quota/$INSTANCE.env"
chmod 0600 "/etc/crewship-quota/$INSTANCE.env"
cat > "/etc/systemd/system/$SERVER_UNIT.d/quota-helper.conf" <<UNIT
[Unit]
Wants=crewship-quota-helper@$INSTANCE.service
After=crewship-quota-helper@$INSTANCE.service
[Service]
Environment=CREWSHIP_QUOTA_HELPER_NAMESPACE=$INSTANCE
Environment=CREWSHIP_QUOTA_HELPER_SOCKET=/run/crewship-quota/$INSTANCE/helper.sock
UNIT
chmod 0644 "/etc/systemd/system/$SERVER_UNIT.d/quota-helper.conf"
systemctl daemon-reload
systemctl enable "crewship-quota-helper@$INSTANCE.service"
commit_shared
if ((ACTIVATE)); then systemctl start "crewship-quota-helper@$INSTANCE.service";fi
printf 'Installed private catalog %s; server configuration applies at its next restart. No server or Docker restart performed.\n' "$INSTANCE"
