import { existsSync, readFileSync, realpathSync } from "node:fs"
import { homedir } from "node:os"
import { join, sep } from "node:path"

/** Returns why this run must not write into DATA_DIR, or "" when it is a verified throwaway instance. */
export function throwawayRefusal(env: Record<string, string | undefined>, baseUrl: string, sharedDataDirs: string[]): string {
  const manifestPath = env.CREWSHIP_THROWAWAY_MANIFEST ?? ""
  const dataDir = env.E2E_CREWSHIP_DATA_DIR ?? ""
  if (!manifestPath) return "needs scripts/throwaway-server.sh run (CREWSHIP_THROWAWAY_MANIFEST is not set)"
  if (!dataDir) return "E2E_CREWSHIP_DATA_DIR is not set"
  let manifest: Record<string, string>
  try {
    if (!existsSync(manifestPath)) return "the throwaway manifest does not exist"
    manifest = JSON.parse(readFileSync(manifestPath, "utf8"))
  } catch { return "the throwaway manifest is unreadable" }
  const root = realpathOrEmpty(env.CREWSHIP_THROWAWAY_STATE || join(homedir(), ".local", "state", "crewship-throwaway"))
  const manifestReal = realpathOrEmpty(manifestPath)
  const dataReal = realpathOrEmpty(dataDir)
  if (!root || !manifestReal || !dataReal) return "manifest or data dir path cannot be resolved"
  const name = manifest.name ?? ""
  if (!/^[a-z0-9][a-z0-9-]{0,30}$/.test(name)) return "manifest has no valid throwaway name"
  if (manifestReal !== join(root, name, "manifest.json")) return "manifest is not under the throwaway state root"
  if (dataReal !== join(root, name, "data")) return "E2E_CREWSHIP_DATA_DIR is not this throwaway instance's data dir"
  if (realpathOrEmpty(manifest.data_dir ?? "") !== dataReal) return "E2E_CREWSHIP_DATA_DIR does not equal the manifest's data_dir"
  const shared = [`${sep}mnt${sep}dev-workspace${sep}envs`, ...sharedDataDirs.map((d) => realpathOrEmpty(d)).filter(Boolean)]
  if (shared.some((d) => dataReal === d || dataReal.startsWith(d + sep))) return "data dir is a shared environment path"
  if (manifest.state !== "running") return `throwaway instance state is "${manifest.state}", not running`
  const port = Number(manifest.port)
  if (!Number.isInteger(port) || port < 8110 || port > 8199) return "manifest port is outside the throwaway range"
  if ((env.CREWSHIP_SERVER ?? "") !== `http://127.0.0.1:${port}`) return "CREWSHIP_SERVER is not the manifest's server"
  let url: URL
  try { url = new URL(baseUrl) } catch { return "base URL is not a URL" }
  if (!["127.0.0.1", "localhost"].includes(url.hostname) || url.protocol !== "http:" || Number(url.port) !== port) return "base URL is not the manifest's server (host and port must match)"
  return ""
}

function realpathOrEmpty(p: string): string {
  try { return p ? realpathSync(p) : "" } catch { return "" }
}
