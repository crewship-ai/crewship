"use client"

import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

const roleCls: Record<string, string> = {
  OWNER: "bg-amber-500/20 text-amber-400 border-amber-500/30",
  ADMIN: "bg-blue-500/20 text-blue-400 border-blue-500/30",
  MANAGER: "bg-teal-500/20 text-teal-400 border-teal-500/30",
  MEMBER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
  VIEWER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
}

interface ProfileSectionProps {
  userName?: string | null
  userEmail?: string | null
  role?: string | null
}

export function ProfileSection({ userName, userEmail, role }: ProfileSectionProps) {
  const initials = (userName ?? "U")
    .split(" ")
    .map((n) => n[0])
    .join("")
    .slice(0, 2)
    .toUpperCase()

  return (
    <div className="space-y-6">
      {/* Profile card */}
      <div className="bg-card border border-white/[0.06] rounded-lg p-6">
        <div className="flex items-center gap-5">
          <div className="w-16 h-16 rounded-full bg-primary/80 ring-2 ring-white/[0.1] flex items-center justify-center text-primary-foreground text-lg font-semibold shrink-0">
            {initials}
          </div>
          <div className="min-w-0">
            <h3 className="text-[16px] font-semibold text-foreground truncate">
              {userName ?? "User"}
            </h3>
            <p className="text-[13px] text-muted-foreground/70 mt-0.5 truncate">
              {userEmail ?? ""}
            </p>
            {role && (
              <Badge
                variant="outline"
                className={cn("mt-2 text-[10px] font-medium", roleCls[role] ?? "")}
              >
                {role}
              </Badge>
            )}
          </div>
        </div>
      </div>

      {/* Details grid */}
      <div className="bg-card border border-white/[0.06] rounded-lg p-6">
        <h4 className="text-[11px] font-semibold text-muted-foreground/50 uppercase tracking-wider mb-4">
          Account Details
        </h4>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-5">
          <div>
            <div className="text-[11px] text-muted-foreground/50 uppercase tracking-wider mb-1">
              Full Name
            </div>
            <div className="text-[14px] text-foreground">{userName ?? "Not set"}</div>
          </div>
          <div>
            <div className="text-[11px] text-muted-foreground/50 uppercase tracking-wider mb-1">
              Email
            </div>
            <div className="text-[14px] text-foreground font-mono">{userEmail ?? "Not set"}</div>
          </div>
          <div>
            <div className="text-[11px] text-muted-foreground/50 uppercase tracking-wider mb-1">
              Workspace Role
            </div>
            <div className="text-[14px] text-foreground">{role ?? "Not assigned"}</div>
          </div>
        </div>
      </div>
    </div>
  )
}
