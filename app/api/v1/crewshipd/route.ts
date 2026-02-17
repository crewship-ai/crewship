import { NextRequest, NextResponse } from "next/server"
import { healthCheck } from "@/lib/crewshipd-client"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")
  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

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
