#!/bin/sh
# Post-install scriptlet for the crewship .deb / .rpm packages.
#
# Deliberately minimal (issue #858 phase 4: "nothing magical, no auto-enable
# without consent"). It creates the service account the bundled systemd unit
# runs as and secures the config dir. It NEVER enables or starts a stopped
# service — the operator opts in explicitly after populating the secrets. On
# an upgrade it only `try-restart`s a service that is ALREADY running, so the
# new binary takes effect (and its pre-migration snapshot runs) without ever
# activating something the operator turned off.
set -e

CREWSHIP_USER=crewship
CREWSHIP_GROUP=crewship
CONFIG_DIR=/etc/crewship
ENV_FILE=${CONFIG_DIR}/crewship.env

# Create a locked, no-login system group + user if they don't already exist.
# useradd/groupadd exist on both Debian- and RPM-based distros; guard with
# getent so a reinstall/upgrade is idempotent.
if ! getent group "${CREWSHIP_GROUP}" >/dev/null 2>&1; then
    groupadd --system "${CREWSHIP_GROUP}" || true
fi
if ! getent passwd "${CREWSHIP_USER}" >/dev/null 2>&1; then
    useradd --system --gid "${CREWSHIP_GROUP}" \
        --home-dir /var/lib/crewship --no-create-home \
        --shell /usr/sbin/nologin \
        --comment "Crewship service account" "${CREWSHIP_USER}" || true
fi

# The env file holds secrets — keep it readable only by the service account.
if [ -f "${ENV_FILE}" ]; then
    chown "${CREWSHIP_USER}:${CREWSHIP_GROUP}" "${ENV_FILE}" 2>/dev/null || true
    chmod 0640 "${ENV_FILE}" 2>/dev/null || true
fi
chown "${CREWSHIP_USER}:${CREWSHIP_GROUP}" "${CONFIG_DIR}" 2>/dev/null || true

if command -v systemctl >/dev/null 2>&1; then
    # Pick up the unit (and any changes to it) so `systemctl enable crewship`
    # works right away.
    systemctl daemon-reload >/dev/null 2>&1 || true

    if systemctl is-active --quiet crewship.service 2>/dev/null; then
        # Upgrade of a running install: swap onto the new binary. try-restart
        # is a no-op if the service is stopped, so this never activates an
        # opted-out service.
        echo "Restarting the running crewship service onto the new binary..."
        systemctl try-restart crewship.service || true
    else
        # Fresh install (or a stopped service): guide, do not start.
        if grep -qE '^(NEXTAUTH_SECRET|ENCRYPTION_KEY)=$' "${ENV_FILE}" 2>/dev/null || [ ! -f "${ENV_FILE}" ]; then
            cat <<'EOF'

Crewship installed. Before starting the service:

  1. Set the required secrets in /etc/crewship/crewship.env
     (NEXTAUTH_SECRET and ENCRYPTION_KEY). Generate each with:
       openssl rand -hex 32

  2. Enable + start when ready (not done automatically):
       sudo systemctl enable --now crewship

  Or try it in the foreground without Docker:
       sudo -u crewship CREWSHIP_DATA_DIR=/var/lib/crewship crewship start --no-docker

  Docs: https://docs.crewship.ai/guides/upgrades
EOF
        else
            echo "Crewship installed. Start when ready: sudo systemctl enable --now crewship"
        fi
    fi
fi

exit 0
