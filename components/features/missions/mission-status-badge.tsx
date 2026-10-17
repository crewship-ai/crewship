"use client"

import {
  Clock,
  Lock,
  Loader2,
  CheckCircle2,
  XCircle,
  MinusCircle,
  ShieldAlert,
  FileEdit,
  Eye,
  Ban,
} from "lucide-react"
import { StatusIconBadge } from "@/components/ui/status-icon-badge"
import { CircleCheckIcon } from "@/components/ui/circle-check"
import { LoaderPinwheelIcon } from "@/components/ui/loader-pinwheel"
import type { MissionStatus, MissionTaskStatus } from "@/lib/types/mission"
import type { StatusConfigEntryWithIcon } from "@/lib/status-config"
import { statusTone } from "@/lib/status-tone"

const MISSION_STATUS_CONFIG: Record<MissionStatus, StatusConfigEntryWithIcon> = {
  PLANNING:    { label: "Planning",    tone: statusTone("PLANNING"), icon: FileEdit },
  IN_PROGRESS: { label: "In Progress", tone: statusTone("IN_PROGRESS"), icon: Loader2 },
  REVIEW:      { label: "Review",      tone: statusTone("REVIEW"), icon: Eye },
  COMPLETED:   { label: "Completed",   tone: statusTone("COMPLETED"), icon: CheckCircle2 },
  FAILED:      { label: "Failed",      tone: statusTone("FAILED"), icon: XCircle },
  CANCELLED:   { label: "Cancelled",   tone: statusTone("CANCELLED"), icon: Ban },
  BACKLOG:     { label: "Backlog",     tone: statusTone("BACKLOG"), icon: Clock },
  TODO:        { label: "Todo",        tone: statusTone("TODO"), icon: Clock },
  DONE:        { label: "Done",        tone: statusTone("DONE"), icon: CheckCircle2 },
  DUPLICATE:   { label: "Duplicate",   tone: statusTone("DUPLICATE"), icon: Ban },
}

const TASK_STATUS_CONFIG: Record<MissionTaskStatus, StatusConfigEntryWithIcon> = {
  PENDING:           { label: "Pending",           tone: statusTone("PENDING"), icon: Clock },
  BLOCKED:           { label: "Blocked",           tone: statusTone("BLOCKED"), icon: Lock },
  IN_PROGRESS:       { label: "Working",           tone: statusTone("IN_PROGRESS"), icon: Loader2 },
  COMPLETED:         { label: "Completed",         tone: statusTone("COMPLETED"), icon: CheckCircle2 },
  FAILED:            { label: "Failed",            tone: statusTone("FAILED"), icon: XCircle },
  SKIPPED:           { label: "Skipped",           tone: statusTone("SKIPPED"), icon: MinusCircle },
  AWAITING_APPROVAL: { label: "Awaiting Approval", tone: statusTone("AWAITING_APPROVAL"), icon: ShieldAlert },
}

function StatusIcon({ status, icon: Icon }: { status: string; icon: React.ComponentType<{ className?: string }> }) {
  if (status === "IN_PROGRESS") return <LoaderPinwheelIcon size={14} />
  if (status === "COMPLETED") return <CircleCheckIcon size={14} />
  return <Icon className="h-3 w-3" />
}

export function MissionStatusBadge({ status }: { status: MissionStatus }) {
  const config = MISSION_STATUS_CONFIG[status]
  return <StatusIconBadge entry={config} icon={<StatusIcon status={status} icon={config.icon} />} />
}

export function TaskStatusBadge({ status }: { status: MissionTaskStatus }) {
  const config = TASK_STATUS_CONFIG[status]
  return <StatusIconBadge entry={config} icon={<StatusIcon status={status} icon={config.icon} />} />
}
