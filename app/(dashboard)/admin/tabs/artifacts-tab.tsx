"use client"

import { useId, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Bot, FileText, Table2 } from "lucide-react"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { PageError, PageLoading } from "@/components/layout/page-state"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { StatusPill } from "@/components/ui/status-pill"
import { apiFetch, type ApiFetchInit } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"

const APPLICATIONS_PATH = "/api/v1/admin/instance/artifacts/applications"
const QUERY_KEY = ["instance-admin", "artifact-applications"] as const
const INSTALLABLE = new Set(["component_not_installed", "component_install_failed", "component_integrity"])
const DRAIN_CHOICES = [
  [0, "Immediately"],
  [300, "5 minutes"],
  [900, "15 minutes"],
  [1800, "30 minutes"],
  [3600, "1 hour"],
  [86400, "24 hours"],
] as const

type Subject = "wordcraft" | "gridcraft" | "agents"
type Desired = "enabled" | "disabled"

interface Application {
  id: Subject
  label: string
  desired: Desired
  locked: boolean
  source: "admin" | "default" | "server_config"
  state: "ready" | "draining" | "disabled" | "unavailable"
  reason?: string
  message?: string
  drain_until?: string
  documents?: number
  component?: {
    available: boolean
    reason?: string
    detail?: string
    version?: string
    runtime?: string
  }
}

interface ApplicationSettings {
  applications: Application[]
  agents: Application
  editor: string
}

interface Diagnostics {
  passed: boolean
  message?: string
  version?: string
}

type Update =
  | { id: Subject; desired: Desired; drain?: number }
  | { id: Subject; file?: File }

const ICON = { wordcraft: FileText, gridcraft: Table2, agents: Bot }
const STATUS = {
  ready: { tone: "success", label: "Ready" },
  draining: { tone: "warn", label: "Turning off" },
  disabled: { tone: "muted", label: "Off" },
  unavailable: { tone: "warn", label: "Unavailable" },
} as const

const isLocked = (application: Application) => application.locked || application.source === "server_config"
const isInstalling = (application: Application) => application.reason === "component_installing"

class ApplicationRequestError extends Error {
  constructor(readonly status: number, message: string) {
    super(message)
  }
}

const accessDenied = (error: unknown) =>
  error instanceof ApplicationRequestError && (error.status === 401 || error.status === 403)

async function request<T>(path: string, init?: ApiFetchInit): Promise<T> {
  const response = await apiFetch(path, init)
  if (!response.ok) {
    const body = await response.json().catch(() => null)
    throw new ApplicationRequestError(response.status, body?.error || body?.detail || `HTTP ${response.status}`)
  }
  return response.json()
}

async function updateApplication(action: Update): Promise<ApplicationSettings> {
  const path = `${APPLICATIONS_PATH}/${action.id}`
  if ("desired" in action) {
    return request(path, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ desired: action.desired, drain_seconds: action.drain }),
    })
  }
  return request(`${path}/install`, {
    method: "POST",
    ...(action.file && { headers: { "Content-Type": "application/octet-stream" }, body: action.file }),
  })
}

export function ArtifactsTab() {
  const client = useQueryClient()
  const selectId = useId()
  const [confirm, setConfirm] = useState<Application | null>(null)
  const [drain, setDrain] = useState(900)
  const [diagnostics, setDiagnostics] = useState<Partial<Record<Subject, Diagnostics>>>({})
  const update = useMutation({
    mutationFn: updateApplication,
    onSuccess: async (settings) => {
      await client.cancelQueries({ queryKey: QUERY_KEY })
      client.setQueryData(QUERY_KEY, settings)
    },
    onError: (error) => {
      toast.error(error.message)
      void client.invalidateQueries({ queryKey: QUERY_KEY })
    },
  })
  const query = useQuery({
    queryKey: QUERY_KEY,
    retry: false,
    gcTime: 0,
    queryFn: ({ signal }) => request<ApplicationSettings>(APPLICATIONS_PATH, { signal }),
    refetchInterval: ({ state: { data, error } }) => {
      if (accessDenied(error)) return false
      if (update.isPending || data?.applications.some(isInstalling)) return 2000
      if (data?.applications.some((app) => app.state === "draining")) return 15000
      return false
    },
  })
  const diagnose = useMutation({
    mutationFn: (id: Subject) => request<Diagnostics>(`${APPLICATIONS_PATH}/${id}/diagnostics`, { method: "POST" }),
    onSuccess: (result, id) => setDiagnostics((previous) => ({ ...previous, [id]: result })),
    onError: (error) => toast.error(error.message),
  })

  if (!query.data || accessDenied(query.error)) {
    if (query.isError) {
      return <PageError title="Couldn’t load artifact applications" detail={query.error.message} onRetry={() => void query.refetch()} />
    }
    return <PageLoading variant="cards" rows={3} />
  }

  const { applications, agents, editor } = query.data
  const current = confirm && [agents, ...applications].find(({ id }) => id === confirm.id)
  return (
    <div className="space-y-4">
      {query.isRefetchError && (
        <PageError title="Couldn’t refresh artifact applications" detail={query.error.message} onRetry={() => void query.refetch()} />
      )}
      {(editor === "failed" || editor === "disabled" || editor === "pending") && (
        <p className="rounded-card border border-border bg-card p-3 text-label text-muted-foreground">
          {editor === "pending" ? "Editor isolation check pending." : "Editor isolation check is blocked. Check the server’s editor proxy configuration."}
        </p>
      )}
      {([...applications, agents]).map((app) => (
        <ApplicationCard
          key={app.id}
          app={app}
          busy={update.isPending}
          pending={update.isPending && update.variables?.id === app.id ? update.variables : undefined}
          diagnostic={diagnostics[app.id]}
          diagnosticBusy={diagnose.isPending}
          diagnosing={diagnose.isPending && diagnose.variables === app.id}
          onToggle={() => {
            if (app.desired === "enabled") {
              setDrain(900)
              setConfirm(app)
            } else {
              update.mutate({ id: app.id, desired: "enabled" })
            }
          }}
          onInstall={(file) => update.mutate({ id: app.id, file })}
          onDiagnose={() => diagnose.mutate(app.id)}
        />
      ))}
      <ConfirmDialog
        open={Boolean(confirm)}
        onOpenChange={(open) => { if (!open) setConfirm(null) }}
        title={`Turn off ${confirm?.label ?? ""}?`}
        description={confirm?.id === "agents"
          ? "Agents will lose access to the Word and Sheet tools. People can still use the applications that are on."
          : "People will no longer be able to start creating or editing these documents."}
        consequences={[{ tone: "kept", text: "Existing documents will remain readable." }]}
        confirmLabel={`Turn off ${confirm?.label ?? ""}`}
        confirmDisabled={!current || isLocked(current) || update.isPending}
        onConfirm={async () => {
          if (!current || isLocked(current)) return
          await update.mutateAsync({ id: current.id, desired: "disabled", drain: current.id === "agents" ? undefined : drain })
        }}
      >
        {confirm?.id !== "agents" && (
          <label htmlFor={selectId} className="flex flex-col gap-1.5 text-label text-muted-foreground">
            Allow open editors to finish saving for
            <select
              id={selectId}
              value={drain}
              onChange={(event) => setDrain(Number(event.target.value))}
              className="min-h-9 rounded-md border border-input bg-background px-2 text-foreground"
            >
              {DRAIN_CHOICES.map(([seconds, label]) => <option key={seconds} value={seconds}>{label}</option>)}
            </select>
          </label>
        )}
      </ConfirmDialog>
    </div>
  )
}

