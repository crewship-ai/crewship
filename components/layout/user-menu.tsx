"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import {
  Bell, BookOpen, ChevronDown, ChevronRight, ChevronsUpDown, CircleAlert, GitBranch, HelpCircle,
  LogOut, MonitorSmartphone, Palette, Sparkles, SquareTerminal, User, Volume2,
} from "lucide-react"

import { useAuth, useIsInstanceAdmin } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAccessMode } from "@/hooks/use-access-mode"
import { apiFetch } from "@/lib/api-fetch"
import {
  SOUND_PREFERENCES_EVENT, readSoundPreferences, saveSoundPreferences, unlockNotificationAudio,
} from "@/lib/notification-sounds"
import { cn } from "@/lib/utils"
import { UserAvatar } from "@/components/ui/user-avatar"
import { Badge } from "@/components/ui/badge"
import { MenuSheet, MenuSheetItem, MenuSheetSeparator, MenuSheetSub } from "@/components/ui/menu-sheet"
import { buildLabel, editionName } from "@/lib/build-info"
import { ThemeSwitcher } from "./theme-switcher"
import { ConnectCliDialog } from "./connect-cli-dialog"

// External destinations for the user menu. Kept here (not env-driven) because
// they are stable public properties; the docs site is the Mintlify source of
// truth and Report an issue routes to the GitHub issue tracker.
export const DOCS_URL = "https://docs.crewship.ai"

/** OWNER → Owner, the way the role reads under a name. */
function roleLabel(role: string): string {
  return role.charAt(0) + role.slice(1).toLowerCase()
}
export const GITHUB_URL = "https://github.com/crewship-ai/crewship"
export const SUPPORT_URL = "https://github.com/crewship-ai/crewship/issues"
export const RELEASES_URL = "https://github.com/crewship-ai/crewship/releases"

const ITEM = "gap-3 text-xs"
const ICON = "h-4 w-4 text-muted-foreground"

/**
 * The profile menu: everything about YOU, on every screen size.
 *
 * Ordered by what a person reaches for: who am I signed in as, then my own
 * settings, then help, then out. It used to open on a Theme radiogroup and an
 * Accent swatch row — two thirds of the menu's height spent on a choice made
 * once — with three external links stacked under the personal ones. Theme
 * stays as one compact row (the one appearance switch people flip by time of
 * day); accent and the rest of appearance live in Settings › Appearance.
 *
 * The workspace is stated, not switched, here. The switcher is the top of the
 * left rail, because the workspace is the context of everything in that rail;
 * this menu is about the person, which holds across workspaces.
 */
