/** Experimental Mooring SDK · versions. Every save is an immutable server
 * version. Restore never rewinds history: it creates a NEW version (base + 1)
 * whose content is the target's. The server fences restore by lease generation
 * and base version exactly like a save; this module validates what it returns.
 *
 * Native Word/Sheet history is not reimplemented here: `nativeCraftVersions`
 * adapts the host's admitted client, whose replies are already validated by
 * `lib/mooring/native-versions` (the one parser for that wire contract). */
import type { ArtifactProjectSnapshot } from "../../../hooks/use-artifact-project"
import type { NativeDocumentClient } from "../native-document-client"
import type { NativeRestoreRequest, NativeVersionAuthor } from "../native-versions"
import { validNativeVersion } from "../native-draft"

export type { NativeVersion, NativeVersionAuthor, NativeVersionList, NativeRestoreReceipt } from "../native-versions"

export interface DocumentVersion {
  version: string
  createdAt?: string
  message?: string
  author?: NativeVersionAuthor
  restoredFrom?: string
  current: boolean
}
export interface RestoreReceipt { version: string; restoredFrom: string }
export type VersionsFailure = "invalid_versions" | "versions_unavailable" | "unknown_version" | "already_current" | "restore_unconfirmed"
export class VersionsError extends Error {
  constructor(readonly reason: VersionsFailure) { super(reason) }
}
/** An authorized transport supplied by the host. `list` may return any order;
 * `truncated` says the server returned only the newest part of the history.
 * `restore` returns the server's receipt for the new version. */
export interface VersionsTransport {
  list(signal?: AbortSignal): Promise<{ head: string; versions: Omit<DocumentVersion, "current">[]; truncated?: boolean }>
  /** `generation` is the editor's lease generation, for transports that fence
   * restore by lease (Craft); artifact projects fence by base version only. */
  restore(request: { target: string; base: string; generation?: string }, signal?: AbortSignal): Promise<{ version: string }>
}

const version = (v: unknown): v is string => validNativeVersion(v) && v !== "0"

export function createVersionHistory(transport: VersionsTransport) {
  let known: DocumentVersion[] = [], truncated = false
  return {
    get known() { return known },
    /** The last listing held only the newest part of the history. */
    get truncated() { return truncated },
    /** Newest first. Exactly the server head is `current`. */
    async list(signal?: AbortSignal): Promise<DocumentVersion[]> {
      const raw = await transport.list(signal)
      if (!raw || !version(raw.head) || !Array.isArray(raw.versions) || raw.versions.some(v => !v || !version(v.version))) throw new VersionsError("invalid_versions")
      const seen = new Set<string>()
      const rows = raw.versions.filter(v => !seen.has(v.version) && seen.add(v.version))
      if (!seen.has(raw.head) || rows.some(v => BigInt(v.version) > BigInt(raw.head))) throw new VersionsError("invalid_versions")
      known = rows.map(v => ({ ...v, current: v.version === raw.head })).sort((a, b) => BigInt(b.version) > BigInt(a.version) ? 1 : -1)
      truncated = raw.truncated === true
      return known
    },
    /** Restore `target` on top of `held.base`, the version the editor holds. The
     * receipt must name exactly base + 1; anything else is unconfirmed. */
    async restore(target: string, held: { base: string; generation?: string }, signal?: AbortSignal): Promise<RestoreReceipt> {
      const { base, generation } = held
      if (!version(target) || !version(base) || BigInt(target) > BigInt(base) || generation !== undefined && !version(generation)) throw new VersionsError("unknown_version")
      if (target === base) throw new VersionsError("already_current")
      const receipt = await transport.restore({ target, base, ...(generation ? { generation } : {}) }, signal)
      if (!receipt || !version(receipt.version) || BigInt(receipt.version) !== BigInt(base) + 1n) throw new VersionsError("restore_unconfirmed")
      return { version: receipt.version, restoredFrom: target }
    },
  }
}

/** Adapter over the host's admitted native Craft client (`GET …/craft/document/versions`,
 * `POST …/craft/document/restore`). `binding` is the admission's own scope; the
 * client refuses any other. A listing while the history flag is off is
 * `versions_unavailable`. Restore needs the lease generation. */
export function nativeCraftVersions(client: Pick<NativeDocumentClient, "versions" | "restore">, binding: Omit<NativeRestoreRequest, "generation" | "baseVersion" | "targetVersion">): VersionsTransport {
  const signalOf = (signal?: AbortSignal) => signal ?? new AbortController().signal
  return {
    async list(signal) {
      const list = await client.versions(signalOf(signal))
      if (!list) throw new VersionsError("versions_unavailable")
      return {
        head: list.currentVersion,
        truncated: list.truncated,
        versions: list.versions.map(v => ({ version: v.version, createdAt: v.createdAt, author: v.author, ...(v.restoredFrom ? { restoredFrom: v.restoredFrom } : {}) })),
      }
    },
    async restore({ target, base, generation }, signal) {
      if (!generation) throw new VersionsError("unknown_version")
      const receipt = await client.restore({ ...binding, generation, baseVersion: base, targetVersion: target }, signalOf(signal))
      return { version: receipt.version }
    },
  }
}

/** Adapter over the artifact project path (`useArtifactProject`): its query
 * data and its `restore` mutation already carry the host's authentication. */
export function artifactProjectVersions(source: {
  read(signal?: AbortSignal): Promise<Pick<ArtifactProjectSnapshot, "latest_version" | "versions">>
  restore(body: { version: number; base_version: number }): Promise<unknown>
}): VersionsTransport {
  return {
    async list(signal) {
      const snapshot = await source.read(signal)
      if (!Number.isSafeInteger(snapshot.latest_version) || snapshot.versions.some(v => !Number.isSafeInteger(v.version))) throw new VersionsError("invalid_versions")
      return { head: String(snapshot.latest_version), versions: snapshot.versions.map(v => ({ version: String(v.version), ...(v.created_at ? { createdAt: v.created_at } : {}), ...(v.message ? { message: v.message } : {}) })) }
    },
    async restore({ target, base }) {
      const targetVersion = Number(target), baseVersion = Number(base)
      if (!version(target) || !version(base) || !Number.isSafeInteger(targetVersion) || !Number.isSafeInteger(baseVersion) || baseVersion >= Number.MAX_SAFE_INTEGER) throw new VersionsError("unknown_version")
      const receipt = await source.restore({ version: targetVersion, base_version: baseVersion }) as { version?: unknown } | null
      return { version: typeof receipt?.version === "number" && Number.isSafeInteger(receipt.version) ? String(receipt.version) : "" }
    },
  }
}