interface CardProps {
  app: Application
  busy: boolean
  pending?: Update
  diagnostic?: Diagnostics
  diagnosing: boolean
  diagnosticBusy: boolean
  onToggle: () => void
  onInstall: (file?: File) => void
  onDiagnose: () => void
}

function ApplicationCard({ app, busy, pending, diagnostic, diagnosing, diagnosticBusy, onToggle, onInstall, onDiagnose }: CardProps) {
  const fileInput = useRef<HTMLInputElement>(null)
  const installing = isInstalling(app)
  const installable = INSTALLABLE.has(app.component?.reason ?? app.reason ?? "")
  const locked = isLocked(app)
  const status = installing ? { tone: "blue", label: "Installing" } as const : STATUS[app.state]
  const drainUntil = app.state === "draining" && app.drain_until ? new Date(app.drain_until) : null
  const toggle = app.desired === "enabled" ? "Turn off" : "Turn on"
  let progress = installing ? `Installing ${app.label}…` : undefined
  if (pending && "desired" in pending && !installing) {
    progress = pending.desired === "enabled" ? `Turning on ${app.label}…` : `Turning off ${app.label}…`
  } else if (pending && !("desired" in pending)) {
    progress = pending.file ? `Uploading and verifying ${app.label}…` : `Installing ${app.label}…`
  }
  return (
    <SettingsCard title={app.label} icon={ICON[app.id]} actions={<StatusPill {...status} live={installing} />}>
      <SettingsRow label="Access" description={locked ? "Managed by server configuration. This setting can only be changed on the server." : undefined}>
        <Button variant="outline" size="sm" disabled={busy || locked} onClick={onToggle}>
          {`${toggle} ${app.label}`}
        </Button>
      </SettingsRow>
      {(app.message || drainUntil || app.documents !== undefined || app.component?.version) && (
        <div className="space-y-1 border-t border-border px-4 py-3 text-label text-muted-foreground" aria-live="polite">
          {app.message && <p>{app.message}</p>}
          {drainUntil && <p>Open editors can save until <time dateTime={app.drain_until}>{drainUntil.toLocaleString()}</time>.</p>}
          {app.documents !== undefined && <p>{app.documents} {app.documents === 1 ? "document" : "documents"}</p>}
          {app.component?.version && <p>Installed version {app.component.version}{app.component.runtime && ` · ${app.component.runtime}`}</p>}
        </div>
      )}
      {app.component?.detail && <p className="break-words border-t border-border px-4 py-3 text-label text-muted-foreground">{app.component.detail}</p>}
      {progress && <p className="border-t border-border px-4 py-3 text-label text-muted-foreground" role="status">{progress}</p>}
      {!installing && (installable || app.component?.available) && (
        <div className="flex flex-wrap gap-2 border-t border-border px-4 py-3">
          {installable && (
            <>
              <Button size="sm" variant="outline" disabled={busy} onClick={() => onInstall()}>Install {app.label}</Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => fileInput.current?.click()}>Install {app.label} from file</Button>
              <input
                ref={fileInput}
                type="file"
                accept=".craftpkg"
                hidden
                onChange={(event) => {
                  const file = event.currentTarget.files?.[0]
                  event.currentTarget.value = ""
                  if (file) onInstall(file)
                }}
              />
            </>
          )}
          {app.component?.available && (
            <Button size="sm" variant="outline" disabled={diagnosticBusy} onClick={onDiagnose}>
              {diagnosing ? "Running diagnostics…" : `Test ${app.label}`}
            </Button>
          )}
        </div>
      )}
      {diagnostic && (
        <p role="status" className="border-t border-border px-4 py-3 text-label text-muted-foreground">
          {diagnostic.passed
            ? `Diagnostics passed${diagnostic.version ? ` · ${diagnostic.version}` : ""}`
            : diagnostic.message || "Diagnostics failed."}
        </p>
      )}
    </SettingsCard>
  )
}
