#!/usr/bin/env bash
# Disposable hosted acceptance probe, not a publishing or container boot helper.
set -euo pipefail
out="${RUNNER_TEMP:?}/classic-store-probe"
mkdir -p "$out"
exec > >(tee "$out/probe.log") 2>&1
blocked() { echo "BLOCKED: $*"; exit 78; }
unverified() { echo "UNVERIFIED: $*"; exit 1; }
[[ "$IMAGE" == ghcr.io/crewship-ai/crewship@sha256:b74257f22023eb498852d9f4a024c749aff5780bd3961dd5e3b37494b283ae13 ]] || blocked 'unexpected image target'
[[ "$SHA" == ba1fc351c363e9946e95604e1f4c233adeac6925 ]] || blocked 'unexpected source identity'
case "$CHECKER" in old|fixed) ;; *) blocked 'unknown checker' ;; esac
printf 'run=%s attempt=%s job=%s checker=%s\nhead=%s\nimage=%s\nsource=%s\n' \
  "$GITHUB_RUN_ID" "$GITHUB_RUN_ATTEMPT" "$GITHUB_JOB" "$CHECKER" "$(git rev-parse HEAD)" "$IMAGE" "$SHA"
docker version
docker info --format '{"ServerVersion":{{json .ServerVersion}},"Driver":{{json .Driver}},"DriverStatus":{{json .DriverStatus}},"OperatingSystem":{{json .OperatingSystem}},"KernelVersion":{{json .KernelVersion}}}' > "$out/docker-info.json"
python3 - "$out/docker-info.json" <<'PY'
import json, sys
info = json.load(open(sys.argv[1]))
print('Docker identity:', info['ServerVersion'], info['Driver'], info['DriverStatus'])
if not (info['ServerVersion'].startswith('28.') and info['Driver'] == 'overlay2'
        and not any('containerd' in str(row).lower() for row in info['DriverStatus'] or [])):
    print('BLOCKED: require actual Docker 28.x classic overlay2; no daemon reconfiguration')
    sys.exit(78)
PY
# This contacts the anonymous registry, not the daemon: it primes no image cache.
timeout 120 docker manifest inspect "$IMAGE" > "$out/index.json" || unverified 'immutable image unavailable; no target substitution'
for platform in linux/amd64 linux/arm64; do
  python3 scripts/ci/image-platform.py "$IMAGE" "$platform" < "$out/index.json" \
    > "$out/${platform#linux/}-ref.txt" || unverified "cannot resolve $platform"
  printf '%s child=' "$platform"; cat "$out/${platform#linux/}-ref.txt"
done
[[ "$(cat "$out/amd64-ref.txt")" == ghcr.io/crewship-ai/crewship@sha256:d072f344ad5ff26b6cf65f894b8a77cb3d35b6de95400a0ef109b4423e889751 ]] || unverified 'unexpected AMD child'
[[ "$(cat "$out/arm64-ref.txt")" == ghcr.io/crewship-ai/crewship@sha256:651ccbb2a9f542e3e22fbe53d11b2352eff9f51fe6335e95af49b459d636dd71 ]] || unverified 'unexpected ARM child'
# Cache hits would weaken acquisition evidence even on nominally fresh VMs.
for ref in "$IMAGE" "$(cat "$out/amd64-ref.txt")" "$(cat "$out/arm64-ref.txt")"; do
  if docker image inspect "$ref" >/dev/null 2>&1; then
    blocked "target already cached before checker: $ref"
  fi
done
printf 'platform_resolver_blob=%s\n' "$(git hash-object scripts/ci/image-platform.py)"
old_source=ba1fc351c363e9946e95604e1f4c233adeac6925
fixed_source=bf2ae2bcd4fd9e93f37052cc78f1a111207b627b
old_blob=9b1c3fdd9b45297bd7720a7fe8628683d9a4f132
fixed_blob=b526e706c4f550806e2508a34d7ffa61dba372a1
printf 'old_source=%s old_blob=%s\nfixed_source=%s fixed_blob=%s\n' "$old_source" "$old_blob" "$fixed_source" "$fixed_blob"
[[ "$(git rev-parse "$old_source:scripts/check-release-artifacts.sh")" == "$old_blob" ]] || blocked 'old blob mismatch'
[[ "$(git hash-object scripts/check-release-artifacts.sh)" == "$fixed_blob" ]] || blocked 'fixed blob mismatch'
if [[ "$CHECKER" == old ]]; then
  git show "$old_source:scripts/check-release-artifacts.sh" > "$out/old-checker.sh"
  checker_path="$out/old-checker.sh"
