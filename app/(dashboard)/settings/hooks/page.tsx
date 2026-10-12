"use client"

import { Suspense } from "react"

import { HooksPage } from "@/components/features/settings/hooks/hooks-page"

// HooksPage reads ?hook= through useSearchParams, which a static export only
// renders inside a Suspense boundary.
export default function SettingsHooksPage() {
  return (
    <Suspense>
      <HooksPage />
    </Suspense>
  )
}
