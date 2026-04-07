"use client"

import { useState, useEffect } from "react"
import { LogOut, Copy, Check } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Separator } from "@/components/ui/separator"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"

const roleCls: Record<string, string> = {
  OWNER: "bg-amber-500/20 text-amber-400 border-amber-500/30",
  ADMIN: "bg-blue-500/20 text-blue-400 border-blue-500/30",
  MANAGER: "bg-teal-500/20 text-teal-400 border-teal-500/30",
  MEMBER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
  VIEWER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
}

function useTimeUntil(dateStr: string | null | undefined) {
  const [text, setText] = useState("")

  useEffect(() => {
    if (!dateStr) return
    function update() {
      const diff = new Date(dateStr!).getTime() - Date.now()
      if (diff <= 0) { setText("Expired"); return }
      const h = Math.floor(diff / 3600000)
      const m = Math.floor((diff % 3600000) / 60000)
      setText(h > 24 ? `${Math.floor(h / 24)}d ${h % 24}h` : h > 0 ? `${h}h ${m}m` : `${m}m`)
    }
    update()
    const id = setInterval(update, 60000)
    return () => clearInterval(id)
  }, [dateStr])

  return text
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

function FieldRow({ label, children, className }: {
  label: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn("flex flex-col sm:flex-row sm:items-center gap-1 sm:gap-0 py-2.5", className)}>
      <span className="text-[13px] text-muted-foreground/50 sm:w-[140px] shrink-0">{label}</span>
      <div className="flex-1 min-w-0 flex items-center gap-2">{children}</div>
    </div>
  )
}

function CopyableValue({ value, mono }: { value: string; mono?: boolean }) {
  const [copied, setCopied] = useState(false)

  function handleCopy() {
    navigator.clipboard.writeText(value)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            onClick={handleCopy}
            className={cn(
              "text-[13px] text-foreground truncate text-left hover:text-foreground/80 transition-colors",
              mono && "font-mono",
            )}
          >
            {value}
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" className="text-xs">
          {copied ? (
            <span className="flex items-center gap-1"><Check className="h-3 w-3 text-emerald-400" /> Copied</span>
          ) : (
            <span className="flex items-center gap-1"><Copy className="h-3 w-3" /> Click to copy</span>
          )}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

function P2Chip() {
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Badge
            variant="outline"
            className="text-[8px] px-1 py-0 h-4 border-white/[0.06] text-muted-foreground/25 cursor-default shrink-0"
          >
            EDIT
          </Badge>
        </TooltipTrigger>
        <TooltipContent side="left" className="text-xs">Coming in Phase 2</TooltipContent>
      </Tooltip>
    </TooltipProvider>
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

  const expiresIn = useTimeUntil(sessionExpires)

  return (
    <Card className="border-white/[0.06]">
      <CardContent className="p-0">
        {/* Header */}
        <div className="p-5 sm:p-6">
          <div className="flex items-start sm:items-center gap-4 flex-col sm:flex-row">
            <div className="w-14 h-14 rounded-full bg-primary/80 ring-2 ring-white/[0.08] flex items-center justify-center text-primary-foreground text-[15px] font-semibold shrink-0">
              {initials}
            </div>
            <div className="min-w-0 flex-1">
              <h3 className="text-[16px] font-semibold text-foreground truncate">
                {userName ?? "User"}
              </h3>
              <p className="text-[13px] text-muted-foreground/40 mt-0.5 truncate font-mono">
                {userEmail ?? ""}
              </p>
              <div className="flex flex-wrap items-center gap-2 mt-2">
                {role && (
                  <Badge variant="outline" className={cn("text-[10px] font-medium", roleCls[role] ?? "")}>
                    {role}
                  </Badge>
                )}
                {workspaceName && (
                  <span className="text-[11px] text-muted-foreground/25">{workspaceName}</span>
                )}
              </div>
            </div>
          </div>
        </div>

        <Separator className="bg-white/[0.06]" />

        {/* Account */}
        <div className="px-5 sm:px-6 py-3">
          <div className="text-[10px] font-semibold text-muted-foreground/25 uppercase tracking-wider mb-0.5">
            Account
          </div>
          <FieldRow label="Full Name">
            <span className="text-[13px] text-foreground truncate">{userName ?? "Not set"}</span>
            <div className="ml-auto"><P2Chip /></div>
          </FieldRow>
          <FieldRow label="Email">
            {userEmail ? <CopyableValue value={userEmail} mono /> : <span className="text-[13px] text-foreground">Not set</span>}
            <div className="ml-auto"><P2Chip /></div>
          </FieldRow>
          <FieldRow label="Password">
            <span className="text-[13px] text-muted-foreground/40 tracking-wider">••••••••••</span>
            <div className="ml-auto"><P2Chip /></div>
          </FieldRow>
        </div>

        <Separator className="bg-white/[0.06]" />

        {/* Workspace */}
        <div className="px-5 sm:px-6 py-3">
          <div className="text-[10px] font-semibold text-muted-foreground/25 uppercase tracking-wider mb-0.5">
            Workspace
          </div>
          <FieldRow label="Role">
            {role ? (
              <Badge variant="outline" className={cn("text-[10px] font-medium", roleCls[role] ?? "")}>
                {role}
              </Badge>
            ) : (
              <span className="text-[13px] text-muted-foreground/40">Not assigned</span>
            )}
          </FieldRow>
          {workspaceName && (
            <FieldRow label="Organization">
              <span className="text-[13px] text-foreground">{workspaceName}</span>
            </FieldRow>
          )}
          {joinedAt && (
            <FieldRow label="Joined">
              <span className="text-[13px] text-muted-foreground/60">
                {new Date(joinedAt).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })}
              </span>
            </FieldRow>
          )}
        </div>

        <Separator className="bg-white/[0.06]" />

        {/* Session */}
        <div className="px-5 sm:px-6 py-3">
          <div className="text-[10px] font-semibold text-muted-foreground/25 uppercase tracking-wider mb-0.5">
            Session
          </div>
          <FieldRow label="Status">
            <span className="flex items-center gap-1.5 text-[13px] text-foreground">
              <span className="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse" />
              Active
            </span>
          </FieldRow>
          {expiresIn && (
            <FieldRow label="Expires">
              <span className="text-[13px] text-muted-foreground/40">{expiresIn}</span>
            </FieldRow>
          )}
          <div className="pt-2 pb-1">
            <Button
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-[12px] text-red-400/60 hover:text-red-400 hover:bg-red-500/10 gap-1.5"
              onClick={onSignOut}
            >
              <LogOut className="h-3 w-3" />
              Sign out
            </Button>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
