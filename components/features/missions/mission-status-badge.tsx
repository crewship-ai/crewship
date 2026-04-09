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
import { Badge } from "@/components/ui/badge"
import { CircleCheckIcon } from "@/components/ui/circle-check"
import { LoaderPinwheelIcon } from "@/components/ui/loader-pinwheel"
import type { MissionStatus, MissionTaskStatus } from "@/lib/types/mission"
import { STATUS_STYLES, type StatusConfigEntryWithIcon } from "@/lib/status-config"

const MISSION_STATUS_CONFIG: Record<MissionStatus, StatusConfigEntryWithIcon> = {
  PLANNING:    { label: "Planning",    className: STATUS_STYLES.slate,   icon: FileEdit },
  IN_PROGRESS: { label: "In Progress", className: STATUS_STYLES.blue,    icon: Loader2 },
  REVIEW:      { label: "Review",      className: STATUS_STYLES.purple,  icon: Eye },
  COMPLETED:   { label: "Completed",   className: STATUS_STYLES.emerald, icon: CheckCircle2 },
  FAILED:      { label: "Failed",      className: STATUS_STYLES.red,     icon: XCircle },
  CANCELLED:   { label: "Cancelled",   className: STATUS_STYLES.gray,    icon: Ban },
  BACKLOG:     { label: "Backlog",     className: STATUS_STYLES.slate,   icon: Clock },
  TODO:        { label: "Todo",        className: STATUS_STYLES.slate,   icon: Clock },
  DONE:        { label: "Done",        className: STATUS_STYLES.emerald, icon: CheckCircle2 },
  DUPLICATE:   { label: "Duplicate",   className: STATUS_STYLES.gray,    icon: Ban },
}

const TASK_STATUS_CONFIG: Record<MissionTaskStatus, StatusConfigEntryWithIcon> = {
  PENDING:           { label: "Pending",           className: STATUS_STYLES.amber,   icon: Clock },
  BLOCKED:           { label: "Blocked",           className: STATUS_STYLES.orange,  icon: Lock },
  IN_PROGRESS:       { label: "Working",           className: STATUS_STYLES.blue,    icon: Loader2 },
  COMPLETED:         { label: "Completed",         className: STATUS_STYLES.emerald, icon: CheckCircle2 },
  FAILED:            { label: "Failed",            className: STATUS_STYLES.red,     icon: XCircle },
  SKIPPED:           { label: "Skipped",           className: STATUS_STYLES.gray,    icon: MinusCircle },
  AWAITING_APPROVAL: { label: "Awaiting Approval", className: STATUS_STYLES.violet,  icon: ShieldAlert },
}

function StatusIcon({ status, icon: Icon }: { status: string; icon: React.ComponentType<{ className?: string }> }) {
  if (status === "IN_PROGRESS") return <LoaderPinwheelIcon size={14} />
  if (status === "COMPLETED") return <CircleCheckIcon size={14} />
  return <Icon className="h-3 w-3" />
}

export function MissionStatusBadge({ status }: { status: MissionStatus }) {
  const config = MISSION_STATUS_CONFIG[status]
  return (
    <Badge variant="outline" className={`gap-1 border-0 ${config.className}`}>
      <StatusIcon status={status} icon={config.icon} />
      {config.label}
    </Badge>
  )
}

export function TaskStatusBadge({ status }: { status: MissionTaskStatus }) {
  const config = TASK_STATUS_CONFIG[status]
  return (
    <Badge variant="outline" className={`gap-1 border-0 ${config.className}`}>
      <StatusIcon status={status} icon={config.icon} />
      {config.label}
    </Badge>
  )
}
