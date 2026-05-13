"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

export function OrchestrationProjectRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/issues")
  }, [router])
  return null
}
