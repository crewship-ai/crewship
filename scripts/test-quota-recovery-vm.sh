#!/usr/bin/env bash
# Explicit owned-VM acceptance only. Restarts Docker and reboots that guest.
# Never run against a shared dev/prod host. Uses only quota-vm-owned resources.
set -euo pipefail
[[ ${CREWSHIP_OWNED_QUOTA_VM:-} == 1 ]] || { echo 'explicit owned guest authorization required' >&2;exit 2; }
QUOTA_VM_KEY=${1:?SSH key}; QUOTA_VM_PORT=${2:?local forwarded guest port}
[[ $QUOTA_VM_PORT =~ ^[0-9]+$ && $QUOTA_VM_PORT -gt 1024 && $QUOTA_VM_PORT -lt 65536 ]] || exit 2
QUOTA_SSH=(ssh -p "$QUOTA_VM_PORT" -i "$QUOTA_VM_KEY" -o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=2 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null acceptance@127.0.0.1)
run_stage() {
 "${QUOTA_SSH[@]}" "sudo setpriv --reuid=1000 --regid=1000 --groups=\"\$(stat -c %g /var/run/docker.sock)\" env CREWSHIP_QUOTA_VM_STAGE=$1 CREWSHIP_QUOTA_VM_SOCKET=/run/crewship-quota/quota-acceptance/helper.sock CREWSHIP_QUOTA_VM_STATE=/var/lib/crewship-quota-probe-fixture/state.json /var/lib/crewship-quota-probe-fixture/crewship-quota-recovery-tests -test.v -test.run ^TestVMQuotaServiceRecovery\$"
}
wait_guest() {
 for ((attempt=0;attempt<60;attempt++)); do
  if timeout 15 "${QUOTA_SSH[@]}" 'systemctl is-active --quiet docker && systemctl is-active --quiet crewship-quota-helper@quota-acceptance.service';then return;fi
  sleep 2
 done
 echo 'owned guest recovery timed out' >&2;exit 1
}
# Build the fixture with: go test -c -tags quota_vm ./internal/provider/docker
# Binaries, installer and production unit must already be installed in guest.
# Reject an untagged binary before creating resources or restarting the guest.
QUOTA_VM_TESTS=$("${QUOTA_SSH[@]}" "/var/lib/crewship-quota-probe-fixture/crewship-quota-recovery-tests -test.list '^TestVMQuotaServiceRecovery$'")
[[ $QUOTA_VM_TESTS == TestVMQuotaServiceRecovery ]] || { echo 'guest test binary must include -tags quota_vm' >&2; exit 2; }
run_stage create
"${QUOTA_SSH[@]}" 'sudo systemctl restart docker'
run_stage pre-controller
run_stage recover
"${QUOTA_SSH[@]}" 'sudo systemctl reboot' || true
sleep 2;wait_guest
run_stage pre-controller
run_stage recover
run_stage stop
"${QUOTA_SSH[@]}" 'sudo systemctl restart docker'
run_stage stopped-pre-controller
"${QUOTA_SSH[@]}" 'sudo systemctl reboot' || true
sleep 2;wait_guest
run_stage stopped-pre-controller
run_stage cleanup
printf 'Quota provider/helper real UID1000 recovery, pre-controller stopped policy and durable physical canary passed.\n'
