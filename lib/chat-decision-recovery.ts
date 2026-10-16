/** Remove protected reply bodies only after definitive scope loss or a complete
 * authorized replacement snapshot. Network uncertainty never calls this. */
export function purgeChatDecisionRecovery(workspace: string, viewer: string, chat: string, retainedSources?: ReadonlySet<string>) {
  try {
    const remove: string[] = []
    for (let index = 0; index < sessionStorage.length; index++) {
      const key = sessionStorage.key(index)
      if (!key?.startsWith("chat-decision:")) continue
      let scope: unknown
      try { scope = JSON.parse(key.slice("chat-decision:".length)) } catch { continue }
      if (!Array.isArray(scope) || scope[0] !== workspace || scope[1] !== viewer || scope[2] !== chat) continue
      if (!retainedSources?.has(JSON.stringify([scope[3], scope[4]]))) remove.push(key)
    }
    for (const key of remove) sessionStorage.removeItem(key)
  } catch { /* Access denial still evicts all visible cards even when storage is unavailable. */ }
}
