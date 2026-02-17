import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import { getDebugInfo, getDebugLogs, getAgentStatus, getAgentLogs } from "@/lib/crewshipd-client"
import type { OrgRole } from "@/lib/generated/prisma/client"

const SENSITIVE_PATTERNS = /token|secret|password|key|authorization|credential|bearer/i

function redactLogEntries(logs: unknown[]): unknown[] {
  return logs.map((entry) => {
    if (typeof entry !== "object" || entry === null) return entry
    const e = entry as Record<string, unknown>
    const redacted: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(e)) {
      if (typeof v === "string" && SENSITIVE_PATTERNS.test(k)) {
        redacted[k] = "[REDACTED]"
      } else {
        redacted[k] = v
      }
    }
    return redacted
  })
}

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> },
) {
  const { agentId } = await params
  if (!z.string().uuid().safeParse(agentId).success) {
    return NextResponse.json({ error: "Invalid agent ID" }, { status: 400 })
  }
  const orgId = req.nextUrl.searchParams.get("org_id")
  if (orgId && !z.string().uuid().safeParse(orgId).success) {
    return NextResponse.json({ error: "Invalid org_id" }, { status: 400 })
  }

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("manage", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, org_id: authResult.orgId, deleted_at: null },
    select: { id: true, name: true, team_id: true, cli_adapter: true, status: true },
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  const debug: Record<string, unknown> = {
    agent: { id: agent.id, name: agent.name, cli_adapter: agent.cli_adapter, db_status: agent.status },
    crewshipd_reachable: false,
  }

  // crewshipd comprehensive info (redact sensitive config fields)
  try {
    const info = await getDebugInfo()
    if (info.ok) {
      const data = info.data as Record<string, unknown>
      if (data.config && typeof data.config === "object") {
        const cfg = data.config as Record<string, unknown>
        const safeConfig: Record<string, unknown> = {}
        const allowedKeys = [
          "runtime_image", "default_memory_mb", "default_cpus", "network",
          "log_path", "storage_base_path",
        ]
        for (const k of allowedKeys) {
          if (k in cfg) safeConfig[k] = cfg[k]
        }
        data.config = safeConfig
      }
      debug.crewshipd = data
      debug.crewshipd_reachable = true
    } else {
      debug.crewshipd = { error: info.error }
    }
  } catch (err) {
    debug.crewshipd = { error: String(err) }
  }

  // Agent runtime status from state
  try {
    const status = await getAgentStatus(agentId)
    debug.runtime = status.ok ? status.data : { status: "unknown" }
  } catch {
    debug.runtime = { status: "unreachable" }
  }

  // crewshipd service logs (filtered to this agent where possible)
  try {
    const svcLogs = await getDebugLogs(200, agentId)
    const rawLogs = svcLogs.ok ? (svcLogs.data.logs ?? []) : []
    debug.service_logs = redactLogEntries(rawLogs as unknown[])
  } catch {
    debug.service_logs = []
  }

  // Agent output logs (JSONL from logcollector)
  if (agent.team_id) {
    try {
      const logs = await getAgentLogs(agentId, agent.team_id, 0, 50)
      const rawLogs = logs.ok ? (logs.data.logs ?? []) : []
      debug.agent_logs = redactLogEntries(rawLogs as unknown[])
    } catch {
      debug.agent_logs = []
    }
  } else {
    debug.agent_logs = []
  }

  return NextResponse.json(debug)
}
