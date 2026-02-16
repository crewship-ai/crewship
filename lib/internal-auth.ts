import { NextRequest, NextResponse } from "next/server"

function getInternalToken(): string {
  return process.env.CREWSHIP_INTERNAL_TOKEN || "crewshipd"
}

/**
 * Validate that a request comes from crewshipd (internal IPC).
 * Checks X-Internal-Token header against CREWSHIP_INTERNAL_TOKEN env var.
 */
export function requireInternal(req: NextRequest): NextResponse | null {
  const token = req.headers.get("x-internal-token")
  if (!token || token !== getInternalToken()) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }
  return null
}
