/**
 * Every place inside a page that ⌘K can open directly (#3045): the cards of
 * each Settings tab, the tabs and sections of Integrations, Inbox, Activity,
 * Journal and Routines, the Admin console, and the actions that live behind a
 * button. The entity lists (agents, crews, issues, …) come from the API; this
 * is the part of the app no list endpoint describes.
 *
 * Every row carries the gate its destination applies, so the palette never
 * offers a card the page would hide or bounce the caller away from.
 */
import type { LucideIcon } from "lucide-react"
import {
  Activity,
  Archive,
  Bell,
  BookOpen,
  Building2,
  CalendarClock,
  CalendarDays,
  ClipboardCheck,
  CircleDot,
  Coins,
  Gauge,
  Inbox,
  KeyRound,
  Link2,
  Lock,
  Palette,
  Plug,
  Plus,
  ScrollText,
  Server,
  ShieldAlert,
  ShieldCheck,
  Timer,
  Trash2,
  User,
  UserPlus,
  Users,
  Volume2,
  Webhook,
} from "lucide-react"
import { isSettingsSectionVisible } from "@/components/features/settings/settings-nav"
import { settingsCardSlug } from "@/components/features/settings/shared"
import { isAdminTier, isOwner } from "@/lib/permissions/tiers"

export interface PaletteDestination {
  /** Stable key; also what Recent remembers the row by. */
  id: string
  title: string
  /** Where it lives, shown on the right: "Settings › General". */
  trail: string
  href: string
  icon: LucideIcon
  /** Words a person might type instead of the title. */
  keywords: string[]
  /** "action" rows do something; "place" rows open a page, tab or card. */
  kind: "place" | "action"
}

export interface DestinationContext {
  role: string | null | undefined
  isInstanceAdmin: boolean
}

interface Entry extends Omit<PaletteDestination, "kind"> {
  kind?: PaletteDestination["kind"]
  visible?: (ctx: DestinationContext) => boolean
}

const settingsCard = (
  tab: string,
  tabLabel: string,
  title: string,
  icon: LucideIcon,
  keywords: string[],
  visible?: (ctx: DestinationContext) => boolean,
): Entry => ({
  id: `settings:${tab}:${settingsCardSlug(title)}`,
  title,
  trail: `Settings › ${tabLabel}`,
  href: `/settings?tab=${tab}&card=${settingsCardSlug(title)}`,
  icon,
  keywords,
  // The tab's own gate first — a card in a tab the nav hides is not reachable.
  visible: (ctx) => isSettingsSectionVisible(tab, ctx.role) && (visible?.(ctx) ?? true),
})

const admin = (id: string, title: string, trail: string, href: string, icon: LucideIcon, keywords: string[]): Entry => ({
  id: `admin:${id}`,
  title,
  trail,
  href,
  icon,
  keywords: ["admin", "instance", ...keywords],
  visible: (ctx) => ctx.isInstanceAdmin,
})

