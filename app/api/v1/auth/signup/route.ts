import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { hashSync } from "bcryptjs"
import { prisma } from "@/lib/db"

const signupSchema = z.object({
  full_name: z.string().min(2, "Name must be at least 2 characters"),
  email: z.string().email("Invalid email address"),
  password: z.string().min(8, "Password must be at least 8 characters"),
})

export async function POST(req: NextRequest) {
  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }
  const parsed = signupSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const { full_name, email, password } = parsed.data

  const existing = await prisma.user.findUnique({
    where: { email },
    select: { id: true },
  })

  if (existing) {
    return NextResponse.json({ error: "Email already registered" }, { status: 409 })
  }

  const hashed_password = hashSync(password, 12)

  const slugBase = email.split("@")[0]?.replace(/[^a-z0-9-]/gi, "-").toLowerCase() ?? "user"

  const user = await prisma.user.create({
    data: {
      full_name,
      email,
      hashed_password,
      org_memberships: {
        create: {
          role: "OWNER",
          organization: {
            create: {
              name: `${full_name}'s Org`,
              slug: `${slugBase}-${Date.now()}`,
            },
          },
        },
      },
    },
    select: { id: true, email: true },
  })

  return NextResponse.json(user, { status: 201 })
}
