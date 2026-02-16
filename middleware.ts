// Crewship -- Auth.js v5 middleware (Edge-compatible)
// Cookie-existence gate only. Real auth validation happens server-side
// in API routes via requireAuth() -> auth(). We cannot import @/auth here
// because Prisma is not Edge-compatible.

import { NextResponse } from "next/server"
import type { NextRequest } from "next/server"

export function middleware(request: NextRequest) {
  const cookieName = request.nextUrl.protocol === "https:"
    ? "__Secure-authjs.session-token"
    : "authjs.session-token"
  const sessionToken = request.cookies.get(cookieName)?.value

  if (!sessionToken) {
    const loginUrl = new URL("/login", request.url)
    loginUrl.searchParams.set("callbackUrl", request.nextUrl.pathname)
    return NextResponse.redirect(loginUrl)
  }

  return NextResponse.next()
}

export const config = {
  matcher: [
    "/(dashboard)(.*)",
    "/api/v1/((?!health|webhooks|internal).*)",
  ],
}
