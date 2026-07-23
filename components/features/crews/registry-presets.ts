// Package-registry preset for Restricted-mode crews (#1377).
//
// A Restricted crew can't `npm/pip/cargo/go install` or pull a Docker Hub image
// unless every host those tools dial is on the allowlist. Enumerating them by
// hand is a top Restricted-mode trap, so the create-crew wizard and the crew
// settings tab offer a one-click "allow package registries" button that appends
// this curated set.
//
// MUST stay in sync with internal/sidecar/allowlist.go PackageRegistryDomains —
// that Go list is the source of truth the CLI (`--allow-package-registries`)
// and the sidecar allowlist both use.
export const PACKAGE_REGISTRY_DOMAINS: readonly string[] = [
  // npm
  "registry.npmjs.org",
  // pip (PyPI)
  "pypi.org",
  "files.pythonhosted.org",
  // cargo (crates.io)
  "crates.io",
  "static.crates.io",
  "index.crates.io",
  // Go modules
  "proxy.golang.org",
  "sum.golang.org",
  // apt (Debian + Ubuntu mirrors)
  "deb.debian.org",
  "security.debian.org",
  "archive.ubuntu.com",
  "security.ubuntu.com",
  "ports.ubuntu.com",
  // Docker Hub image pulls
  "registry-1.docker.io",
  "auth.docker.io",
  "index.docker.io",
  "production.cloudflare.docker.com",
]

/**
 * Union `extra` into `base`, preserving base order and appending only entries
 * not already present (case-insensitive, trimmed, lower-cased). Mirrors the
 * CLI's mergeDomains so the one-click preset behaves identically everywhere.
 */
export function mergeDomains(base: string[], extra: readonly string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const d of [...base, ...extra]) {
    const key = d.trim().toLowerCase()
    if (!key || seen.has(key)) continue
    seen.add(key)
    out.push(key)
  }
  return out
}
