import { NextResponse } from "next/server"

/** Health check endpoint for container healthcheck probes. */
export function GET() {
  return NextResponse.json({ status: "ok" })
}
