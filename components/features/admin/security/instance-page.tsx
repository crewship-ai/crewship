"use client"

import * as React from "react"
import { ChevronLeft } from "lucide-react"

import { Button } from "@/components/ui/button"
import { SettingsPageHead } from "@/components/features/settings/shared"
import { WholeInstanceMark } from "./security-kit"
import { INSTANCE_ITEMS, type InstanceItem } from "./security-model"
import type { LucideIcon } from "lucide-react"

/**
 * The frame of an instance setting: "← Overview", the head with its one
 * sentence, and the mark that says it reaches every workspace on the server.
 */
export function InstancePage({ item, icon, onBack, children }: { item: InstanceItem; icon: LucideIcon; onBack: () => void; children: React.ReactNode }) {
  const meta = INSTANCE_ITEMS.find((i) => i.key === item)!
  return (
    <>
      <Button type="button" variant="ghost" size="sm" className="-ml-2 self-start" onClick={onBack}><ChevronLeft className="size-3.5" />Overview</Button>
      <SettingsPageHead icon={icon} title={meta.label} description={meta.head} actions={<WholeInstanceMark />} />
      {children}
    </>
  )
}