export function UserMenu({ placement = "toolbar" }: {
  /** "sidebar": the last row of the sidebar footer (Harbor 2) — avatar, name
   *  and role, opening to the right. "toolbar": the phone top bar. "row": the
   *  user row of the phone More sheet, opening upward. */
  placement?: "toolbar" | "sidebar" | "row"
} = {}) {
  const { session, signOut } = useAuth()
  const { workspace, workspaceId, role: workspaceRole } = useWorkspace()
  // Guest browser pairing is account security (#3310). Profile and
  // notification settings still require trusted access.
  const accessMode = useAccessMode()
  const trusted = accessMode === "trusted"
  const canPair = accessMode !== "loading"
  const [open, setOpen] = useState(false)
  const [pairOpen, setPairOpen] = useState(false)

  const userName = session?.user?.name ?? "User"
  const userEmail = session?.user?.email ?? ""
  const userAvatar = session?.user?.avatar_url ?? ""
  const userId = session?.user?.id
  const soundScope = userId && workspaceId ? JSON.stringify([userId, workspaceId]) : null

  return (
    <>
      <MenuSheet
        open={open}
        onOpenChange={setOpen}
        label="User menu"
        popoverAlign="end"
        popoverSide={placement === "sidebar" ? "right" : placement === "row" ? "top" : "bottom"}
        popoverOffset={placement === "sidebar" ? 8 : 4}
        popoverClassName="w-72 p-0"
        trigger={
          placement === "sidebar" ? (
            <button type="button" className="rail-user" aria-label="User menu">
              <UserAvatar name={userName} email={userEmail} src={userAvatar} className="size-6 shrink-0" textClassName="text-micro" />
              <span className="rail-label grid min-w-0 flex-1 leading-tight">
                <span className="truncate text-label font-medium text-foreground">{userName}</span>
                {workspaceRole && <span className="truncate text-micro text-muted-foreground-soft">{roleLabel(workspaceRole)}</span>}
              </span>
              <ChevronsUpDown className="rail-label size-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
            </button>
          ) : placement === "row" ? (
            <button type="button" className="rail-user" aria-label="User menu">
              <UserAvatar name={userName} email={userEmail} src={userAvatar} className="size-6 shrink-0" textClassName="text-micro" />
              <span className="min-w-0 flex-1 truncate text-left">
                {userName}{workspaceRole ? ` · ${roleLabel(workspaceRole)}` : ""}
              </span>
              <ChevronRight className="size-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
            </button>
          ) : (
            <button
              className="flex items-center gap-2 rounded-full py-0.5 pl-0.5 pr-1.5 coarse:min-h-[44px] hover:bg-accent data-[state=open]:bg-accent transition-colors"
              aria-label="User menu"
            >
              <UserAvatar name={userName} email={userEmail} src={userAvatar} className="h-7 w-7" textClassName="text-micro" />
              <span className="text-xs font-medium hidden sm:inline">{userName.split(" ")[0]}</span>
              <ChevronDown className="h-3 w-3 text-muted-foreground hidden sm:block" />
            </button>
          )
        }
      >
          <div className="p-1">
            <MenuSheetItem asChild className="items-center gap-3 px-2 py-2.5">
              <Link href="/settings?tab=profile" aria-label={`Your profile: ${userName}`}>
                <UserAvatar name={userName} email={userEmail} src={userAvatar} className="h-10 w-10" textClassName="text-sm" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium" title={userName}>{userName}</span>
                  <span className="block truncate text-xs text-muted-foreground" title={userEmail}>{userEmail}</span>
                </span>
                <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
              </Link>
            </MenuSheetItem>
            <div className="flex items-center gap-1.5 px-2 pb-1.5">
              <Badge variant="outline" className="shrink-0 text-micro px-1.5 py-0.5">
                {workspaceRole ? workspaceRole.charAt(0) + workspaceRole.slice(1).toLowerCase() : "Loading role…"}
              </Badge>
              {workspace?.name && (
                <span className="truncate text-micro text-muted-foreground" title={workspace.name}>
                  in {workspace.name}
                </span>
              )}
            </div>
          </div>
          <MenuSheetSeparator />
          <div className="p-1">
            <MenuSheetItem asChild className={ITEM}>
              <Link href="/settings?tab=profile">
                <User className={ICON} />
                Profile &amp; account
              </Link>
            </MenuSheetItem>
            <MenuSheetItem asChild className={ITEM}>
              <Link href="/settings?tab=appearance">
                <Palette className={ICON} />
                Appearance
              </Link>
            </MenuSheetItem>
            {/* Not a menu item: a row holding its own radiogroup, so picking a
                theme keeps the menu open and the change is visible. */}
            <div className="flex h-8 items-center gap-3 px-2 text-xs">
              <Sparkles className={ICON} aria-hidden />
              <span className="flex-1">Theme</span>
              <ThemeSwitcher size="sm" />
            </div>
            {soundScope && <SoundsToggleItem key={soundScope} scope={soundScope} />}
            {trusted && (
              <MenuSheetItem asChild className={ITEM}>
                <Link href="/integrations?tab=notifications&section=preferences">
                  <Bell className={ICON} />
                  Notification preferences
                </Link>
              </MenuSheetItem>
            )}
            <MenuSheetItem asChild className={ITEM}>
              <Link href="/settings?tab=sessions">
                <MonitorSmartphone className={ICON} />
                <span className="flex-1">Sessions &amp; CLI tokens</span>
                <ActiveSessionsCount />
              </Link>
            </MenuSheetItem>
            {canPair && (
              <MenuSheetItem
                className={ITEM}
                onSelect={() => {
                  // Let the menu close first; a dialog opened from inside it
                  // would have its focus restored to the trigger and shut.
                  setTimeout(() => setPairOpen(true), 0)
                }}
              >
                <SquareTerminal className={ICON} />
                Connect CLI…
              </MenuSheetItem>
            )}
          </div>
          <MenuSheetSeparator />
          <div className="p-1">
            <MenuSheetSub
              label="Help & resources"
              triggerClassName={ITEM}
              contentClassName="w-56"
              trigger={<><HelpCircle className={ICON} />Help &amp; resources</>}
            >
                <MenuSheetItem asChild className={ITEM}>
                  <a href={DOCS_URL} target="_blank" rel="noopener noreferrer">
                    <BookOpen className={ICON} />
                    Documentation
                  </a>
                </MenuSheetItem>
                <MenuSheetItem asChild className={ITEM}>
                  <a href={RELEASES_URL} target="_blank" rel="noopener noreferrer">
                    <Sparkles className={ICON} />
                    What&apos;s new
                  </a>
                </MenuSheetItem>
                <MenuSheetItem asChild className={ITEM}>
                  <a href={SUPPORT_URL} target="_blank" rel="noopener noreferrer">
                    <CircleAlert className={ICON} />
                    Report an issue
                  </a>
                </MenuSheetItem>
                <MenuSheetItem asChild className={ITEM}>
                  <a href={GITHUB_URL} target="_blank" rel="noopener noreferrer">
                    <GitBranch className={ICON} />
                    GitHub
                  </a>
                </MenuSheetItem>
            </MenuSheetSub>
          </div>
          <MenuSheetSeparator />
          <div className="p-1">
            <MenuSheetItem
              className={ITEM}
              onClick={() => { signOut().then(() => window.location.href = "/login") }}
            >
              <LogOut className={ICON} />
              Log out
            </MenuSheetItem>
          </div>
          {trusted && <BuildFooter />}
      </MenuSheet>
      {canPair && <ConnectCliDialog open={pairOpen} onOpenChange={setPairOpen} />}
    </>
  )
}

