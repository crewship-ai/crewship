"use client"

import * as React from "react"
import { Settings as SettingsIcon, type LucideIcon } from "lucide-react"

import { DrillPage } from "@/components/layout/drill-page"
import type { PanelSpacesConfig } from "@/components/layout/panel-spaces"

/**
 * A Settings page one level down (Members, Crew links, Audit log): the kit
 * DrillPage with Settings as the parent. The kit owns the path in the top bar
 * ("Settings / Members"), the section head - drawn from the same title, icon
 * and sentence, once, before the data arrives - and the panel's back row,
 * facets and spaces (#3487 SET-07, PRD SHL-05). `note` is who may change what
 * is here, on the right of the head.
 */
export function SettingsDrillPage<S extends string = string>({
  title,
  icon,
  description,
  note,
  panelId,
  toolbar,
  facets,
  spaces,
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
  facets?: React.ReactNode
  spaces?: PanelSpacesConfig<S>
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
      description={description}
      note={note}
      head={!headless}
      toolbar={toolbar}
      facets={facets}
      spaces={spaces}
      nav={nav}
      mobileNav={mobileNav}
      actions={actions}
    >
      {children}
    </DrillPage>
  )
}
