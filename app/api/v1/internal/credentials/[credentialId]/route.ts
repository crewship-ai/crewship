import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireInternal } from "@/lib/internal-auth"
import { encrypt } from "@/lib/encryption"
import { z } from "zod"

const updateStatusSchema = z.object({
  status: z.enum(["ACTIVE", "EXPIRED", "RATE_LIMITED", "REVOKED", "ERROR"]),
  last_error: z.string().nullable().optional(),
  access_token: z.string().optional(),
  refresh_token: z.string().nullable().optional(),
  token_expires_at: z.string().datetime().nullable().optional(),
})

/**
 * Internal API for crewshipd to update credential status/tokens.
 * Used by credential health monitor after token refresh or error detection.
 */
export async function PATCH(
  req: NextRequest,
  { params }: { params: Promise<{ credentialId: string }> }
) {
  const forbidden = requireInternal(req)
  if (forbidden) return forbidden

  const { credentialId } = await params

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }

  const parsed = updateStatusSchema.safeParse(body)
  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const data: Record<string, unknown> = {
    status: parsed.data.status,
    last_checked_at: new Date(),
  }

  if (parsed.data.last_error !== undefined) {
    data.last_error = parsed.data.last_error
  }

  if (parsed.data.access_token) {
    data.encrypted_value = encrypt(parsed.data.access_token)
  }

  if (parsed.data.refresh_token !== undefined) {
    data.encrypted_refresh_token = parsed.data.refresh_token
      ? encrypt(parsed.data.refresh_token)
      : null
  }

  if (parsed.data.token_expires_at !== undefined) {
    data.token_expires_at = parsed.data.token_expires_at
      ? new Date(parsed.data.token_expires_at)
      : null
  }

  const updated = await prisma.credential.update({
    where: { id: credentialId },
    data,
    select: { id: true, status: true, last_checked_at: true },
  })

  return NextResponse.json(updated)
}