const ENTRIES: Entry[] = [
  // ── Settings cards ────────────────────────────────────────────────────
  settingsCard("profile", "Profile", "Account", User, ["profile picture", "avatar", "email", "name", "password", "change password"]),
  settingsCard("profile", "Profile", "Workspace", Building2, ["my role", "organization", "joined"]),
  // Sessions & tokens is its own section with two cards (Harbor 2).
  settingsCard("sessions", "Sessions & tokens", "Browsers & devices", Lock, ["sessions", "devices", "browsers", "sign out everywhere", "revoke session"]),
  settingsCard("sessions", "Sessions & tokens", "CLI tokens", KeyRound, ["cli tokens", "api tokens", "new token", "revoke token"]),
  settingsCard("sounds", "Notification sounds", "Notification sounds", Volume2, ["sound", "do not disturb", "dnd", "volume", "mute"]),
  settingsCard("general", "General", "Identity", Building2, ["workspace name", "workspace logo", "slug", "agent language", "rename workspace"]),
  settingsCard("general", "General", "Usage", Gauge, ["counts", "agent avatars", "store missing avatars"]),
  settingsCard("general", "General", "Pages appearance", Palette, ["theme", "pages colours", "pages colors", "branding"]),
  settingsCard("general", "General", "Privileged credentials", ShieldAlert, ["security", "privileged", "fail closed"]),
  settingsCard("general", "General", "Danger zone", Trash2, ["delete workspace", "remove workspace"], (ctx) => isOwner(ctx.role)),
  // Members is a page of its own (/settings/members), not a card in a tab.
  {
    id: "settings:members",
    title: "Members",
    trail: "Settings",
    href: "/settings/members",
    icon: Users,
    keywords: ["people", "team", "invite", "roles", "permissions", "role change", "access"],
    visible: (ctx) => isSettingsSectionVisible("members", ctx.role),
  },
  settingsCard("access-secrets", "Access & Secrets", "Value reveal", KeyRound, ["reveal secrets", "show secret values"]),
  settingsCard("access-secrets", "Access & Secrets", "Who may reveal", KeyRound, ["reveal permission", "secrets access"]),
  settingsCard("access-secrets", "Access & Secrets", "Classification", KeyRound, ["secret classification", "sensitivity"]),
  settingsCard("access-secrets", "Access & Secrets", "Where credentials live", KeyRound, ["credential storage", "vault", "secret store"]),
  // Lifecycle hooks are paused (internal/hooks/paused.go), so not offered.
  {
    id: "settings:crew-links",
    title: "Crew links",
    trail: "Settings",
    href: "/settings/crew-links",
    icon: Link2,
    keywords: ["connections", "crew network", "links matrix", "peers"],
    visible: (ctx) => isSettingsSectionVisible("connections", ctx.role),
  },
  {
    id: "settings:audit",
    title: "Audit log",
    trail: "Settings",
    href: "/settings/audit",
    icon: ScrollText,
    keywords: ["audit trail", "who changed", "history of changes"],
    visible: (ctx) => isSettingsSectionVisible("audit", ctx.role),
  },

  // ── Integrations ──────────────────────────────────────────────────────
  { id: "int:connections", title: "Channels", trail: "Integrations › Channels", href: "/integrations?space=channels", icon: Bell, keywords: ["slack", "email", "discord", "teams", "telegram", "channels", "outgoing"] },
  { id: "int:preferences", title: "My notifications", trail: "Integrations › Channels", href: "/integrations?space=channels&view=preferences", icon: Bell, keywords: ["notify me", "alerts", "preferences"] },
  { id: "int:deliveries", title: "Notification deliveries", trail: "Integrations › Channels", href: "/integrations?space=channels&view=deliveries", icon: Bell, keywords: ["sent", "failed notifications", "delivery log"] },
  { id: "int:endpoints", title: "Triggers", trail: "Integrations", href: "/integrations?space=triggers", icon: Webhook, keywords: ["webhook endpoints", "inbound", "trigger url"] },
  { id: "int:incoming-routine", title: "Triggers that start routines", trail: "Integrations › Triggers", href: "/integrations?space=triggers&starts=routine", icon: Webhook, keywords: ["routine webhook"] },
  { id: "int:incoming-agent", title: "Triggers that start agents", trail: "Integrations › Triggers", href: "/integrations?space=triggers&starts=agent", icon: Webhook, keywords: ["agent webhook"] },
  { id: "int:incoming-page", title: "Triggers that feed pages", trail: "Integrations › Triggers", href: "/integrations?space=triggers&starts=page", icon: Webhook, keywords: ["page webhook", "page data"] },
  { id: "int:catalog", title: "App catalog", trail: "Integrations › Tools", href: "/integrations?space=tools&view=catalog", icon: Plug, keywords: ["apps", "install integration", "connectors", "marketplace"] },
  { id: "int:accounts", title: "Connected apps", trail: "Integrations › Tools", href: "/integrations?space=tools", icon: Plug, keywords: ["oauth", "sign in", "github", "google", "accounts"] },
  { id: "int:agents", title: "Agent access to tools", trail: "Integrations › Tools", href: "/integrations?space=tools", icon: Plug, keywords: ["agent access", "tool permissions"] },
  { id: "int:tools", title: "Tools", trail: "Integrations › Tools", href: "/integrations?space=tools", icon: Plug, keywords: ["mcp tools"] },
  { id: "int:triggers", title: "Triggers", trail: "Integrations › Tools", href: "/integrations?space=tools", icon: Plug, keywords: ["events", "app triggers"] },
  { id: "int:mcp", title: "MCP endpoints", trail: "Integrations › Tools", href: "/integrations?space=tools&view=mcp", icon: Server, keywords: ["mcp server", "endpoint"] },
  { id: "int:crew-tools", title: "Crew MCP servers", trail: "Integrations › Tools", href: "/integrations?space=tools&view=crew-tools", icon: Server, keywords: ["crew mcp servers"] },

  // ── Tabs inside pages ─────────────────────────────────────────────────
  { id: "inbox:action", title: "To handle", trail: "Inbox", href: "/inbox?view=action", icon: Inbox, keywords: ["todo", "needs me"] },
  { id: "inbox:updates", title: "Updates", trail: "Inbox", href: "/inbox?view=updates", icon: Inbox, keywords: [] },
  { id: "inbox:history", title: "Inbox history", trail: "Inbox", href: "/inbox?view=history", icon: Inbox, keywords: ["handled", "done"] },
  { id: "inbox:approvals", title: "Decisions waiting", trail: "Inbox", href: "/inbox?attention=approvals", icon: ClipboardCheck, keywords: ["approvals", "approve", "waitpoints"] },
  { id: "inbox:run-alerts", title: "Run alerts", trail: "Inbox", href: "/inbox?attention=run-alerts", icon: Inbox, keywords: ["failed runs", "errors"] },
  { id: "inbox:schedule-alerts", title: "Schedule alerts", trail: "Inbox", href: "/inbox?attention=schedule-alerts", icon: Inbox, keywords: ["missed schedules"] },
  { id: "activity:work", title: "Work queue", trail: "Activity", href: "/activity?section=work", icon: Activity, keywords: ["queue", "assignments", "running work"] },
  { id: "activity:deliveries", title: "Webhook deliveries", trail: "Activity", href: "/activity?section=deliveries", icon: Activity, keywords: ["webhook log"] },
  // The Journal's three tabs, merged into Activity (F-02) and gated as before:
  // OWNER / ADMIN only.
  { id: "journal:timeline", title: "Technical log", trail: "Activity", href: "/activity?section=log", icon: BookOpen, keywords: ["journal", "timeline", "events", "audit", "log", "trace"], visible: (ctx) => isAdminTier(ctx.role) },
  { id: "journal:runs", title: "Fleet runs", trail: "Activity", href: "/activity?section=runs", icon: BookOpen, keywords: ["journal", "runs", "executions", "run history"], visible: (ctx) => isAdminTier(ctx.role) },
  { id: "journal:spend", title: "Spend", trail: "Activity", href: "/activity?mode=spend", icon: Coins, keywords: ["journal", "cost", "usage", "tokens", "billing", "budget"], visible: (ctx) => isAdminTier(ctx.role) },
  { id: "routines:calendar", title: "Routine calendar", trail: "Routines", href: "/routines?tab=calendar", icon: CalendarDays, keywords: ["schedule", "calendar", "cron"] },
  { id: "issues:missions", title: "Missions", trail: "Issues", href: "/issues?mission_type=mission", icon: CircleDot, keywords: ["mission list"] },
  { id: "credentials:providers", title: "Model providers", trail: "Credentials", href: "/credentials?tab=providers", icon: KeyRound, keywords: ["providers", "pays with", "anthropic", "openai", "subscription"] },

  // ── Actions behind a button ───────────────────────────────────────────
  { id: "act:issue", kind: "action", title: "Create issue", trail: "Issues", href: "/issues?create=1", icon: Plus, keywords: ["new issue", "add issue", "ticket", "task"] },
  { id: "act:invite", kind: "action", title: "Invite member", trail: "Settings › Members", href: "/settings/members", icon: UserPlus, keywords: ["add person", "add user", "invite"], visible: (ctx) => isAdminTier(ctx.role) },
  { id: "act:token", kind: "action", title: "Create CLI token", trail: "Settings › Sessions & tokens", href: `/settings?tab=sessions&card=${settingsCardSlug("CLI tokens")}`, icon: KeyRound, keywords: ["new token", "api key", "cli login"] },
  { id: "act:password", kind: "action", title: "Change password", trail: "Settings › Profile", href: `/settings?tab=profile&card=${settingsCardSlug("Account")}`, icon: Lock, keywords: ["reset password"] },
  { id: "act:avatars", kind: "action", title: "Store missing avatars", trail: "Settings › General", href: `/settings?tab=general&card=${settingsCardSlug("Usage")}`, icon: Users, keywords: ["agent avatars", "faces"], visible: (ctx) => isAdminTier(ctx.role) },

  // ── Admin console (instance administrators) ───────────────────────────
  admin("overview", "Instance overview", "Admin", "/admin?tab=overview", Gauge, ["capacity", "platform", "integrity", "license"]),
  admin("runtime", "Container runtime", "Admin › Runtime", "/admin?tab=providers", Server, ["docker", "logging", "maintenance", "remove every crew runtime"]),
  admin("notifications", "Instance notifications", "Admin", "/admin?tab=notifications", Bell, ["transports", "smtp"]),
  admin("limits", "Limits", "Admin", "/admin?tab=ratelimits", Timer, ["rate limits", "quotas"]),
  admin("retention", "Data retention", "Admin", "/admin?tab=retention", Archive, ["retention", "purge", "keep data"]),
  admin("people", "People & workspaces", "Admin", "/admin/people", Users, ["users", "accounts", "workspaces", "suspend", "lock"]),
  admin("security", "Security", "Admin", "/admin/security", ShieldCheck, ["credential judge", "decision rules", "watchdog", "leases", "alerts"]),
  admin("security-judge", "Credential judge", "Admin › Security", "/admin/security?section=judge", ShieldCheck, ["judge"]),
  admin("security-rules", "Decision rules", "Admin › Security", "/admin/security?section=rules", ShieldCheck, ["rules", "policy"]),
  admin("security-watchdog", "Watchdog", "Admin › Security", "/admin/security?section=watchdog", ShieldCheck, ["watch"]),
  admin("security-leases", "Credential leases", "Admin › Security", "/admin/security?section=leases", ShieldCheck, ["lease"]),
  admin("backups", "Backups", "Admin", "/admin/backups", Archive, ["backup", "restore"]),
  admin("backups-schedules", "Backup schedules", "Admin › Backups", "/admin/backups?section=schedules", CalendarClock, ["backup schedule"]),
  admin("backups-storage", "Backup storage", "Admin › Backups", "/admin/backups?section=storage", Archive, ["s3", "bucket"]),
  admin("backups-recovery", "Recovery", "Admin › Backups", "/admin/backups?section=recovery", Archive, ["restore", "disaster recovery", "drills"]),
  admin("backups-keys", "Backup keys & alerts", "Admin › Backups", "/admin/backups?section=keys", KeyRound, ["encryption keys"]),
]

/** The destinations this caller may open, in catalog order. */
export function paletteDestinations(ctx: DestinationContext): PaletteDestination[] {
  return ENTRIES.filter((e) => e.visible?.(ctx) ?? true).map(({ visible: _v, kind = "place", ...rest }) => ({ ...rest, kind }))
}
