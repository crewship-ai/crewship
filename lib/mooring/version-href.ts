/** One version of a document in Artifacts (?version= pins it, artifacts-route). */
export function agentVersionHref(historyHref: string, version: string): string {
  return `${historyHref}${historyHref.includes("?") ? "&" : "?"}${new URLSearchParams({ version })}`
}
