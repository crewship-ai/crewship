// Crewship -- Auth.js v5 middleware (Edge-compatible)
// NOTE: Cannot import from @/auth here because it pulls in Prisma (Node-only).
// Using next-auth/jwt directly for Edge middleware.

import { NextResponse } from "next/server"
import type { NextRequest } from "next/server"
import { getToken } from "next-auth/jwt"

export async function middleware(request: NextRequest) {
  const token = await getToken({ req: request })

  if (!token) {
    const loginUrl = new URL("/login", request.url)
    loginUrl.searchParams.set("callbackUrl", request.nextUrl.pathname)
    return NextResponse.redirect(loginUrl)
  }

  return NextResponse.next()
}

export const config = {
  matcher: [
    "/(dashboard)(.*)",
    "/api/v1/((?!health|webhooks).*)",
  ],
}
