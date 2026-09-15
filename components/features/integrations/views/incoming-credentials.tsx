"use client"
import * as React from "react"
import { Copy, Plus } from "lucide-react"
import { toast } from "sonner"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import { usePage } from "@/hooks/use-pages"
import { usePageWebhookCreate } from "@/hooks/use-page-sharing"
import {
  INCOMING_KINDS,
  type IncomingKind,
  type IncomingTarget,
} from "../incoming-model"
import { incomingJSON, type IncomingData } from "../use-incoming-endpoints"
import { IncomingError } from "./incoming-shared"

export interface Reveal {
  url?: string
  secret?: string
  target: IncomingTarget
}
export function absolute(path: string) {
  return new URL(path, window.location.origin).toString()
}
export async function copy(value: string) {
  try {
    await navigator.clipboard.writeText(value)
    toast.success("Copied")
  } catch {
    toast.error("Could not copy; select and save the value manually.")
  }
}
function RevealBody({
  reveal,
  onClose,
}: {
  reveal: Reveal
  onClose: () => void
}) {
  return (
    <div className="space-y-4">
      <h3 className="text-sm font-medium">Save your endpoint credentials</h3>
      <p className="text-xs text-muted-foreground">
        Secret values are shown once. Save them in your sender before closing.
      </p>
      {reveal.url && (
        <CreateSurfaceField label="Receiving URL">
          <code
            data-testid="incoming-receiving-url"
            className="break-all rounded border p-3 text-xs"
          >
            {reveal.url}
          </code>
          <Button variant="soft" onClick={() => copy(reveal.url!)}>
            <Copy className="size-3.5" />
            Copy receiving URL
          </Button>
        </CreateSurfaceField>
      )}
      {reveal.secret && (
        <CreateSurfaceField label="Signing secret">
          <code className="break-all rounded border p-3 text-xs">
            {reveal.secret}
          </code>
          <Button variant="soft" onClick={() => copy(reveal.secret!)}>
            <Copy className="size-3.5" />
            Copy signing secret
          </Button>
        </CreateSurfaceField>
      )}
      {!reveal.url && (
        <p className="text-xs text-muted-foreground">
          Your receiving URL has not changed. Rotating the signing secret cannot
          recover a lost URL.
        </p>
      )}
      <Button onClick={onClose}>Done</Button>
    </div>
  )
}
export function RevealDialog({
  reveal,
  onClose,
}: {
  reveal: Reveal
  onClose: () => void
}) {
  return (
    <CreateSurface
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      size="md"
    >
      <CreateSurfaceHeader
        concept="integrations"
        context="Integrations"
        title="Endpoint credentials"
        onClose={onClose}
      />
      <CreateSurfaceBody>
        <RevealBody reveal={reveal} onClose={onClose} />
      </CreateSurfaceBody>
    </CreateSurface>
  )
}

