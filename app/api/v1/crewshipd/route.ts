import { NextRequest, NextResponse } from "next/server"
import { healthCheck } from "@/lib/crewshipd-client"
import { requireAuth, isAuthError } from "@/lib/api-auth"

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")
  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  try {
    const res = await healthCheck()
    if (res.ok) {
      return NextResponse.json(res.data)
    }
    return NextResponse.json({ status: "unreachable" }, { status: 503 })
  } catch {
    return NextResponse.json({ status: "unreachable" }, { status: 503 })
  }
}
