// Crewship -- Auth.js v5 middleware
// Protects routes requiring authentication.
// See: https://authjs.dev/getting-started/session-management/protecting

export { auth as middleware } from "@/auth"

export const config = {
  matcher: [
    "/(dashboard)(.*)",
    "/api/v1/((?!health|webhooks).*)",
  ],
}