else
  checker_path=scripts/check-release-artifacts.sh
fi
set +e
bash "$checker_path" --strict --image "$IMAGE" > "$out/checker.log" 2>&1
rc=$?
set -e
cat "$out/checker.log"
printf 'strict_checker_exit=%s\n' "$rc"
python3 - "$CHECKER" "$rc" "$out/checker.log" "$IMAGE" <<'PY'
import sys
kind, rc, path, image = sys.argv[1:]
log = open(path).read()
amd = 'image linux/amd64: 141 go texts + 1123 npm texts match their manifests (sha256)'
arm = 'image linux/arm64: 141 go texts + 1123 npm texts match their manifests (sha256)'
verified = f'VERIFIED    image {image}: legal files + manifest texts hash-match on:'
if kind == 'old':
    failure = f'UNVERIFIED  image: linux/arm64 unavailable for {image}:'
    ok = (rc == '1' and amd in log and failure in log
          and log.index(amd) < log.index(failure)
          and verified + ' linux/amd64\n' in log
          and arm not in log and 'FAIL  ' not in log)
    label = 'RED expected old-checker ARM acquisition failure after verified AMD'
else:
    ok = (rc == '0' and amd in log and arm in log
          and verified + ' linux/amd64 linux/arm64\n' in log
          and not any(line.startswith(('UNVERIFIED', 'FAIL  ')) for line in log.splitlines()))
    label = 'GREEN both architectures and every legal manifest hash verified'
print(label if ok else 'UNVERIFIED: strict checker did not satisfy the expected control')
sys.exit(0 if ok else 1)
PY

# Attribute RED to the classic index collision rather than an unrelated pull
# outage. Retry only ARM acquisition; never start the resulting container.
if [[ "$CHECKER" == old ]]; then
  set +e
  timeout 120 docker create --platform linux/arm64 "$IMAGE" /bin/sh \
    > "$out/red-retry-cid.txt" 2> "$out/red-retry-stderr.txt"
  retry_rc=$?
  set -e
  printf 'red_arm_retry_exit=%s\n' "$retry_rc"
  cat "$out/red-retry-stderr.txt"
  retry_cid=$(cat "$out/red-retry-cid.txt")
  if [[ -n "$retry_cid" ]]; then
    [[ "$retry_cid" =~ ^[0-9a-f]{64}$ ]] || unverified 'unexpected retry container receipt'
    timeout 60 docker rm "$retry_cid" || unverified 'could not remove exact retry container'
  fi
  python3 - "$retry_rc" "$out/red-retry-stderr.txt" <<'PY'
import sys
rc, path = sys.argv[1:]
err = open(path).read().lower()
if rc != '1' or 'cannot overwrite digest' not in err:
    sys.exit('UNVERIFIED: old ARM retry did not prove classic-store digest collision')
print('RED mechanism: cannot overwrite digest on ARM index acquisition')
PY
fi
# Inspect image config identity without executing image code. On RED the old
# checker retains the index binding to amd64; GREEN uses the resolved children.
if [[ "$CHECKER" == old ]]; then refs=("$IMAGE"); else
  refs=("$(cat "$out/amd64-ref.txt")" "$(cat "$out/arm64-ref.txt")")
fi
for ref in "${refs[@]}"; do
  revision=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$ref") \
    || unverified "image config unavailable: $ref"
  printf 'config_ref=%s source_revision=%s\n' "$ref" "$revision"
  [[ "$revision" == "$SHA" ]] || unverified 'embedded OCI source revision mismatch'
done
# Checker trap removes precisely its created (never started) containers.
docker ps -a --format '{{.ID}} {{.Status}}' > "$out/remaining-containers.txt"
echo 'Probe complete; no image code was executed and no publication was attempted.'
