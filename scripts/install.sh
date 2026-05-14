#!/usr/bin/env bash
#
# Crewship installer
#
#   curl -fsSL https://crewship.ai/install | sh
#
# Detects OS+arch, downloads the matching release archive from GitHub,
# verifies the SHA-256 checksum against the signed checksums.txt, and
# installs the binary into a writable directory on PATH.
#
# When `cosign` is available locally the script ALSO verifies the
# release's keyless signature against the project's expected OIDC
# identity (GitHub Actions workflow). Missing cosign is a soft warning
# rather than a hard failure — keeping the install one-liner workable
# for users who haven't installed sigstore tooling yet.
#
# Environment overrides:
#   CREWSHIP_VERSION   pin a specific tag (default: latest stable)
#   CREWSHIP_INSTALL_DIR  override install directory
#   CREWSHIP_SKIP_VERIFY  set to 1 to skip checksum verification (NOT recommended)

set -euo pipefail

REPO="crewship-ai/crewship"
EXPECTED_OIDC_ISSUER="https://token.actions.githubusercontent.com"
EXPECTED_CERT_IDENTITY_RE="https://github.com/${REPO}/.github/workflows/release.yml@.*"

# ---------- colors ----------
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_BOLD='\033[1m'
  C_DIM='\033[2m'
  C_GREEN='\033[32m'
  C_YELLOW='\033[33m'
  C_RED='\033[31m'
  C_RESET='\033[0m'
else
  C_BOLD='' C_DIM='' C_GREEN='' C_YELLOW='' C_RED='' C_RESET=''
fi

info() { printf '%b\n' "${C_DIM}==>${C_RESET} $*"; }
ok()   { printf '%b\n' "${C_GREEN}✓${C_RESET} $*"; }
warn() { printf '%b\n' "${C_YELLOW}!${C_RESET} $*" >&2; }
err()  { printf '%b\n' "${C_RED}✗${C_RESET} $*" >&2; exit 1; }

# ---------- platform detection ----------
detect_os() {
  case "$(uname -s)" in
    Linux)   echo linux ;;
    Darwin)  echo darwin ;;
    MINGW*|MSYS*|CYGWIN*)
      err "Windows is not supported by this script. Download the .zip from https://github.com/${REPO}/releases or install via 'scoop install crewship'." ;;
    *) err "unsupported OS: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *) err "unsupported arch: $(uname -m)" ;;
  esac
}

# ---------- prerequisite check ----------
require() {
  command -v "$1" >/dev/null 2>&1 || err "missing required command: $1"
}

require curl
require tar
require uname

# ---------- resolve version ----------
resolve_version() {
  if [ -n "${CREWSHIP_VERSION:-}" ]; then
    echo "$CREWSHIP_VERSION"
    return
  fi
  # GitHub `releases/latest` returns the most recent NON-prerelease, which
  # is exactly what a curl-pipe-install should default to. Beta testers who
  # want a pre-release pin via CREWSHIP_VERSION=v0.1.0-beta.1.
  api_url="https://api.github.com/repos/${REPO}/releases/latest"
  tag=$(curl -fsSL "$api_url" 2>/dev/null \
    | grep -E '"tag_name"' \
    | head -n1 \
    | sed -E 's/.*"tag_name"\s*:\s*"([^"]+)".*/\1/')
  if [ -z "$tag" ]; then
    err "could not resolve latest release. Set CREWSHIP_VERSION=vX.Y.Z to install a specific tag."
  fi
  echo "$tag"
}

# ---------- install directory ----------
# Order of preference:
#   1. $CREWSHIP_INSTALL_DIR (operator override)
#   2. $XDG_BIN_HOME, $HOME/.local/bin if on PATH or creatable (no sudo)
#   3. /usr/local/bin (sudo if not writable)
pick_install_dir() {
  if [ -n "${CREWSHIP_INSTALL_DIR:-}" ]; then
    mkdir -p "$CREWSHIP_INSTALL_DIR"
    echo "$CREWSHIP_INSTALL_DIR"
    return
  fi

  for candidate in "${XDG_BIN_HOME:-}" "$HOME/.local/bin"; do
    [ -z "$candidate" ] && continue
    if mkdir -p "$candidate" 2>/dev/null && [ -w "$candidate" ]; then
      case ":$PATH:" in
        *":$candidate:"*) echo "$candidate"; return ;;
      esac
    fi
  done

  echo /usr/local/bin
}

# ---------- download + verify ----------
fetch_to() {
  url=$1
  dest=$2
  curl -fsSL --retry 3 -o "$dest" "$url" \
    || err "download failed: $url"
}

verify_checksum() {
  archive=$1
  checksums=$2
  # checksums.txt format: "<sha256>  <filename>"
  archive_basename=$(basename "$archive")
  expected=$(grep -E "  ${archive_basename}$" "$checksums" | awk '{print $1}' | head -n1)
  if [ -z "$expected" ]; then
    err "checksum for ${archive_basename} not found in checksums.txt"
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$archive" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$archive" | awk '{print $1}')
  else
    err "no sha256sum or shasum available"
  fi
  if [ "$actual" != "$expected" ]; then
    err "checksum mismatch for ${archive_basename}\n  expected: $expected\n  actual:   $actual"
  fi
  ok "checksum verified"
}

