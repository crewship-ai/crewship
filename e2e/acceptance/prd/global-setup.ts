// Logs in every configured role once, records the deployed build and the run
// context in <outputDir>/run.json. Fails when the owner account is missing:
// every scenario needs it, and a silently skipped suite would read as green.
import * as fs from "node:fs"
import { readAppBuild } from "./app-sha"
import { apiAs, loginStorageState } from "./auth"
import { outputPaths, PRD_ROLES, readPrdEnv, storageStatePath } from "./env"

export interface RunInfo {
  origin: string
  mode: "quick" | "full"
  appSha: string
  appVersion: string | null
  appDirty: boolean | null
  appShaError: string | null
  roles: string[]
  startedAt: string
}

export default async function globalSetup() {
  const env = readPrdEnv()
  const owner = env.accounts.owner
  if (!owner) {
    throw new Error(
      env.mode === "quick"
        ? "quick mode needs PRD_OWNER_EMAIL/PRD_OWNER_PASSWORD (or E2E_EMAIL/E2E_PASSWORD)"
        : "full mode needs PRD_OWNER_EMAIL and PRD_OWNER_PASSWORD",
    )
  }
  const paths = outputPaths(env.outputDir)
  fs.mkdirSync(env.outputDir, { recursive: true })

  const roles: string[] = []
  let build = null
  for (const role of PRD_ROLES) {
    const account = env.accounts[role]
    if (!account) continue
    const state = await loginStorageState(env.origin, account)
    const file = storageStatePath(env, role)
    fs.writeFileSync(file, JSON.stringify(state), { mode: 0o600 })
    fs.chmodSync(file, 0o600)
    roles.push(role)
    if (role === "owner") {
      const api = await apiAs(env.origin, state)
      try {
        build = await readAppBuild(api)
      } finally {
        await api.dispose()
      }
    }
  }

  const info: RunInfo = {
    origin: env.origin,
    mode: env.mode,
    appSha: build?.sha ?? "unknown",
    appVersion: build?.version ?? null,
    appDirty: build?.dirty ?? null,
    appShaError: build?.error ?? null,
    roles,
    startedAt: new Date().toISOString(),
  }
  fs.writeFileSync(paths.run, JSON.stringify(info, null, 2) + "\n")
}
