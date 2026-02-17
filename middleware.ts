// Crewship -- Auth.js v5 middleware (Edge-compatible)
// Cookie-existence gate only. Real auth validation happens server-side
// in API routes via requireAuth() -> auth(). We cannot import @/auth here
// because Prisma is not Edge-compatible.

import { NextResponse } from "next/server"
import type { NextRequest } from "next/server"

const PUBLIC_PATHS = [
  "/login",
  "/signup",
  "/api/auth",       // Auth.js handlers (session, csrf, callback, etc.)
  "/api/v1/health",
  "/api/v1/webhooks",
  "/api/v1/internal",
]

export function middleware(request: NextRequest) {
  const { pathname } = request.nextUrl

  // Skip public paths
  if (PUBLIC_PATHS.some((p) => pathname === p || pathname.startsWith(p + "/"))) {
    return NextResponse.next()
  }

  const rawProto = request.headers.get("x-forwarded-proto")
  const proto = rawProto
    ? rawProto.split(",")[0].trim().toLowerCase()
    : request.nextUrl.protocol.replace(":", "")
  const isSecure = proto === "https"
  const cookieName = isSecure
    ? "__Secure-authjs.session-token"
    : "authjs.session-token"
  const sessionToken = request.cookies.get(cookieName)?.value

  if (!sessionToken) {
    const loginUrl = new URL("/login", request.url)
    const callbackUrl = pathname + (request.nextUrl.search || "")
    loginUrl.searchParams.set("callbackUrl", callbackUrl)
    return NextResponse.redirect(loginUrl)
  }

  return NextResponse.next()
}

export const config = {
  // Run on all routes except static assets and Next.js internals
  matcher: ["/((?!_next/|favicon\\.ico|crewship\\.svg).*)"],
}
