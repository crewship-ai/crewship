"use client"

import { Suspense } from "react"

import { MembersPage } from "@/components/features/settings/members/members-page"

// MembersPage reads ?member= and the filters through useSearchParams, which a
// static export only renders inside a Suspense boundary.
export default function SettingsMembersPage() {
  return (
    <Suspense>
      <MembersPage />
    </Suspense>
  )
}
