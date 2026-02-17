import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"
import http from "node:http"

const CREWSHIPD_URL = process.env.CREWSHIPD_URL ?? "unix:///tmp/crewship.sock"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> },
) {
  const { agentId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")
  const filePath = req.nextUrl.searchParams.get("path")

  if (!filePath) {
    return NextResponse.json({ error: "path parameter required" }, { status: 400 })
  }

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true, slug: true, crew_id: true },
  })

  if (!agent || !agent.crew_id) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  const agentFilePath = `${agent.slug}/${filePath}`
  const ipcPath = `/crews/${encodeURIComponent(agent.crew_id)}/files/download?path=${encodeURIComponent(agentFilePath)}`

  try {
    const buffer = await ipcDownload(ipcPath)
    const filename = filePath.split("/").pop() ?? "download"
    return new NextResponse(new Uint8Array(buffer), {
      headers: {
        "Content-Type": "application/octet-stream",
        "Content-Disposition": `attachment; filename="${filename}"`,
        "Content-Length": String(buffer.length),
      },
    })
  } catch {
    return NextResponse.json({ error: "File not found" }, { status: 404 })
  }
}

function ipcDownload(path: string): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const isUnix = CREWSHIPD_URL.startsWith("unix://")
    const socketPath = isUnix ? CREWSHIPD_URL.replace("unix://", "") : undefined

    const options: http.RequestOptions = {
      method: "GET",
      path,
      timeout: 30000,
      headers: {
        Authorization: `Bearer ${process.env.CREWSHIP_INTERNAL_TOKEN ?? ""}`,
      },
      ...(socketPath ? { socketPath } : { hostname: "localhost", port: 8080 }),
    }

    const req = http.request(options, (res) => {
      if (res.statusCode !== 200) {
        reject(new Error(`IPC ${res.statusCode}`))
        res.resume()
        return
      }
      const chunks: Buffer[] = []
      res.on("data", (chunk: Buffer) => chunks.push(chunk))
      res.on("end", () => resolve(Buffer.concat(chunks)))
      res.on("error", reject)
    })

    req.on("error", reject)
    req.on("timeout", () => { req.destroy(); reject(new Error("timeout")) })
    req.end()
  })
}
