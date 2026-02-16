import { NextRequest, NextResponse } from "next/server"

/**
 * Validate that a request comes from crewshipd (internal IPC).
 * In production, this should use a shared secret; for MVP we check the header.
 */
export function requireInternal(req: NextRequest): NextResponse | null {
  const token = req.headers.get("x-internal-token")
  if (token !== "crewshipd") {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }
  return null
}
