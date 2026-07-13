#!/usr/bin/env bash
set -euo pipefail

# Crewship git pre-commit hook installer (CRE-122)
# Auto-called by dev.sh start. Safe to re-run (idempotent).
#
# The installed hook runs gitleaks + golangci-lint on changed files before commit.
# If either tool is not installed locally, the hook warns but does NOT block —
# CI is the enforcement backstop.

HOOK=".git/hooks/pre-commit"
MARKER="# crewship-pre-commit-v2"

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
# crewship-pre-commit-v2
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
  # web/embed.go's `//go:embed all:out` needs a target dir to typecheck. A
  # fresh `git worktree add` has no web/out, which fails typecheck repo-wide
  # and blocks ANY commit. Mirror CI's setup-go-embed stub. (#1004)
  if [ ! -f web/out/index.html ]; then
    mkdir -p web/out
    echo '<!doctype html>' > web/out/index.html
  fi

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

  if ! golangci-lint run --new-from-rev="$BASE_REF" "${PKGS[@]}"; then
    echo ""
    echo "✗ golangci-lint found issues on new code — commit blocked"
    echo "  Fix the issues or add //nolint:<rule> with justification"
    exit 1
  fi
elif [ -n "$STAGED_GO" ]; then
  echo "⚠ golangci-lint not installed — skipping lint"
  echo "  Install with: brew install golangci-lint"
fi
EOF

chmod +x "$HOOK"
echo "✓ Crewship pre-commit hook installed at $HOOK"
