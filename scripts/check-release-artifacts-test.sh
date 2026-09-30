#!/usr/bin/env bash
# check-release-artifacts-test.sh — regression tests for the artifact checker.
#
# Covers the failures found in the 2026-09-28 reviews:
#   R1 --strict parser loop            (timeout-guarded parse cases)
#   R2 channel independence            (image-only never reads dist)
#   S2 content verification            (real archives with manifests:
#                                       complete → PASS; a missing Go text,
#                                       a missing SCOPED npm text, mutated
#                                       content or an empty manifest → FAIL)
#   S3 truthful VERIFIED               (docker stub: create ok, every cp
#                                       fails → FAIL lines but NO "VERIFIED")
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECK="$REPO_ROOT/scripts/check-release-artifacts.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

expect() { # expect <expected-exit> <description> -- cmd…
  local want="$1" desc="$2"; shift 2; [ "${1:-}" = "--" ] && shift
  local got=0
  timeout 20 "$@" >/dev/null 2>&1 || got=$?
  if [ "$got" -eq 124 ]; then
    echo "TEST FAIL ($desc): timed out (parser loop?)" >&2
    exit 1
  fi
  if [ "$got" -ne "$want" ]; then
    echo "TEST FAIL ($desc): exit $got, want $want" >&2
    exit 1
  fi
  echo "ok  $desc (exit $got)"
}

# ── argument parsing (R1) ──────────────────────────────────────────────────
expect 2 "--strict with no channel is a usage error" -- "$CHECK" --strict
expect 2 "no arguments is a usage error" -- "$CHECK"
expect 2 "invalid flag rejected" -- "$CHECK" --bogus
expect 2 "--dist without value rejected" -- "$CHECK" --dist
expect 2 "--image without value rejected" -- "$CHECK" --image
expect 1 "--strict --dist <missing-dir> fails fast on the missing dir" -- \
  "$CHECK" --strict --dist "$TMP/does-not-exist"

expect 2 "flag cannot be consumed as a dist value" -- "$CHECK" --dist --strict
expect 2 "empty image rejected even with another channel" -- "$CHECK" --dist "$TMP" --image=
expect 0 "equals form selects a dist channel" -- "$CHECK" --dist="$TMP"

# ── channel selection (R2) ─────────────────────────────────────────────────
mkdir -p "$TMP/empty-dist"
expect 0 "empty dist is UNVERIFIED (non-strict) and succeeds" -- \
  "$CHECK" --dist "$TMP/empty-dist"
expect 1 "empty dist fails in strict mode (missing channels are errors)" -- \
  "$CHECK" --strict --dist "$TMP/empty-dist"

mkdir -p "$TMP/tar-only" "$TMP/tarsrc"
echo placeholder > "$TMP/tarsrc/README.md"
tar czf "$TMP/tar-only/crewship_1.0.0_linux_amd64.tar.gz" -C "$TMP/tarsrc" README.md
expect 1 "tar.gz-only dist fails strict (zip channel missing)" -- \
  "$CHECK" --strict --dist "$TMP/tar-only"

mkdir -p "$TMP/bin"
printf '#!/bin/sh\nexit 1\n' > "$TMP/bin/docker"
chmod +x "$TMP/bin/docker"
OUT="$(PATH="$TMP/bin:$PATH" timeout 20 "$CHECK" --image fixture:unavailable 2>&1)"
if printf '%s' "$OUT" | grep -qE "archives|\.deb|\.rpm"; then
  echo "TEST FAIL: image-only mode mentioned dist channels:" >&2
  printf '%s\n' "$OUT" >&2
  exit 1
fi
echo "ok  image-only mode checks no dist channel"

# ── content verification (S2) — fixtures with real manifests ───────────────
# fixture.py builds a complete archive tree; the variants below mutate it.
python3 - "$TMP" <<'PY'
import hashlib, json, os, sys, tarfile, zipfile
tmp = sys.argv[1]

def sha(b): return hashlib.sha256(b).hexdigest()

go_mods = {
    "example.com/plain": ("v1.0.0", b"go plain license\n"),
    "example.com/@odd": ("v2.0.0", b"go odd license\n"),
}
npm_pkgs = {
    "plain-demo": ("1.0.0", b"npm plain license\n"),
    "@example/scoped-demo": ("2.0.0", b"npm scoped license\n"),
}

