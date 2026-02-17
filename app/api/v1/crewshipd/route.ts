import { NextResponse } from "next/server"
import { healthCheck } from "@/lib/crewshipd-client"

export async function GET() {
  try {
    const res = await healthCheck()
    if (res.ok) {
      return NextResponse.json(res.data)
    }
    return NextResponse.json({ status: "unreachable" }, { status: 503 })
  } catch {
    return NextResponse.json({ status: "unreachable" }, { status: 503 })
  }
}
