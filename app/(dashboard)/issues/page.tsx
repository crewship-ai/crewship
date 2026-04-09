"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

export default function IssuesRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/orchestration")
  }, [router])
  return null
}
