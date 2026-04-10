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
