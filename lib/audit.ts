import type { Prisma } from "@/lib/generated/prisma/client"
import { prisma } from "@/lib/db"

interface AuditEntry {
  orgId: string
  userId?: string | null
  action: string
  entityType: string
  entityId?: string | null
  metadata?: Prisma.InputJsonValue | null
  ipAddress?: string | null
  userAgent?: string | null
}

/**
 * Creates an immutable audit log entry.
 * Should be called from API routes after any state-changing action.
 */
export async function createAuditLog(entry: AuditEntry): Promise<void> {
  await prisma.auditLog.create({
    data: {
      org_id: entry.orgId,
      user_id: entry.userId,
      action: entry.action,
      entity_type: entry.entityType,
      entity_id: entry.entityId,
      metadata: entry.metadata ?? undefined,
      ip_address: entry.ipAddress,
      user_agent: entry.userAgent,
    },
  })
}
