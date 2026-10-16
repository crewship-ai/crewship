"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import {
  Bell, BookOpen, ChevronDown, ChevronRight, CircleAlert, GitBranch, HelpCircle,
  LogOut, MonitorSmartphone, Sparkles, SquareTerminal, User, Volume2,
} from "lucide-react"

import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAccessMode } from "@/hooks/use-access-mode"
import { apiFetch } from "@/lib/api-fetch"
import {
  SOUND_PREFERENCES_EVENT, readSoundPreferences, saveSoundPreferences, unlockNotificationAudio,
} from "@/lib/notification-sounds"
import { cn } from "@/lib/utils"
import { UserAvatar } from "@/components/ui/user-avatar"
import { Badge } from "@/components/ui/badge"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { buildLabel } from "./sidebar-version"
import { ThemeSwitcher } from "./theme-switcher"
import { ConnectCliDialog } from "./connect-cli-dialog"

// External destinations for the user menu. Kept here (not env-driven) because
// they are stable public properties; the docs site is the Mintlify source of
// truth and Report an issue routes to the GitHub issue tracker.
export const DOCS_URL = "https://docs.crewship.ai"
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
export function UserMenu() {
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
      <DropdownMenu open={open} onOpenChange={setOpen}>
        <DropdownMenuTrigger asChild>
          <button
            className="flex items-center gap-2 rounded-full py-0.5 pl-0.5 pr-1.5 coarse:min-h-12 hover:bg-accent data-[state=open]:bg-accent transition-colors"
            aria-label="User menu"
          >
            <UserAvatar name={userName} email={userEmail} src={userAvatar} className="h-7 w-7" textClassName="text-micro" />
            <span className="text-xs font-medium hidden sm:inline">{userName.split(" ")[0]}</span>
            <ChevronDown className="h-3 w-3 text-muted-foreground hidden sm:block" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-72 p-0">
          <div className="p-1">
            <DropdownMenuItem asChild className="items-center gap-3 px-2 py-2.5">
              <Link href="/settings?tab=profile" aria-label={`Your profile: ${userName}`}>
                <UserAvatar name={userName} email={userEmail} src={userAvatar} className="h-10 w-10" textClassName="text-sm" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium" title={userName}>{userName}</span>
                  <span className="block truncate text-xs text-muted-foreground" title={userEmail}>{userEmail}</span>
                </span>
                <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
              </Link>
            </DropdownMenuItem>
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
          <DropdownMenuSeparator className="my-0" />
          <div className="p-1">
            <DropdownMenuItem asChild className={ITEM}>
              <Link href="/settings?tab=profile">
                <User className={ICON} />
                Profile &amp; account
              </Link>
            </DropdownMenuItem>
            {/* Not a menu item: a row holding its own radiogroup, so picking a
                theme keeps the menu open and the change is visible. */}
            <div className="flex h-8 items-center gap-3 px-2 text-xs">
              <Sparkles className={ICON} aria-hidden />
              <span className="flex-1">Theme</span>
              <ThemeSwitcher size="sm" />
            </div>
            {soundScope && <SoundsToggleItem key={soundScope} scope={soundScope} />}
            {trusted && (
              <DropdownMenuItem asChild className={ITEM}>
                <Link href="/integrations?tab=notifications&section=preferences">
                  <Bell className={ICON} />
                  Notification preferences
                </Link>
              </DropdownMenuItem>
            )}
            <DropdownMenuItem asChild className={ITEM}>
              <Link href="/settings?tab=sessions">
                <MonitorSmartphone className={ICON} />
                <span className="flex-1">Sessions &amp; CLI tokens</span>
                <ActiveSessionsCount />
              </Link>
            </DropdownMenuItem>
            {canPair && (
              <DropdownMenuItem
                className={ITEM}
                onSelect={() => {
                  // Let the menu close first; a dialog opened from inside it
                  // would have its focus restored to the trigger and shut.
                  setTimeout(() => setPairOpen(true), 0)
                }}
              >
                <SquareTerminal className={ICON} />
                Connect CLI…
              </DropdownMenuItem>
            )}
          </div>
          <DropdownMenuSeparator className="my-0" />
          <div className="p-1">
            <DropdownMenuSub>
              <DropdownMenuSubTrigger className={ITEM}>
                <HelpCircle className={ICON} />
                Help &amp; resources
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-56">
                <DropdownMenuItem asChild className={ITEM}>
                  <a href={DOCS_URL} target="_blank" rel="noopener noreferrer">
                    <BookOpen className={ICON} />
                    Documentation
                  </a>
                </DropdownMenuItem>
                <DropdownMenuItem asChild className={ITEM}>
                  <a href={RELEASES_URL} target="_blank" rel="noopener noreferrer">
                    <Sparkles className={ICON} />
                    What&apos;s new
                  </a>
                </DropdownMenuItem>
                <DropdownMenuItem asChild className={ITEM}>
                  <a href={SUPPORT_URL} target="_blank" rel="noopener noreferrer">
                    <CircleAlert className={ICON} />
                    Report an issue
                  </a>
                </DropdownMenuItem>
                <DropdownMenuItem asChild className={ITEM}>
                  <a href={GITHUB_URL} target="_blank" rel="noopener noreferrer">
                    <GitBranch className={ICON} />
                    GitHub
                  </a>
                </DropdownMenuItem>
              </DropdownMenuSubContent>
            </DropdownMenuSub>
          </div>
          <DropdownMenuSeparator className="my-0" />
          <div className="flex items-center p-1">
            <DropdownMenuItem
              className={cn(ITEM, "flex-1 text-destructive")}
              onClick={() => { signOut().then(() => window.location.href = "/login") }}
            >
              <LogOut className="h-4 w-4" />
              Log out
            </DropdownMenuItem>
            {trusted && <BuildFooter />}
          </div>
        </DropdownMenuContent>
      </DropdownMenu>
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
    <DropdownMenuItem
      role="menuitemcheckbox"
      aria-checked={on}
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
    </DropdownMenuItem>
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

/** Which build this is, at the foot of the menu — the first thing a bug
 *  report needs. Same read and label as the rail's SidebarVersion. */
function BuildFooter() {
  const [label, setLabel] = useState<string | null>(null)
  useEffect(() => {
    let live = true
    apiFetch("/api/v1/system/version")
      .then((r) => (r.ok ? r.json() : null))
      .then((b) => { if (live && b?.current) setLabel(buildLabel(b)) })
      .catch(() => {})
    return () => { live = false }
  }, [])
  if (!label) return null
  return <span className="shrink-0 px-2 font-mono text-micro text-muted-foreground-soft" data-testid="user-menu-build">{label}</span>
}
