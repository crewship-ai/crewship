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

const MISSION_STATUS_CONFIG: Record<
  MissionStatus,
  { label: string; className: string; icon: React.ComponentType<{ className?: string }> }
> = {
  PLANNING: {
    label: "Planning",
    className: "bg-slate-100 text-slate-800 dark:bg-slate-900/40 dark:text-slate-300",
    icon: FileEdit,
  },
  IN_PROGRESS: {
    label: "In Progress",
    className: "bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300",
    icon: Loader2,
  },
  REVIEW: {
    label: "Review",
    className: "bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300",
    icon: Eye,
  },
  COMPLETED: {
    label: "Completed",
    className: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300",
    icon: CheckCircle2,
  },
  FAILED: {
    label: "Failed",
    className: "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300",
    icon: XCircle,
  },
  CANCELLED: {
    label: "Cancelled",
    className: "bg-gray-100 text-gray-800 dark:bg-gray-900/40 dark:text-gray-300",
    icon: Ban,
  },
  BACKLOG: {
    label: "Backlog",
    className: "bg-slate-100 text-slate-800 dark:bg-slate-900/40 dark:text-slate-300",
    icon: Clock,
  },
  TODO: {
    label: "Todo",
    className: "bg-slate-100 text-slate-800 dark:bg-slate-900/40 dark:text-slate-300",
    icon: Clock,
  },
  DONE: {
    label: "Done",
    className: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300",
    icon: CheckCircle2,
  },
  DUPLICATE: {
    label: "Duplicate",
    className: "bg-gray-100 text-gray-800 dark:bg-gray-900/40 dark:text-gray-300",
    icon: Ban,
  },
}

const TASK_STATUS_CONFIG: Record<
  MissionTaskStatus,
  { label: string; className: string; icon: React.ComponentType<{ className?: string }> }
> = {
  PENDING: {
    label: "Pending",
    className: "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300",
    icon: Clock,
  },
  BLOCKED: {
    label: "Blocked",
    className: "bg-orange-100 text-orange-800 dark:bg-orange-900/40 dark:text-orange-300",
    icon: Lock,
  },
  IN_PROGRESS: {
    label: "Working",
    className: "bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300",
    icon: Loader2,
  },
  COMPLETED: {
    label: "Completed",
    className: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300",
    icon: CheckCircle2,
  },
  FAILED: {
    label: "Failed",
    className: "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300",
    icon: XCircle,
  },
  SKIPPED: {
    label: "Skipped",
    className: "bg-gray-100 text-gray-800 dark:bg-gray-900/40 dark:text-gray-300",
    icon: MinusCircle,
  },
  AWAITING_APPROVAL: {
    label: "Awaiting Approval",
    className: "bg-violet-100 text-violet-800 dark:bg-violet-900/40 dark:text-violet-300",
    icon: ShieldAlert,
  },
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
