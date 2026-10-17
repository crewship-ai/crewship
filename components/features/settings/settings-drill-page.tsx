"use client"

import * as React from "react"
import { Settings as SettingsIcon, type LucideIcon } from "lucide-react"

import { DrillPage } from "@/components/layout/drill-page"
import { SettingsPageHead } from "@/components/features/settings/shared"

/**
 * A Settings page one level down (Members, Crew links, Audit log): the
 * shared DrillPage frame with the section head drawn ONCE, here, from the
 * same title, icon and sentence — never again by the page (#3487 SET-07).
 *
 * The top bar names the place ("Settings › Members") and carries the page's
 * actions; the section head under it says what the page is in one sentence,
 * with who may change it on the right (`note`). The head is drawn before the
 * page's data arrives, so a loading page already says where you are.
 *
 * The sentence is not passed to the top bar: DrillPage would print it a
 * second time beside the title. Making the shared frame own the head for
 * every nested page (Admin's too) is a kit request — see the PR.
 */
export function SettingsDrillPage({
  title,
  icon,
  description,
  note,
  panelId,
  toolbar,
  nav,
  mobileNav,
  actions,
  headless = false,
  children,
}: {
  title: string
  icon: LucideIcon
  /** The section head's one sentence. */
  description: string
  /** Right of the head: who may change what is here. */
  note?: React.ReactNode
  panelId: string
  toolbar?: React.ReactNode
  nav?: React.ReactNode
  mobileNav?: React.ReactNode
  actions?: React.ReactNode
  /** No head (a page that shows only a no-access state). */
  headless?: boolean
  children: React.ReactNode
}) {
  return (
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
      panelId={panelId}
      title={title}
      icon={icon}
      toolbar={toolbar}
      nav={nav}
      mobileNav={mobileNav}
      actions={actions}
    >
      {!headless && (
        <div className="px-4 pt-4 md:px-6 md:pt-6">
          <SettingsPageHead
            icon={icon}
            title={title}
            description={description}
            actions={note != null ? <span className="text-label text-muted-foreground" data-slot="settings-page-note">{note}</span> : undefined}
          />
        </div>
      )}
      {children}
    </DrillPage>
  )
}