def build(root):
    os.makedirs(f"{root}/LICENSES/go", exist_ok=True)
    os.makedirs(f"{root}/LICENSES/frontend/texts", exist_ok=True)
    gorows, npmrows = [], []
    for mod, (ver, text) in go_mods.items():
        d = f"{root}/LICENSES/go/{mod}"
        os.makedirs(d, exist_ok=True)
        open(f"{d}/LICENSE", "wb").write(text)
        gorows.append([mod, ver, "LICENSE", "linux/amd64", sha(text)])
    for name, (ver, text) in npm_pkgs.items():
        d = f"{root}/LICENSES/frontend/texts/{name}@{ver}"
        os.makedirs(d, exist_ok=True)
        open(f"{d}/LICENSE", "wb").write(text)
        npmrows.append([name, ver, "LICENSE", sha(text)])
    open(f"{root}/LICENSES/go/manifest.tsv", "w").write(
        "module\tversion\tfile\tprovenance\tsha256\n" +
        "\n".join("\t".join(r) for r in gorows) + "\n")
    open(f"{root}/LICENSES/frontend/manifest.tsv", "w").write(
        "name\tversion\tfile\tsha256\n" +
        "\n".join("\t".join(r) for r in npmrows) + "\n")
    for f in ("LICENSE", "NOTICE", "THIRD-PARTY-NOTICES.md"):
        open(f"{root}/{f}", "w").write(f"{f} content\n")
    open(f"{root}/LICENSES/frontend/npm-licenses.json", "w").write(
        json.dumps([{"name": k, "version": v[0]} for k, v in npm_pkgs.items()]))

complete = f"{tmp}/fixture-complete"
build(complete)
os.makedirs(f"{tmp}/dist-good", exist_ok=True)
with tarfile.open(f"{tmp}/dist-good/crewship_1.0.0_linux_amd64.tar.gz", "w:gz") as t:
    t.add(complete, arcname="")
with zipfile.ZipFile(f"{tmp}/dist-good/crewship-cli_1.0.0_windows_amd64.zip", "w") as z:
    for base, _dirs, files in os.walk(complete):
        for f in files:
            full = os.path.join(base, f)
            z.write(full, os.path.relpath(full, complete))

def variant(name, mutate):
    root = f"{tmp}/fixture-{name}"
    dist = f"{tmp}/dist-{name}"
    os.makedirs(dist, exist_ok=True)
    import shutil
    shutil.copytree(complete, root)
    mutate(root)
    with tarfile.open(f"{dist}/crewship_1.0.0_linux_amd64.tar.gz", "w:gz") as t:
        t.add(root, arcname="")

variant("no-go-text", lambda r: os.remove(
    f"{r}/LICENSES/go/example.com/plain/LICENSE"))
variant("no-scoped-npm-text", lambda r: os.remove(
    f"{r}/LICENSES/frontend/texts/@example/scoped-demo@2.0.0/LICENSE"))
def mutate_content(r):
    p = f"{r}/LICENSES/go/example.com/plain/LICENSE"
    open(p, "wb").write(b"tampered\n")
variant("mutated", mutate_content)
def empty_manifest(r):
    open(f"{r}/LICENSES/frontend/manifest.tsv", "w").write(
        "name\tversion\tfile\tsha256\n")
variant("empty-npm-manifest", empty_manifest)
PY

# Positive: the complete fixture passes the archive channel (deb/rpm absent
# → UNVERIFIED non-strict, which is correct).
OUT="$(timeout 20 "$CHECK" --dist "$TMP/dist-good" 2>&1 || true)"
if ! printf '%s' "$OUT" | grep -q "VERIFIED    archives"; then
  echo "TEST FAIL: complete fixture not verified:" >&2; printf '%s\n' "$OUT" >&2; exit 1
fi
if ! printf '%s' "$OUT" | grep -q "2 go texts + 2 npm texts"; then
  echo "TEST FAIL: manifest counts not reported:" >&2; printf '%s\n' "$OUT" >&2; exit 1
fi
echo "ok  complete archive fixture VERIFIED with manifest counts"

for v in no-go-text no-scoped-npm-text mutated empty-npm-manifest; do
  OUT="$(timeout 20 "$CHECK" --dist "$TMP/dist-$v" 2>&1 || true)"
  rc=0; timeout 20 "$CHECK" --dist "$TMP/dist-$v" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -ne 1 ]; then
    echo "TEST FAIL ($v): expected exit 1, got $rc" >&2; printf '%s\n' "$OUT" >&2; exit 1
  fi
  echo "ok  $v archive fails (exit 1)"
