/** Recovery belongs to the verified browser user, never a subsequent login. */
export function purgeOtherE2Recovery(userId: string | null) {
  if (typeof sessionStorage === "undefined") return
  try {
    for (const key of Object.keys(sessionStorage)) {
      const prefix = ["chat-proposal:", "chat-decision:"].find(value => key.startsWith(value))
      if (!prefix) continue
      let owner: unknown
      try { owner = JSON.parse(key.slice(prefix.length))[1] } catch { /* Invalid recovery is discarded. */ }
      if (!userId || owner !== userId) sessionStorage.removeItem(key)
    }
  } catch { /* Unavailable browser storage must not block signing out. */ }
}

export function purgeE2SourceRecovery(workspaceId: string, userId: string, sourceId: string) {
  if (typeof sessionStorage === "undefined") return
  try {
    for (const key of Object.keys(sessionStorage)) {
      const prefix = ["chat-proposal:", "chat-decision:"].find(value => key.startsWith(value))
      if (!prefix) continue
      try {
        const values = JSON.parse(key.slice(prefix.length))
        const [workspace, user] = values
        const source = values[prefix === "chat-decision:" ? 4 : 2]
        if (workspace === workspaceId && user === userId && source === sourceId) sessionStorage.removeItem(key)
      } catch { /* Unknown keys are purged at the next definitive auth check. */ }
    }
  } catch { /* Best effort when browser storage is unavailable. */ }
}
