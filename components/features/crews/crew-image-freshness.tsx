"use client"

// #1845 — the surface an operator lands on after the "crew image is behind"
// notification.
//
// Built from the canonical detail kit (DetailCard / Pill / TickRow) rather
// than hand-rolled chrome: those components exist because hand-rolled variants
// were what they replaced, and a card that looks almost like the ones around it
// is worse than one that looks the same.

import { useCallback, useEffect, useState } from "react"
import { RefreshCw, Layers } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { DetailCard, Pill, TickRow } from "@/components/ui/detail"
import { apiFetch } from "@/lib/api-fetch"

interface CrewImageStatus {
  image: string
  container_id: string
  running: boolean
  running_digest: string
  resolved_digest: string
  behind: boolean
  reason: string
}

interface CrewImageFreshnessProps {
  crewId: string
  workspaceId: string
  /** Refreshing pulls and force-removes a running container — a mutation. */
  canEdit: boolean
}

// Verdict is a THREE-way answer, not a boolean, and that is the whole design
// of this card. The backend distinguishes "compared and current" from "could
// not compare" precisely so a freshness check never reports a green tick for a
// check that did not run; collapsing the two here would throw that away at the
// last step.
type Verdict = "loading" | "behind" | "current" | "unknown" | "unavailable"

function verdictOf(status: CrewImageStatus | null, unavailable: boolean): Verdict {
  if (unavailable) return "unavailable"
  if (!status) return "loading"
  if (status.behind) return "behind"
  if (status.reason) return "unknown"
  return "current"
}

const VERDICT_LABEL: Record<Verdict, string> = {
  loading: "Checking…",
  behind: "Behind",
  current: "Current",
  unknown: "Unknown",
  unavailable: "Unavailable",
}

const VERDICT_TONE: Record<Verdict, "default" | "success" | "warn"> = {
  loading: "default",
  behind: "warn",
  current: "success",
  unknown: "default",
  unavailable: "default",
}

/** shortDigest renders "sha256:abcdef012345" — enough to compare two by eye. */
function shortDigest(digest: string): string {
  const hex = digest.startsWith("sha256:") ? digest.slice(7) : digest
  return `sha256:${hex.slice(0, 12)}`
}

export function CrewImageFreshness({ crewId, workspaceId, canEdit }: CrewImageFreshnessProps) {
  const [status, setStatus] = useState<CrewImageStatus | null>(null)
  const [unavailable, setUnavailable] = useState(false)
  const [refreshing, setRefreshing] = useState(false)

  const load = useCallback(async () => {
    try {
      const res = await apiFetch(`/api/v1/crews/${crewId}/image-status?workspace_id=${workspaceId}`)
      if (!res.ok) {
        // 503 is the honest "nothing here can answer" — no container provider,
        // or one that cannot report image digests. Anything else is equally
        // not an answer, and both must render as such rather than as "current".
        setUnavailable(true)
        setStatus(null)
        return
      }
      setUnavailable(false)
      setStatus((await res.json()) as CrewImageStatus)
    } catch {
      setUnavailable(true)
      setStatus(null)
    }
  }, [crewId, workspaceId])

  useEffect(() => {
    void load()
  }, [load])

  const handleRefresh = useCallback(async () => {
    setRefreshing(true)
    try {
      const res = await apiFetch(`/api/v1/crews/${crewId}/refresh-image?workspace_id=${workspaceId}`, { method: "POST" })
      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed" }))
        // Never a success toast on a failed pull: the usual cause is a
        // throttling registry, and an operator told "refreshed" stops looking
        // while still on the old image.
        toast.error(data.error || `HTTP ${res.status}`)
        return
      }
      const body = (await res.json()) as { container_removed?: boolean }
      toast.success(body.container_removed
        ? "Image refreshed. The container was dropped — agents recreate it from the new image on their next run."
        : "Image refreshed. No container was running, so the next start uses it directly.")
      await load()
    } catch {
      toast.error("Network error")
    } finally {
      setRefreshing(false)
    }
  }, [crewId, workspaceId, load])

  const verdict = verdictOf(status, unavailable)

  return (
    <DetailCard
      title="Image freshness"
      icon={Layers}
      tone={verdict === "behind" ? "warn" : "default"}
      data-testid="crew-image-freshness"
      action={
        canEdit && verdict === "behind" ? (
          <Button
            size="xs"
            variant="outline"
            onClick={handleRefresh}
            disabled={refreshing}
            data-testid="crew-image-refresh"
          >
            {refreshing ? <Spinner className="mr-1.5 h-3 w-3" /> : <RefreshCw className="mr-1.5 h-3 w-3" />}
            Refresh image
          </Button>
        ) : undefined
      }
    >
      <div className="space-y-2">
        <div className="flex items-center gap-2">
          <Pill tone={VERDICT_TONE[verdict]} data-testid="crew-image-freshness-verdict">
            {VERDICT_LABEL[verdict]}
          </Pill>
          {status?.image ? (
            <span className="type-meta truncate font-mono text-muted-foreground-soft">{status.image}</span>
          ) : null}
        </div>

        {verdict === "behind" ? (
          <p className="type-meta text-muted-foreground">
            This crew&rsquo;s container was created from an older build than its tag now points at.
            Nothing is broken — it is simply not the current image.
          </p>
        ) : null}

        {/* The reason a check could not be made is part of the answer, not a
            debug detail. Without it "not behind" is indistinguishable from
            "current", which is the false assurance the whole feature avoids. */}
        {verdict === "unknown" && status ? (
          <p className="type-meta text-muted-foreground" data-testid="crew-image-freshness-reason">
            Could not compare: {status.reason}.
          </p>
        ) : null}

        {verdict === "unavailable" ? (
          <p className="type-meta text-muted-foreground" data-testid="crew-image-freshness-reason">
            No container provider on this instance can report image digests, so freshness is unknown.
          </p>
        ) : null}

        {status && (status.running_digest || status.resolved_digest) ? (
          <div className="border-t border-hairline pt-2">
            {status.running_digest ? (
              <TickRow
                label="Running"
                meta={shortDigest(status.running_digest)}
                status={status.behind ? "pending" : "ok"}
              />
            ) : null}
            {status.resolved_digest ? (
              <TickRow
                label="Tag resolves to"
                meta={shortDigest(status.resolved_digest)}
                status="ok"
              />
            ) : null}
          </div>
        ) : null}
      </div>
    </DetailCard>
  )
}