export function IncomingCreateDialog({
  workspaceId,
  data,
  initialTarget,
  onClose,
  onCreated,
}: {
  workspaceId: string
  data: IncomingData
  initialTarget?: IncomingTarget
  onClose: () => void
  onCreated: (t: IncomingTarget) => void
}) {
  const [kind, setKind] = React.useState<IncomingKind>(
    initialTarget?.kind ?? "routine",
  )
  const [targetId, setTargetId] = React.useState(initialTarget?.id ?? "")
  const [name, setName] = React.useState("")
  const [profile, setProfile] = React.useState<"crewship" | "github">(
    "crewship",
  )
  const [panel, setPanel] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const [reveal, setReveal] = React.useState<Reveal | null>(null)
  const target = data.targets.find((t) => t.kind === kind && t.id === targetId)
  const page = usePage(
    kind === "page" ? workspaceId : null,
    kind === "page" ? (target?.slug ?? null) : null,
  )
  const panelIds = (page.page?.panels ?? [])
    .filter(
      (p) =>
        p.producer?.startsWith("webhook/") || p.producer?.startsWith("script/"),
    )
    .map((p) => p.spec.id)
  const pageCreate = usePageWebhookCreate(workspaceId, target?.slug ?? "", {
    onOk: (w) => {
      if (target) setReveal({ url: w.url, target })
    },
    onRefused: setError,
  })
  const ready =
    !!target &&
    (kind !== "page" || panelIds.includes(panel)) &&
    !(kind === "agent" && (!target.crew_id || target.webhook_secret_set))
  const finish = () => {
    if (reveal) onCreated(reveal.target)
    onClose()
  }
  const submit = async () => {
    if (!ready || busy || pageCreate.isPending) return
    setError(null)
    setBusy(true)
    try {
      if (kind === "page") {
        await pageCreate.mutateAsync({ panel, name: name.trim() || undefined })
        return
      }
      if (kind === "agent") {
        const r = await incomingJSON<{ webhook_secret: string }>(
          `/api/v1/agents/${target.id}/webhook-secret/rotate?workspace_id=${encodeURIComponent(workspaceId)}`,
          { method: "POST" },
        )
        setReveal({
          target,
          secret: r.webhook_secret,
          url: absolute(
            `/api/v1/webhooks/${target.crew_id}/${target.id}/trigger`,
          ),
        })
        data.refresh()
      } else {
        const r = await data.hooks.create({
          name: name.trim() || `${target.name} webhook`,
          target_pipeline_id: target.id,
          ingress_profile: profile,
          enabled: true,
        })
        if (!r) throw new Error("No endpoint was returned")
        setReveal({
          target,
          secret: r.signing_secret,
          url: absolute(
            `/api/v1/webhooks/${r.token}${profile === "github" ? "/github-pull-request" : ""}`,
          ),
        })
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <CreateSurface
      open
      onOpenChange={(open) => {
        if (!open) finish()
      }}
      size="md"
      dirty={!reveal && !!name}
      discardLabel="this endpoint"
      onSubmit={() => void submit()}
    >
      <CreateSurfaceHeader
        concept="integrations"
        context="Integrations"
        title="Add incoming webhook"
        description="Choose where an external HTTP event should arrive."
        onClose={finish}
      />
      <CreateSurfaceBody className="space-y-4">
        {reveal ? (
          <RevealBody reveal={reveal} onClose={finish} />
        ) : (
          <>
            <CreateSurfaceField label="Target kind">
              <Select
                value={kind}
                onValueChange={(v) => {
                  setKind(v as IncomingKind)
                  setTargetId("")
                  setPanel("")
                }}
              >
                <SelectTrigger aria-label="Target kind">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {INCOMING_KINDS.map((k) => (
                    <SelectItem key={k.key} value={k.key}>
                      {k.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </CreateSurfaceField>
            <CreateSurfaceField
              label="Target"
              hint={INCOMING_KINDS.find((k) => k.key === kind)?.hint}
            >
              <Select
                value={targetId}
                onValueChange={(v) => {
                  setTargetId(v)
                  setPanel("")
                }}
              >
                <SelectTrigger aria-label="Target">
                  <SelectValue placeholder="Choose a target" />
                </SelectTrigger>
                <SelectContent>
                  {data.targets
                    .filter((t) => t.kind === kind)
                    .map((t) => (
                      <SelectItem key={t.id} value={t.id}>
                        {t.name}
                      </SelectItem>
                    ))}
                </SelectContent>
              </Select>
            </CreateSurfaceField>
            {kind !== "agent" && (
              <CreateSurfaceField label="Endpoint name" htmlFor="incoming-name">
                <Input
                  id="incoming-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="e.g. GitHub PR review"
                />
              </CreateSurfaceField>
            )}
            {kind === "routine" && (
              <CreateSurfaceField
                label="Sender"
                hint="GitHub: use JSON, Pull requests events and the signing secret shown after creation."
              >
                <Select
                  value={profile}
                  onValueChange={(v) => setProfile(v as "crewship" | "github")}
                >
                  <SelectTrigger aria-label="Sender">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="crewship">Crewship signature</SelectItem>
                    <SelectItem value="github">GitHub pull requests</SelectItem>
                  </SelectContent>
                </Select>
              </CreateSurfaceField>
            )}
            {kind === "page" && (
              <CreateSurfaceField
                label="Panel"
                hint="Only panels with a webhook or script producer accept these writes."
              >
                {page.loading ? (
                  <Skeleton className="h-9" />
                ) : (
                  <Select value={panel} onValueChange={setPanel}>
                    <SelectTrigger aria-label="Panel">
                      <SelectValue placeholder="Choose a panel" />
                    </SelectTrigger>
                    <SelectContent>
                      {panelIds.map((id) => (
                        <SelectItem key={id} value={id}>
                          {id}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
                {page.error && <IncomingError message={page.error} />}
              </CreateSurfaceField>
            )}
            {kind === "agent" && target?.webhook_secret_set && (
              <p className="text-xs text-muted-foreground">
                This agent already has an endpoint. Open its detail to rotate
                the signing secret.
              </p>
            )}
            {kind === "agent" && target && !target.crew_id && (
              <p className="text-xs text-muted-foreground">
                Assign this agent to a crew before adding an endpoint.
              </p>
            )}
            {data.error && (
              <IncomingError message={data.error} onRetry={data.refresh} />
            )}
          </>
        )}
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={error} onDismiss={() => setError(null)} />
      {!reveal && (
        <CreateSurfaceFooter
          onCancel={onClose}
          guardCancel
          primaryLabel="Create endpoint"
          primaryIcon={Plus}
          primaryDisabled={!ready}
          busy={busy || pageCreate.isPending}
          onPrimary={() => void submit()}
        />
      )}
    </CreateSurface>
  )
}
