#!/usr/bin/env bash
# install-backup-timer.sh — install systemd timers that take a Crewship
# backup once a day and rotate old bundles weekly. Idempotent; re-run
# to update the unit files.
#
# Why timers instead of a Crewship-internal scheduler:
#   The existing schedulers (agents.schedule_cron, pipeline_schedules)
#   fire LLM workloads. A backup is a deterministic, no-LLM operation;
#   driving it through an LLM scheduler would burn Anthropic credits
#   and add a non-deterministic dependency to a data-integrity path.
#   systemd timers match prod's existing release-pull timer setup and
#   keep the backup path operator-owned.
#
# Usage:
#   sudo ./scripts/install-backup-timer.sh \
#     --workspace <workspace-id> \
#     [--user <linux-user>] \
#     [--keep-last 30] \
#     [--cron-style daily|hourly] \
#     [--dry-run]
#
# Required:
#   --workspace        Workspace ID to back up (run `crewship workspaces`
#                      list to find it). Multi-workspace setups: install
#                      this script once per workspace with a distinct
#                      service+timer name pair (script auto-suffixes the
#                      unit names with the workspace id).
#
# Optional:
#   --user             Linux user that owns ~/.crewship/. Defaults to
#                      the user invoking sudo (SUDO_USER) or 'ubuntu'.
#   --keep-last        Bundles to retain on weekly rotate. Default 30.
#   --cron-style       'daily' (default; runs nightly with a 30 min
#                      randomized delay so multi-host installs don't
#                      thunder) or 'hourly' (for high-churn workspaces).
#   --dry-run          Print the unit files + commands; don't install.
#
# Exit codes:
#   0   installed (or dry-run completed)
#   2   missing required arg / bad input
#   3   systemd not present
#   4   crewship binary not on PATH for target user

set -euo pipefail

WORKSPACE_ID=""
TARGET_USER="${SUDO_USER:-${USER:-ubuntu}}"
KEEP_LAST=30
CRON_STYLE="daily"
DRY_RUN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --workspace)   WORKSPACE_ID="$2"; shift 2 ;;
    --user)        TARGET_USER="$2"; shift 2 ;;
    --keep-last)   KEEP_LAST="$2"; shift 2 ;;
    --cron-style)  CRON_STYLE="$2"; shift 2 ;;
    --dry-run)     DRY_RUN=1; shift ;;
    -h|--help)
      sed -n '2,40p' "$0"
      exit 0
      ;;
    *)
      echo "error: unknown flag: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "${WORKSPACE_ID}" ]]; then
  echo "error: --workspace is required" >&2
  echo "       run 'crewship workspaces' as ${TARGET_USER} to list ids" >&2
  exit 2
fi

# Bail early on non-systemd hosts. Crewship technically runs on macOS dev
# boxes (launchd) but those have their own /System/Library/LaunchDaemons
# convention and need a different installer.
if ! command -v systemctl >/dev/null 2>&1; then
  echo "error: systemctl not found — this installer is Linux-only" >&2
  exit 3
fi

# Verify the target user can actually invoke the crewship binary.
# `command -v` inside `runuser` honors the user's PATH; PATH inside a
# systemd service unit does NOT inherit the login PATH by default, so we
# resolve the binary now and burn the absolute path into ExecStart.
CREWSHIP_BIN=$(runuser -l "${TARGET_USER}" -c 'command -v crewship' 2>/dev/null || true)
if [[ -z "${CREWSHIP_BIN}" ]]; then
  echo "error: 'crewship' not on PATH for user ${TARGET_USER}" >&2
  echo "       install or symlink the binary into /usr/local/bin first" >&2
  exit 4
fi

# Suffix unit names with a stable hash of the workspace id rather than
# the literal first 8 characters. Crewship's CUID-style ids share a
# common prefix during the same epoch second (timestamp-derived), so a
# naive substring suffix collides between workspaces created back to
# back. sha256 -> first 8 hex chars has ~2^-32 collision odds — fine
# for unit-file uniqueness on a single host.
if command -v sha256sum >/dev/null 2>&1; then
  WS_SUFFIX="$(printf '%s' "${WORKSPACE_ID}" | sha256sum | cut -c1-8)"
else
  # macOS / BSD fallback: shasum is in the default toolchain.
  WS_SUFFIX="$(printf '%s' "${WORKSPACE_ID}" | shasum -a 256 | cut -c1-8)"
fi
SERVICE_NAME="crewship-backup@${WS_SUFFIX}.service"
TIMER_NAME="crewship-backup@${WS_SUFFIX}.timer"
ROTATE_SERVICE="crewship-backup-rotate@${WS_SUFFIX}.service"
ROTATE_TIMER="crewship-backup-rotate@${WS_SUFFIX}.timer"

# Daily by default; --cron-style hourly is for operators running heavy
# write workloads where 24h between snapshots is too coarse.
if [[ "${CRON_STYLE}" == "hourly" ]]; then
  TIMER_ONCALENDAR="hourly"
else
  TIMER_ONCALENDAR="daily"
fi

