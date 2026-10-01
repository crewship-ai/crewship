#!/usr/bin/env bash
# Operate only on an explicitly owned QEMU guest, never the host Docker daemon.
set -euo pipefail
cd "$(dirname "$0")/.."
fixture=${1:?Usage: acceptance-service-recovery-vm.sh /tmp/crewship-service-vm.XXXXXX}
fixture=$(realpath "$fixture")
[[ "$fixture" == /tmp/crewship-* ]] || { echo 'Expected an owned /tmp/crewship-* fixture' >&2; exit 1; }
[[ -f "$fixture/qemu.pid" && -f "$fixture/guest.qcow2" && -f "$fixture/ssh-key" && -f "$fixture/known-hosts" ]]
guest_pid=$(sudo cat "$fixture/qemu.pid")
[[ "$guest_pid" =~ ^[0-9]+$ ]]
guest_command=$(sudo cat "/proc/$guest_pid/cmdline" | tr '\0' ' ')
[[ "$guest_command" == *qemu-system-x86_64* && "$guest_command" == *"$fixture/guest.qcow2"* && "$guest_command" == *hostfwd=tcp:127.0.0.1:22291-:22* ]] || {
  echo 'PID is not the owned guest on the dedicated forwarded port' >&2; exit 1;
}
ssh_args=(-i "$fixture/ssh-key" -o BatchMode=yes -o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=1 -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$fixture/known-hosts")
guest() { ssh "${ssh_args[@]}" -p 22291 acceptance@127.0.0.1 "$@"; }
guest 'test -f /var/lib/crewship-acceptance-ready && test ! -e /srv/crewship/crewship_1'
local_machine=$(cat /etc/machine-id)
guest_machine=$(guest 'cat /etc/machine-id')
[[ -n "$guest_machine" && "$guest_machine" != "$local_machine" ]] || { echo 'Host and guest identities must differ' >&2; exit 1; }
CGO_ENABLED=0 go test -c -tags service_recovery_vm -o "$fixture/service-recovery.test" ./internal/api
scp "${ssh_args[@]}" -P 22291 "$fixture/service-recovery.test" acceptance@127.0.0.1:/home/acceptance/service-recovery.test
phase() {
  if [[ "${CREWSHIP_SERVICE_VM_QUOTA:-0}" == 1 ]]; then
    guest 'sudo install -d -o 1000 -g 1000 -m 0700 /var/lib/crewship-service-quota-acceptance && systemctl is-active --quiet crewship-quota-helper@quota-acceptance.service'
    guest "sudo setpriv --reuid=1000 --regid=1000 --groups=\"\$(stat -c %g /var/run/docker.sock)\" env HOME=/home/acceptance CREWSHIP_SERVICE_VM_QUOTA=1 CREWSHIP_SERVICE_VM_PHASE=$1 /home/acceptance/service-recovery.test -test.run=^TestServiceRecoveryVM\$ -test.v -test.timeout=5m" | tee "$fixture/quota-controller-$2.log"
  else
    guest "sudo env CREWSHIP_SERVICE_VM_PHASE=$1 /home/acceptance/service-recovery.test -test.run=^TestServiceRecoveryVM\$ -test.v -test.timeout=5m" | tee "$fixture/service-$2.log"
  fi
}
if [[ "${CREWSHIP_SERVICE_VM_RESUME:-0}" == 1 ]]; then phase start restart-owned-fixture
elif [[ "${CREWSHIP_SERVICE_VM_RESUME:-0}" == initialize ]]; then phase initialize initialize-owned-fixture
else phase seed seed; fi
phase verify-running process-restart
guest 'test -f /var/lib/crewship-acceptance-ready && sudo systemctl restart docker'
phase verify-running docker-restart
boot_before=$(guest 'cat /proc/sys/kernel/random/boot_id')
guest 'test -f /var/lib/crewship-acceptance-ready && sudo reboot' || true
for ((i=0;i<90;i++)); do
  if boot_after=$(guest 'cat /proc/sys/kernel/random/boot_id' 2>/dev/null) && [[ "$boot_after" != "$boot_before" ]]; then break; fi
  sleep 2
done
[[ -n "${boot_after:-}" && "$boot_after" != "$boot_before" ]] || { echo 'Guest did not reboot' >&2; exit 1; }
guest 'for i in $(seq 1 30); do sudo systemctl is-active --quiet docker && exit 0; sleep 1; done; exit 1'
phase verify-running host-reboot
phase stop stop
guest 'test -f /var/lib/crewship-acceptance-ready && sudo systemctl restart docker'
phase verify-stopped stop-docker-restart
boot_before=$(guest 'cat /proc/sys/kernel/random/boot_id')
guest 'test -f /var/lib/crewship-acceptance-ready && sudo reboot' || true
for ((i=0;i<90;i++)); do
  if boot_after=$(guest 'cat /proc/sys/kernel/random/boot_id' 2>/dev/null) && [[ "$boot_after" != "$boot_before" ]]; then break; fi
  sleep 2
done
[[ -n "${boot_after:-}" && "$boot_after" != "$boot_before" ]] || exit 1
guest 'for i in $(seq 1 30); do sudo systemctl is-active --quiet docker && exit 0; sleep 1; done; exit 1'
phase verify-stopped stop-host-reboot
echo 'PASS: application controller reattached once, retained data, kept Stop, and woke no agent across process/Docker/guest reboots'
