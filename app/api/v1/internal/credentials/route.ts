import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireInternal } from "@/lib/internal-auth"
import { decrypt } from "@/lib/encryption"

/**
 * Internal API for crewshipd to fetch active AI credentials (AI_CLI_TOKEN + API_KEY)
 * with decrypted tokens for the token pool.
 * Auth: X-Internal-Token header.
 */
export async function GET(req: NextRequest) {
  const forbidden = requireInternal(req)
  if (forbidden) return forbidden

  const workspaceId = req.nextUrl.searchParams.get("workspace_id")
  const provider = req.nextUrl.searchParams.get("provider")

  const where: Record<string, unknown> = {
    status: "ACTIVE",
    deleted_at: null,
    type: { in: ["AI_CLI_TOKEN", "API_KEY"] },
    provider: { not: "NONE" },
  }
  if (workspaceId) where.workspace_id = workspaceId
  if (provider) where.provider = provider

  const credentials = await prisma.credential.findMany({
    where,
    select: {
      id: true,
      workspace_id: true,
      name: true,
      type: true,
      provider: true,
      encrypted_value: true,
      encrypted_refresh_token: true,
      token_expires_at: true,
      account_label: true,
      account_email: true,
      status: true,
    },
    orderBy: [{ type: "asc" }, { created_at: "asc" }],
  })

  // SECURITY NOTE: Plaintext tokens are intentionally returned here.
  // This is an internal-only endpoint (requireInternal auth) consumed by crewshipd
  // for the LLM token pool. Never exposed to browsers or external clients.
  const result = credentials.map((cred) => ({
    id: cred.id,
    workspace_id: cred.workspace_id,
    name: cred.name,
    type: cred.type,
    provider: cred.provider,
    access_token: decrypt(cred.encrypted_value),
    refresh_token: cred.encrypted_refresh_token
      ? decrypt(cred.encrypted_refresh_token)
      : null,
    token_expires_at: cred.token_expires_at,
    account_label: cred.account_label,
    status: cred.status,
  }))

  return NextResponse.json(result)
}