read -r -d '' BACKUP_SERVICE <<EOF || true
[Unit]
Description=Crewship daily backup (workspace ${WS_SUFFIX})
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
User=${TARGET_USER}
# Resolve absolute binary path at install time so a future PATH change
# on the target user doesn't silently disable the timer.
ExecStart=${CREWSHIP_BIN} backup create --workspace ${WORKSPACE_ID} --scope workspace
# Backup write touches ~/.crewship/backups; keep it user-scoped.
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/home/${TARGET_USER}/.crewship
PrivateTmp=yes
NoNewPrivileges=yes
# Defence-in-depth: backup needs IP networking (catalog reconcile) and
# nothing else from the kernel surface. Disabling each capability below
# is a no-op for the documented workload — they're guards against a
# future regression where someone adds e.g. a kernel-module probe to
# the backup binary and the test suite doesn't catch it.
RestrictAddressFamilies=AF_INET AF_INET6
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictNamespaces=yes
RestrictRealtime=yes
LockPersonality=yes

[Install]
WantedBy=multi-user.target
EOF

read -r -d '' BACKUP_TIMER <<EOF || true
[Unit]
Description=Crewship backup timer (workspace ${WS_SUFFIX}, ${TIMER_ONCALENDAR})

[Timer]
OnCalendar=${TIMER_ONCALENDAR}
# 30-min jitter so a fleet of hosts (or two instances on one VM) don't
# fire backups at the same wall-clock second and contend on
# ~/.crewship/backups disk + SQLite WAL.
RandomizedDelaySec=30min
# If the host was off when the timer should have fired, run on next boot.
Persistent=true
Unit=${SERVICE_NAME}

[Install]
WantedBy=timers.target
EOF

read -r -d '' ROTATE_SVC <<EOF || true
[Unit]
Description=Crewship backup rotation (workspace ${WS_SUFFIX}, keep last ${KEEP_LAST})
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
User=${TARGET_USER}
ExecStart=${CREWSHIP_BIN} backup rotate --workspace ${WORKSPACE_ID} --keep-last ${KEEP_LAST}
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/home/${TARGET_USER}/.crewship
PrivateTmp=yes
NoNewPrivileges=yes
# Same hardening surface as the create service — rotate is a strict
# subset (it only deletes / inspects on-disk bundles + emits a
# catalog row, no extra capabilities needed).
RestrictAddressFamilies=AF_INET AF_INET6
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictNamespaces=yes
RestrictRealtime=yes
LockPersonality=yes

[Install]
WantedBy=multi-user.target
EOF

read -r -d '' ROTATE_TMR <<EOF || true
[Unit]
Description=Crewship backup rotation timer (workspace ${WS_SUFFIX}, weekly)

[Timer]
OnCalendar=weekly
RandomizedDelaySec=2h
Persistent=true
Unit=${ROTATE_SERVICE}

[Install]
WantedBy=timers.target
EOF

if [[ "${DRY_RUN}" == "1" ]]; then
  echo "# DRY RUN — would write:"
  echo
  echo "### /etc/systemd/system/${SERVICE_NAME}"
  echo "${BACKUP_SERVICE}"
  echo
  echo "### /etc/systemd/system/${TIMER_NAME}"
  echo "${BACKUP_TIMER}"
  echo
  echo "### /etc/systemd/system/${ROTATE_SERVICE}"
  echo "${ROTATE_SVC}"
  echo
  echo "### /etc/systemd/system/${ROTATE_TIMER}"
  echo "${ROTATE_TMR}"
  echo
  echo "# Then: systemctl daemon-reload && systemctl enable --now ${TIMER_NAME} ${ROTATE_TIMER}"
  exit 0
fi

# Non-dry-run path: write the units, reload, enable, and report.
if [[ "$(id -u)" -ne 0 ]]; then
  echo "error: writing to /etc/systemd/system requires root — re-run with sudo" >&2
  exit 2
fi

UNIT_DIR=/etc/systemd/system
printf '%s\n' "${BACKUP_SERVICE}" > "${UNIT_DIR}/${SERVICE_NAME}"
printf '%s\n' "${BACKUP_TIMER}"   > "${UNIT_DIR}/${TIMER_NAME}"
printf '%s\n' "${ROTATE_SVC}"     > "${UNIT_DIR}/${ROTATE_SERVICE}"
printf '%s\n' "${ROTATE_TMR}"     > "${UNIT_DIR}/${ROTATE_TIMER}"
chmod 0644 "${UNIT_DIR}/${SERVICE_NAME}" "${UNIT_DIR}/${TIMER_NAME}" \
           "${UNIT_DIR}/${ROTATE_SERVICE}" "${UNIT_DIR}/${ROTATE_TIMER}"

systemctl daemon-reload
systemctl enable --now "${TIMER_NAME}" "${ROTATE_TIMER}"

echo
echo "installed:"
echo "  ${UNIT_DIR}/${SERVICE_NAME}"
echo "  ${UNIT_DIR}/${TIMER_NAME}"
echo "  ${UNIT_DIR}/${ROTATE_SERVICE}"
echo "  ${UNIT_DIR}/${ROTATE_TIMER}"
echo
echo "next fire:"
systemctl list-timers "${TIMER_NAME}" "${ROTATE_TIMER}" --no-pager | head -5
echo
echo "remove later with:"
echo "  sudo systemctl disable --now ${TIMER_NAME} ${ROTATE_TIMER}"
echo "  sudo rm ${UNIT_DIR}/${SERVICE_NAME} ${UNIT_DIR}/${TIMER_NAME} ${UNIT_DIR}/${ROTATE_SERVICE} ${UNIT_DIR}/${ROTATE_TIMER}"
echo "  sudo systemctl daemon-reload"
