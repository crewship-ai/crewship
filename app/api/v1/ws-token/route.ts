import { NextResponse } from "next/server"
import { cookies } from "next/headers"
import { auth } from "@/auth"

const COOKIE_NAMES = [
  "__Secure-authjs.session-token",
  "authjs.session-token",
]

export async function GET() {
  const session = await auth()
  if (!session?.user?.id) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 })
  }

  const cookieStore = await cookies()
  let token: string | undefined

  for (const name of COOKIE_NAMES) {
    const cookie = cookieStore.get(name)
    if (cookie?.value) {
      token = cookie.value
      break
    }
  }

  if (!token) {
    return NextResponse.json(
      { error: "Session token not found" },
      { status: 401 },
    )
  }

  return NextResponse.json({ token })
}