/**
 * Sounds on/off without leaving the page. The detail — which sound, volume,
 * do not disturb — stays in Settings › Notification sounds, which reads the
 * same per-user-per-workspace store and follows this through its event.
 */
function SoundsToggleItem({ scope }: { scope: string }) {
  const [prefs, setPrefs] = useState(() => readSoundPreferences(scope))
  useEffect(() => {
    const reload = () => setPrefs(readSoundPreferences(scope))
    window.addEventListener("storage", reload)
    window.addEventListener(SOUND_PREFERENCES_EVENT, reload)
    return () => {
      window.removeEventListener("storage", reload)
      window.removeEventListener(SOUND_PREFERENCES_EVENT, reload)
    }
  }, [scope])

  const on = prefs.enabled
  return (
    <MenuSheetItem
      checked={on}
      className={ITEM}
      onSelect={(e) => {
        e.preventDefault()
        const next = { ...prefs, enabled: !on }
        setPrefs(next)
        try { saveSoundPreferences(scope, next) } catch { /* private browsing: the toggle still reflects intent */ }
        // Turning sounds on is a click — the gesture browsers want before
        // they let a page play audio. Spend it here rather than make the
        // user find "Activate audio" in Settings.
        if (!on) void unlockNotificationAudio().catch(() => {})
      }}
    >
      <Volume2 className={ICON} />
      <span className="flex-1">Notification sounds</span>
      <span
        aria-hidden
        className={cn(
          "relative h-4 w-7 shrink-0 rounded-full transition-colors",
          on ? "bg-primary" : "bg-muted-foreground/30",
        )}
      >
        <span className={cn("absolute top-0.5 h-3 w-3 rounded-full bg-white transition-all", on ? "left-3.5" : "left-0.5")} />
      </span>
    </MenuSheetItem>
  )
}

/** "3 active" beside Sessions — read when the menu opens (the content only
 *  mounts while open), so an unrecognised session is one glance away. */
function ActiveSessionsCount() {
  const [count, setCount] = useState<number | null>(null)
  useEffect(() => {
    let live = true
    apiFetch("/api/v1/auth/sessions")
      .then((r) => (r.ok ? r.json() : null))
      .then((data: unknown) => {
        if (!live) return
        const list = Array.isArray(data) ? data : Array.isArray((data as { sessions?: unknown[] } | null)?.sessions) ? (data as { sessions: unknown[] }).sessions : null
        if (list) setCount(list.length)
      })
      .catch(() => {})
    return () => { live = false }
  }, [])
  if (count === null) return null
  return <span className="font-mono text-micro text-muted-foreground">{count} active</span>
}

/** The foot of the menu (v67 `pu-ver`): "Community Edition · dev · a41d725" —
 *  which Crewship this is, the first thing a bug report needs — and, when a
 *  newer release exists, a link to it. The sidebar no longer carries this; an
 *  instance admin can follow the build to the Admin overview. */
function BuildFooter() {
  const instanceAdmin = useIsInstanceAdmin()
  const [build, setBuild] = useState<{ label: string; update: string | null } | null>(null)
  const [edition, setEdition] = useState<string | undefined>()
  useEffect(() => {
    let live = true
    apiFetch("/api/v1/system/version")
      .then((r) => (r.ok ? r.json() : null))
      .then((b) => {
        if (live && b?.current) setBuild({ label: buildLabel(b), update: b.newer ? (b.url ?? RELEASES_URL) : null })
      })
      .catch(() => {})
    apiFetch("/api/v1/system/license")
      .then((r) => (r.ok ? r.json() : null))
      .then((l) => { if (live && l?.edition) setEdition(l.edition) })
      .catch(() => {})
    return () => { live = false }
  }, [])
  if (!build) return null
  const text = (
    <>
      {edition && <><span data-testid="user-menu-edition">{editionName(edition)}</span> · </>}
      <span data-testid="user-menu-build">{build.label}</span>
    </>
  )
  const cls = "min-w-0 truncate font-mono text-micro text-muted-foreground-soft"
  return (
    <div className="flex items-center justify-between gap-3 border-t border-border px-3 py-2" data-testid="user-menu-footer">
      {instanceAdmin ? (
        <Link href="/admin?tab=overview" className={cn(cls, "hover:text-foreground hover:underline")}>{text}</Link>
      ) : (
        <span className={cls}>{text}</span>
      )}
      {build.update && (
        <a href={build.update} target="_blank" rel="noopener noreferrer" className="shrink-0 text-micro font-medium text-primary hover:underline">
          Update available
        </a>
      )}
    </div>
  )
}