done

# ── truthful VERIFIED (S3) — docker stub: create ok, cp fails ──────────────
STUB="$TMP/bin"; mkdir -p "$STUB"
cat > "$STUB/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  create) echo "stubcontainerid"; exit 0 ;;
  cp) exit 1 ;;
  rm) exit 0 ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$STUB/docker"
OUT="$(PATH="$STUB:$PATH" timeout 20 "$CHECK" --image stub-ref:tag 2>&1 || true)"
if printf '%s' "$OUT" | grep -q "VERIFIED"; then
  echo "TEST FAIL (S3): VERIFIED printed although every cp failed:" >&2
  printf '%s\n' "$OUT" >&2; exit 1
fi
if ! printf '%s' "$OUT" | grep -q "FAIL"; then
  echo "TEST FAIL (S3): no FAIL lines for failing copies:" >&2
  printf '%s\n' "$OUT" >&2; exit 1
fi
echo "ok  S3: failing docker cp yields FAIL without any VERIFIED claim"


# A package must not pass with an empty or absent frontend manifest.
if command -v dpkg-deb >/dev/null 2>&1; then
  python3 - "$TMP" <<'PYTEST'
import csv, pathlib, shutil, sys
root = pathlib.Path(sys.argv[1]); package = root / 'deb-package'
control = package / 'DEBIAN'; control.mkdir(parents=True)
(control / 'control').write_text('Package: fixture\nVersion: 1.0\nArchitecture: all\nMaintainer: Example <test@example.com>\nDescription: License check fixture\n')
doc = package / 'usr/share/doc/crewship'; flat = doc / 'licenses'; flat.mkdir(parents=True)
source = root / 'fixture-complete'
for f in ['LICENSE', 'NOTICE', 'THIRD-PARTY-NOTICES.md']:
    shutil.copyfile(source / f, doc / f)
for ecosystem, manifest in [('go', 'go-manifest.tsv'), ('frontend', 'npm-manifest.tsv')]:
    src = source / 'LICENSES' / ecosystem
    shutil.copyfile(src / 'manifest.tsv', flat / manifest)
    with (src / 'manifest.tsv').open() as f:
        for row in csv.DictReader(f, delimiter='\t'):
            if ecosystem == 'go':
                key = row['module']; text = src / key / row['file']; prefix = 'go_'
            else:
                key = row['name'] + '@' + row['version']; text = src / 'texts' / key / row['file']; prefix = 'npm_'
            shutil.copyfile(text, flat / (prefix + key.replace('/', '_').replace('@', '_') + '.' + row['file']))
PYTEST
  mkdir -p "$TMP/deb-dist"
  dpkg-deb --build "$TMP/deb-package" "$TMP/deb-dist/fixture.deb" >/dev/null
  expect 0 "complete deb with absolute dist path" -- "$CHECK" --dist "$TMP/deb-dist"
  printf 'name\tversion\tfile\tsha256\n' > "$TMP/deb-package/usr/share/doc/crewship/licenses/npm-manifest.tsv"
  dpkg-deb --build "$TMP/deb-package" "$TMP/deb-dist/fixture.deb" >/dev/null
  expect 1 "empty npm manifest in deb fails" -- "$CHECK" --dist "$TMP/deb-dist"
  rm "$TMP/deb-package/usr/share/doc/crewship/licenses/npm-manifest.tsv"
  dpkg-deb --build "$TMP/deb-package" "$TMP/deb-dist/fixture.deb" >/dev/null
  expect 1 "missing npm manifest in deb fails" -- "$CHECK" --dist "$TMP/deb-dist"
fi

# Valid dependency manifests cannot hide a missing project NOTICE in an image.
cat > "$STUB/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  create) echo fixture-container ;;
  cp)
    case "$2" in
      */licenses/go) cp -r "$FIXTURE/LICENSES/go" "$3" ;;
      */licenses/frontend) cp -r "$FIXTURE/LICENSES/frontend" "$3" ;;
      */LICENSE) cp "$FIXTURE/LICENSE" "$3" ;;
      */NOTICE) exit 1 ;;
      *) exit 1 ;;
    esac ;;
  rm) exit 0 ;;
esac
EOF
expect 1 "image missing project NOTICE fails despite valid dependencies" -- \
  env FIXTURE="$TMP/fixture-complete" PATH="$STUB:$PATH" "$CHECK" --strict --image fixture

echo "check-release-artifacts-test: all regressions pass"