# Cosign keyless verification: confirms the artifact was signed by the
# project's own release workflow running under GitHub Actions. The cert
# identity regex pins the verification to crewship-ai/crewship's
# release.yml — a forked or impersonated workflow won't pass even if
# the attacker controls a GitHub Actions runner.
verify_cosign() {
  archive=$1
  sig=$2
  cert=$3
  if ! command -v cosign >/dev/null 2>&1; then
    warn "cosign not installed — skipping signature verification (install: https://docs.sigstore.dev/cosign/installation/)"
    return
  fi
  cosign verify-blob \
    --certificate-identity-regexp "$EXPECTED_CERT_IDENTITY_RE" \
    --certificate-oidc-issuer "$EXPECTED_OIDC_ISSUER" \
    --signature "$sig" \
    --certificate "$cert" \
    "$archive" >/dev/null 2>&1 \
    || err "cosign signature verification failed for $(basename "$archive")"
  ok "cosign signature verified"
}

# ---------- install ----------
install_binary() {
  src=$1
  dst_dir=$2
  dst="${dst_dir}/crewship"

  if [ -w "$dst_dir" ]; then
    mv "$src" "$dst"
    chmod +x "$dst"
  else
    info "installing to $dst (sudo required)"
    sudo mv "$src" "$dst"
    sudo chmod +x "$dst"
  fi
  ok "installed: $dst"
}

# ---------- post-install hints ----------
check_docker_hint() {
  if command -v docker >/dev/null 2>&1; then
    return
  fi
  if command -v podman >/dev/null 2>&1; then
    return
  fi
  cat >&2 <<EOF

${C_YELLOW}!${C_RESET} No container runtime detected. Crewship runs agents inside containers
  and needs one of:
    - Docker Desktop:  https://docs.docker.com/get-docker/
    - OrbStack (macOS): https://orbstack.dev/
    - Colima (macOS):  brew install colima && colima start
    - Podman:          https://podman.io/docs/installation

EOF
}

# ---------- main ----------
main() {
  printf '%b\n' "${C_BOLD}Crewship installer${C_RESET}"

  OS=$(detect_os)
  ARCH=$(detect_arch)
  TAG=$(resolve_version)
  # Strip leading "v" for the archive name — goreleaser writes
  # crewship_<version>_<os>_<arch>.tar.gz where <version> is e.g. 0.1.0.
  VERSION=${TAG#v}

  info "platform:  ${OS}/${ARCH}"
  info "version:   ${TAG}"

  ARCHIVE="crewship_${VERSION}_${OS}_${ARCH}.tar.gz"
  BASE_URL="https://github.com/${REPO}/releases/download/${TAG}"

  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT

  info "downloading ${ARCHIVE}"
  fetch_to "${BASE_URL}/${ARCHIVE}"           "${TMP}/${ARCHIVE}"
  fetch_to "${BASE_URL}/checksums.txt"        "${TMP}/checksums.txt"

  if [ "${CREWSHIP_SKIP_VERIFY:-0}" = "1" ]; then
    warn "skipping checksum verification (CREWSHIP_SKIP_VERIFY=1)"
  else
    verify_checksum "${TMP}/${ARCHIVE}" "${TMP}/checksums.txt"
    # Cosign artifacts are best-effort: if a release predates the signing
    # workflow they won't exist, in which case we skip silently.
    if curl -fsSL --retry 2 -o "${TMP}/${ARCHIVE}.sig" "${BASE_URL}/${ARCHIVE}.sig" 2>/dev/null \
       && curl -fsSL --retry 2 -o "${TMP}/${ARCHIVE}.pem" "${BASE_URL}/${ARCHIVE}.pem" 2>/dev/null; then
      verify_cosign "${TMP}/${ARCHIVE}" "${TMP}/${ARCHIVE}.sig" "${TMP}/${ARCHIVE}.pem"
    else
      info "no cosign signatures published for this release — skipping"
    fi
  fi

  info "extracting"
  tar -xzf "${TMP}/${ARCHIVE}" -C "${TMP}"

  if [ ! -f "${TMP}/crewship" ]; then
    err "archive did not contain expected 'crewship' binary"
  fi

  DEST=$(pick_install_dir)
  install_binary "${TMP}/crewship" "$DEST"

  # Sanity check — the binary we just installed must run.
  if ! "${DEST}/crewship" version >/dev/null 2>&1; then
    err "installed binary failed to execute. PATH=${PATH}"
  fi

  printf '\n'
  ok "$("${DEST}/crewship" version | head -n1)"
  check_docker_hint

  # PATH hint if we installed somewhere not on PATH (rare given pick_install_dir).
  case ":$PATH:" in
    *":${DEST}:"*) ;;
    *)
      warn "${DEST} is not on your PATH. Add this to your shell rc:"
      printf '  export PATH="%s:$PATH"\n' "$DEST"
      ;;
  esac

  printf '\nNext: %brun%b\n' "${C_BOLD}" "${C_RESET}"
  printf '  crewship start\n\n'
}

main "$@"
