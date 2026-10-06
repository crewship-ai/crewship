// Test-only helper: the restricted allowlist and the full route table, read from
// the Go source, so frontend tests judge a request the way the server does.
import { readFileSync, readdirSync, statSync } from "node:fs"
import path from "node:path"

import { isRestrictedAllowedRequest } from "@/lib/restricted-endpoints"

const ROOT = path.resolve(__dirname, "../..")
const PATTERN = /"((?:GET|POST|PUT|PATCH|DELETE|HEAD) \/api\/[^"\s]+)"/g

function goFiles(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const full = path.join(dir, name)
    if (statSync(full).isDirectory()) out.push(...goFiles(full))
    else if (name.endsWith(".go") && !name.endsWith("_test.go")) out.push(full)
  }
  return out
}

function patternsIn(source: string): string[] {
  return [...source.matchAll(PATTERN)].map((m) => m[1])
}

/** Every method+path pattern literal in `internal/api/restricted_access.go`. */
export function goRestrictedAllowlist(): string[] {
  const source = readFileSync(path.join(ROOT, "internal/api/restricted_access.go"), "utf8")
  return [...new Set(patternsIn(source))].sort()
}

let registered: string[] | null = null

/** Every route pattern literal registered anywhere under `internal/api`. */
export function goRegisteredRoutes(): string[] {
  if (!registered) {
    const all = goFiles(path.join(ROOT, "internal/api")).flatMap((file) => patternsIn(readFileSync(file, "utf8")))
    registered = [...new Set(all)]
  }
  return registered
}

/** Would the server let a restricted session through with this request? */
export function serverAllowsRestricted(method: string | undefined, url: string): boolean {
  return isRestrictedAllowedRequest(method, url, goRegisteredRoutes())
}
