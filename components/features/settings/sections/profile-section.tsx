"use client"

import { LogOut } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

const roleCls: Record<string, string> = {
  OWNER: "bg-amber-500/20 text-amber-400 border-amber-500/30",
  ADMIN: "bg-blue-500/20 text-blue-400 border-blue-500/30",
  MANAGER: "bg-teal-500/20 text-teal-400 border-teal-500/30",
  MEMBER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
  VIEWER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
}

function timeUntil(dateStr: string): string {
  const diff = new Date(dateStr).getTime() - Date.now()
  if (diff <= 0) return "Expired"
  const hours = Math.floor(diff / (1000 * 60 * 60))
  const minutes = Math.floor((diff % (1000 * 60 * 60)) / (1000 * 60))
  if (hours > 24) return `in ${Math.floor(hours / 24)}d ${hours % 24}h`
  if (hours > 0) return `in ${hours}h ${minutes}m`
  return `in ${minutes}m`
}

interface ProfileSectionProps {
  userName?: string | null
  userEmail?: string | null
  role?: string | null
  workspaceName?: string | null
  joinedAt?: string | null
  sessionExpires?: string | null
  onSignOut?: () => void
}

function Row({ label, value, mono, action }: {
  label: string
  value: React.ReactNode
  mono?: boolean
  action?: React.ReactNode
}) {
  return (
    <div className="flex items-center justify-between py-2.5 min-h-[36px]">
      <span className="text-[13px] text-muted-foreground/60 shrink-0 w-[140px]">{label}</span>
      <span className={cn("text-[13px] text-foreground flex-1 truncate", mono && "font-mono")}>{value}</span>
      {action && <div className="shrink-0 ml-3">{action}</div>}
    </div>
  )
}

function P2Button() {
  return (
    <span className="text-[9px] text-muted-foreground/30 border border-white/[0.06] px-1.5 py-0.5 rounded cursor-default">
      P2
    </span>
  )
}

export function ProfileSection({
  userName,
  userEmail,
  role,
  workspaceName,
  joinedAt,
  sessionExpires,
  onSignOut,
}: ProfileSectionProps) {
  const initials = (userName ?? "U")
    .split(" ")
    .map((n) => n[0])
    .join("")
    .slice(0, 2)
    .toUpperCase()

  return (
    <div className="bg-card border border-white/[0.06] rounded-lg overflow-hidden">
      {/* Header */}
      <div className="px-6 py-5 border-b border-white/[0.06]">
        <div className="flex items-center gap-4">
          <div className="w-14 h-14 rounded-full bg-primary/80 ring-2 ring-white/[0.08] flex items-center justify-center text-primary-foreground text-[15px] font-semibold shrink-0">
            {initials}
          </div>
          <div className="min-w-0">
            <h3 className="text-[16px] font-semibold text-foreground truncate">
              {userName ?? "User"}
            </h3>
            <p className="text-[13px] text-muted-foreground/50 mt-0.5 truncate font-mono">
              {userEmail ?? ""}
            </p>
            <div className="flex items-center gap-2 mt-1.5">
              {role && (
                <Badge
                  variant="outline"
                  className={cn("text-[10px] font-medium", roleCls[role] ?? "")}
                >
                  {role}
                </Badge>
              )}
              {workspaceName && (
                <span className="text-[11px] text-muted-foreground/30">{workspaceName}</span>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Account */}
      <div className="px-6 py-3 border-b border-white/[0.06]">
        <div className="text-[10px] font-semibold text-muted-foreground/30 uppercase tracking-wider mb-1">
          Account
        </div>
        <Row label="Full Name" value={userName ?? "Not set"} action={<P2Button />} />
        <Row label="Email" value={userEmail ?? "Not set"} mono action={<P2Button />} />
        <Row label="Password" value="••••••••••" action={<P2Button />} />
      </div>

      {/* Workspace */}
      <div className="px-6 py-3 border-b border-white/[0.06]">
        <div className="text-[10px] font-semibold text-muted-foreground/30 uppercase tracking-wider mb-1">
          Workspace
        </div>
        <Row label="Role" value={role ?? "Not assigned"} />
        {workspaceName && <Row label="Organization" value={workspaceName} />}
        {joinedAt && (
          <Row label="Joined" value={new Date(joinedAt).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })} />
        )}
      </div>

      {/* Session */}
      <div className="px-6 py-3">
        <div className="text-[10px] font-semibold text-muted-foreground/30 uppercase tracking-wider mb-1">
          Session
        </div>
        <Row
          label="Status"
          value={
            <span className="flex items-center gap-1.5">
              <span className="w-1.5 h-1.5 rounded-full bg-emerald-500" />
              Active
            </span>
          }
        />
        {sessionExpires && (
          <Row label="Expires" value={<span className="text-muted-foreground/60">{timeUntil(sessionExpires)}</span>} />
        )}
        <div className="pt-1 pb-1">
          <button
            onClick={onSignOut}
            className="flex items-center gap-1.5 text-[12px] text-red-400/70 hover:text-red-400 transition-colors"
          >
            <LogOut className="h-3 w-3" />
            Sign out
          </button>
        </div>
      </div>
    </div>
  )
}
