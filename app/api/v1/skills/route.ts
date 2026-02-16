import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import type { SkillCategory, SkillSource } from "@/lib/generated/prisma/client"

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const category = req.nextUrl.searchParams.get("category") as SkillCategory | null
  const source = req.nextUrl.searchParams.get("source") as SkillSource | null
  const search = req.nextUrl.searchParams.get("search")

  const where: Record<string, unknown> = {}

  if (category) where.category = category
  if (source) where.source = source
  if (search) {
    where.OR = [
      { name: { contains: search, mode: "insensitive" } },
      { display_name: { contains: search, mode: "insensitive" } },
      { description: { contains: search, mode: "insensitive" } },
    ]
  }

  const skills = await prisma.skill.findMany({
    where,
    select: {
      id: true,
      name: true,
      slug: true,
      display_name: true,
      description: true,
      version: true,
      author: true,
      category: true,
      source: true,
      icon: true,
      verification: true,
      downloads: true,
      rating_avg: true,
      rating_count: true,
      tags: true,
      featured: true,
      pricing_tier: true,
      tool_count: true,
      created_at: true,
      updated_at: true,
    },
    orderBy: { name: "asc" },
  })

  return NextResponse.json(skills)
}
