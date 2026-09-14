/** Static disclosure, never an assertion that agent/tool effects are bounded. */
export function routineEffects(definition: Record<string, unknown> | null | undefined) {
  const agents = new Set<string>(),
    hosts = new Set<string>(),
    credentials = new Set<string>()
  let indirect = false,
    http = false
  const strings = (value: unknown, target: Set<string>) => {
    if (Array.isArray(value))
      for (const v of value) if (typeof v === "string") target.add(v)
  }
  strings(definition?.egress_targets, hosts)
  if (Array.isArray(definition?.credentials_required))
    for (const c of definition.credentials_required) {
      if (c && typeof c === "object" && typeof c.type === "string")
        credentials.add(c.type)
    }
  function walk(value: unknown) {
    if (Array.isArray(value)) {
      value.forEach(walk)
      return
    }
    if (!value || typeof value !== "object") return
    const row = value as Record<string, unknown>
    if (row.type === "agent_run")
      agents.add(
        typeof row.agent_slug === "string" ? row.agent_slug : "Agent selected at runtime",
      )
    if (
      ["call", "call_pipeline", "code", "script", "notify", "query", "crewship"].includes(
        String(row.type),
      )
    )
      indirect = true
    if (row.type === "http") {
      http = true
      const request = row.http as Record<string, unknown> | undefined
      if (typeof request?.url === "string") {
        try {
          hosts.add(new URL(request.url).host)
        } catch {
          hosts.add("Host resolved at runtime")
        }
      }
      const ref = request?.credential_ref as Record<string, unknown> | undefined
      if (typeof ref?.type === "string") credentials.add(ref.type)
    }
    Object.values(row).forEach(walk)
  }
  walk(definition?.steps)
  return {
    agents: [...agents],
    hosts: [...hosts],
    credentials: [...credentials],
    indirect,
    http,
  }
}
