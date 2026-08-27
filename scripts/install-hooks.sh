#!/usr/bin/env bash
set -euo pipefail

# Crewship git pre-commit hook installer (CRE-122)
# Auto-called by dev.sh start. Safe to re-run (idempotent).
#
# The installed hook runs gitleaks + golangci-lint on changed files before commit.
# If either tool is not installed locally, the hook warns but does NOT block —
# CI is the enforcement backstop.

HOOK=".git/hooks/pre-commit"
# Bump this whenever the hook body below changes: the installer is idempotent
# on this marker, so a body change without a bump reaches nobody who already
# has the hook installed.
MARKER="# crewship-pre-commit-v3"

if [ ! -d .git ]; then
  echo "install-hooks.sh: not a git repo, skipping"
  exit 0
fi

# Idempotent: skip if already installed with matching version marker
if [ -f "$HOOK" ] && grep -q "$MARKER" "$HOOK"; then
  exit 0
fi

cat > "$HOOK" <<'EOF'
#!/usr/bin/env bash
# crewship-pre-commit-v3
set -euo pipefail

# Sentinel: reject leaked git merge-conflict markers in staged files.
# Background (2026-05-21 incident): a literal `<<<<<<<` / `=======` /
# `>>>>>>>` block landed inside a Go raw-string SQL query and shipped
# to main — Go raw strings don't parse their content so go vet /
# golangci-lint / gosec all stayed green, but SQLite exploded at
# runtime. Catching at commit time keeps the local loop tight; CI
# has a mirror sentinel (.github/workflows/ci.yml).
# NUL-delimited paths so filenames with spaces / newlines don't break
# the pipeline, and `git show :path` reads the STAGED blob content
# rather than the working-tree file. Without this, an operator who
# resolved a conflict in their editor but left the marker text in
# the working tree (and only staged the clean version) would still
# trip the working-tree grep — false positive. Conversely, a marker
# in the staged blob but not in the working tree would slip past —
# false negative. CodeRabbit round-6 catch.
STAGED_HIT=0
while IFS= read -r -d '' path; do
  case "$path" in
    *.go|*.ts|*.tsx|*.mdx|*.md|*.yaml|*.yml|*.sql|*.json|*.py|*.sh|Dockerfile|Dockerfile.*) ;;
    *) continue ;;
  esac
  if git show ":$path" 2>/dev/null | grep -qE '^(<<<<<<< |=======$|>>>>>>> )'; then
    if [ "$STAGED_HIT" -eq 0 ]; then
      echo ""
      echo "✗ Staged files contain unresolved git merge-conflict markers — commit blocked"
      STAGED_HIT=1
    fi
    echo "  • $path"
  fi
done < <(git diff --cached --name-only --diff-filter=ACMR -z)
if [ "$STAGED_HIT" -ne 0 ]; then
  echo "  Resolve the conflict, re-stage, and retry."
  exit 1
fi

# Secret scan on staged changes
if command -v gitleaks >/dev/null 2>&1; then
  gitleaks protect --staged --no-banner --redact || {
    echo ""
    echo "✗ gitleaks found secrets in staged changes — commit blocked"
    echo "  Rotate the credential and retry, or add to .gitleaksignore if false positive"
    exit 1
  }
else
  echo "⚠ gitleaks not installed — skipping secret scan"
  echo "  Install with: brew install gitleaks"
fi

# Lint changes since main (fast, not full project).
#
# Only run the Go gate when Go files are actually staged. golangci-lint
# typechecks the whole package set it's given, and --new-from-rev only scopes
# the *reported* issues, not the compile — so a shell/docs-only commit used to
# be blocked by another session's in-flight Go work elsewhere in the tree
# (e.g. `undefined: neutralizeControl`). No staged .go → nothing to gate. (#1004)
STAGED_GO="$(git diff --cached --name-only --diff-filter=ACMR -z | tr '\0' '\n' | grep -E '\.go$' || true)"
if [ -n "$STAGED_GO" ] && command -v golangci-lint >/dev/null 2>&1; then
  # web/embed.go's `//go:embed all:out` needs a target dir to typecheck, and
  # a fresh `git worktree add` used to have none — which failed typecheck
  # repo-wide and blocked ANY commit (#1004). This hook used to hand-roll a
  # web/out/index.html stub for that; web/out/.placeholder.html is tracked
  # in git now (#1567), so the checkout satisfies the embed on its own.

  # Determine merge-base with main/master
  BASE_REF="origin/main"
  if ! git rev-parse --verify "$BASE_REF" >/dev/null 2>&1; then
    BASE_REF="origin/master"
    if ! git rev-parse --verify "$BASE_REF" >/dev/null 2>&1; then
      BASE_REF="HEAD~1"
    fi
  fi

  # Scope the run to the packages of staged Go files so another session's
  # uncommitted work in an unrelated package can't block this commit. Fall
  # back to ./... only if no package dir resolves.
  declare -a PKGS=()
  while IFS= read -r d; do
    [ -n "$d" ] && PKGS+=("./$d")
  done < <(printf '%s\n' "$STAGED_GO" | xargs -r -n1 dirname | sort -u)
  [ "${#PKGS[@]}" -eq 0 ] && PKGS=("./...")

  # Capture rather than stream, so the two very different reasons this can
  # fail get two different messages. A golangci-lint built with an older Go
  # than the repo targets refuses to load its config at all — and reporting
  # that as "issues on new code" sends the reader looking for a bug in their
  # own diff that does not exist. The Go 1.27 bump made this the *expected*
  # state of every machine that had not upgraded the linter yet.
  # `set -e` is on: without the `|| LINT_RC=$?` the failing assignment would
  # abort the hook here and block the commit with no explanation at all.
  LINT_RC=0
  LINT_OUT="$(golangci-lint run --new-from-rev="$BASE_REF" "${PKGS[@]}" 2>&1)" || LINT_RC=$?
  if [ "$LINT_RC" -ne 0 ]; then
    printf '%s\n' "$LINT_OUT"
    if printf '%s' "$LINT_OUT" | grep -q 'lower than the targeted Go version'; then
      # pipefail is on, so a grep that matches nothing would fail the whole
      # substitution and abort before the message it exists to print.
      GOLANGCI_PINNED="$(grep -m1 -oE 'GOLANGCI_VERSION: v[0-9.]+' .github/workflows/ci.yml 2>/dev/null | cut -d' ' -f2 || true)"
      echo ""
      echo "✗ golangci-lint is older than this repo's Go target — commit blocked"
      echo "  This is a tooling mismatch, NOT a problem with your code."
      echo "  Build one with the repo's own toolchain (CI pins ${GOLANGCI_PINNED:-see .github/workflows/ci.yml}):"
      echo "    go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_PINNED:-<GOLANGCI_VERSION>}"
      echo "  A Homebrew build is generally too old for this — it ships built"
      echo "  against whatever Go the formula was bottled with."
      exit 1
    fi
    echo ""
    echo "✗ golangci-lint found issues on new code — commit blocked"
    echo "  Fix the issues or add //nolint:<rule> with justification"
    exit 1
  fi
elif [ -n "$STAGED_GO" ]; then
  echo "⚠ golangci-lint not installed — skipping lint"
  echo "  Install the version CI pins as GOLANGCI_VERSION in"
  echo "  .github/workflows/ci.yml, built with this repo's toolchain:"
  echo "    go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@<GOLANGCI_VERSION>"
fi
EOF

chmod +x "$HOOK"
echo "✓ Crewship pre-commit hook installed at $HOOK"
