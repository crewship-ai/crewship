#!/bin/sh
set -eu
umask 077
helper_binary=${QUOTA_HELPER_BINARY:?set QUOTA_HELPER_BINARY to the built command}
probe_root=$(mktemp -d /tmp/crewship-quota-startup.XXXXXX)
helper_pid=
cleanup() {
  if [ -n "$helper_pid" ]; then kill -TERM "$helper_pid" 2>/dev/null || true; wait "$helper_pid" 2>/dev/null || true; fi
  rm -rf "$probe_root"
}
trap cleanup EXIT
fail_case() {
  case_name=$1
  expected=$2
  shift 2
  set +e
  "$helper_binary" "$@" > "${probe_root}/stdout" 2> "${probe_root}/stderr"
  code=$?
  set -e
  if [ "$code" -ne 1 ] || ! grep -F "$expected" "${probe_root}/stderr" >/dev/null; then
    cat "${probe_root}/stderr"
    echo "FAIL $case_name exit=$code"
    exit 1
  fi
  echo "PASS $case_name"
}
mkdir -m 700 "${probe_root}/catalog" "${probe_root}/recovery" "${probe_root}/clean" "${probe_root}/quarantined"
fail_case missing-namespace 'namespace required'
fail_case oversized-uid 'invalid server UID' --namespace fixture --server-uid 4294967296
fail_case missing-catalog 'quota catalog unavailable' --namespace fixture --root "${probe_root}/absent"
fail_case invalid-namespace 'quota namespace denied' --namespace invalid/name --root "${probe_root}/catalog"
fail_case unsafe-peer-uid 'quota helper failed' --namespace fixture --root "${probe_root}/catalog" --socket "${probe_root}/catalog/helper.sock" --server-uid 1001
fail_case namespace-rebinding 'quota namespace denied' --namespace other --root "${probe_root}/catalog"
mkdir -m 700 "${probe_root}/recovery/images" "${probe_root}/recovery/mounts"
printf fixture > "${probe_root}/recovery/namespace"
printf invalid > "${probe_root}/recovery/images/broken.json"
printf blocked > "${probe_root}/recovery/quarantine"
fail_case recovery-failure 'quota catalog recovery failed' --namespace fixture --root "${probe_root}/recovery"
NOTIFY_SOCKET="${probe_root}/missing-notify"; export NOTIFY_SOCKET
fail_case readiness-failure 'quota helper failed' --namespace fixture --root "${probe_root}/clean" --socket "${probe_root}/clean/helper.sock"
unset NOTIFY_SOCKET
mkdir -m 700 "${probe_root}/quarantined/images" "${probe_root}/quarantined/mounts"
printf fixture > "${probe_root}/quarantined/namespace"
printf invalid > "${probe_root}/quarantined/images/broken.json"
"$helper_binary" --namespace fixture --root "${probe_root}/quarantined" --socket "${probe_root}/quarantined/helper.sock" > "${probe_root}/success.stdout" 2> "${probe_root}/success.stderr" &
helper_pid=$!
ready=0
quota_attempt=0
while [ "$quota_attempt" -lt 200 ]; do
  quota_attempt=$((quota_attempt + 1))
  if [ -S "${probe_root}/quarantined/helper.sock" ]; then ready=1; break; fi
  if ! kill -0 "$helper_pid" 2>/dev/null; then cat "${probe_root}/success.stderr"; exit 1; fi
  sleep 0.01
done
if [ "$ready" -ne 1 ]; then echo 'FAIL readiness deadline'; exit 1; fi
kill -TERM "$helper_pid"
wait "$helper_pid"
helper_pid=
if ! grep -F 'quarantined' "${probe_root}/success.stderr" >/dev/null; then cat "${probe_root}/success.stderr"; exit 1; fi
if [ -e "${probe_root}/quarantined/helper.sock" ]; then echo 'FAIL stale socket'; exit 1; fi
if [ -e "${probe_root}/quarantined/images/broken.json" ]; then echo 'FAIL invalid metadata not quarantined'; exit 1; fi
echo 'PASS quarantine-readiness-graceful-shutdown'
