#!/usr/bin/env bash
set -euo pipefail

# Crewship git pre-commit hook installer (CRE-122)
# Auto-called by dev.sh start. Safe to re-run (idempotent).
#
# The installed hook runs gitleaks + golangci-lint on changed files before commit.
# If either tool is not installed locally, the hook warns but does NOT block —
# CI is the enforcement backstop.

HOOK=".git/hooks/pre-commit"
MARKER="# crewship-pre-commit-v1"

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
# crewship-pre-commit-v1
set -euo pipefail

# Sentinel: reject leaked git merge-conflict markers in staged files.
# Background (2026-05-21 incident): a literal `<<<<<<<` / `=======` /
# `>>>>>>>` block landed inside a Go raw-string SQL query and shipped
# to main — Go raw strings don't parse their content so go vet /
# golangci-lint / gosec all stayed green, but SQLite exploded at
# runtime. Catching at commit time keeps the local loop tight; CI
# has a mirror sentinel (.github/workflows/ci.yml).
STAGED=$(git diff --cached --name-only --diff-filter=ACMR | \
  grep -E '\.(go|ts|tsx|mdx|md|yaml|yml|sql|json|py|sh)$|^Dockerfile' || true)
if [ -n "$STAGED" ]; then
  if echo "$STAGED" | xargs -r grep -lE '^(<<<<<<< |=======$|>>>>>>> )' 2>/dev/null; then
    echo ""
    echo "✗ Staged files contain unresolved git merge-conflict markers — commit blocked"
    echo "  Resolve the conflict, re-stage, and retry."
    exit 1
  fi
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

# Lint changes since main (fast, not full project)
if command -v golangci-lint >/dev/null 2>&1; then
  # Determine merge-base with main/master
  BASE_REF="origin/main"
  if ! git rev-parse --verify "$BASE_REF" >/dev/null 2>&1; then
    BASE_REF="origin/master"
    if ! git rev-parse --verify "$BASE_REF" >/dev/null 2>&1; then
      BASE_REF="HEAD~1"
    fi
  fi
  if ! golangci-lint run --new-from-rev="$BASE_REF" ./...; then
    echo ""
    echo "✗ golangci-lint found issues on new code — commit blocked"
    echo "  Fix the issues or add //nolint:<rule> with justification"
    exit 1
  fi
else
  echo "⚠ golangci-lint not installed — skipping lint"
  echo "  Install with: brew install golangci-lint"
fi
EOF

chmod +x "$HOOK"
echo "✓ Crewship pre-commit hook installed at $HOOK"
