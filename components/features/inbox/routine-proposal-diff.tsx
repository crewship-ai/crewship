"use client"

// What actually changed, on the item a reviewer is being asked to
// decide.
//
// A routine proposal arrived carrying a slug, a risk reason and a
// pipeline id. To see what had changed you had to leave the inbox, find
// the routine, open its versions and compare two by eye — so in
// practice nobody did, and Approve became a button you press because it
// is the one that makes the notification go away.
//
// Everything needed already existed: routines keep immutable versions,
// and the API serves a unified diff of any two. The payload simply
// never carried the numbers.

import * as React from "react"
import { ArrowUpRight, GitCompare } from "lucide-react"
import Link from "next/link"

import { cn } from "@/lib/utils"
import { Appear, DetailCard } from "@/components/ui/detail"
import { apiFetch } from "@/lib/api-fetch"

interface Props {
  workspaceId: string
  slug: string
  fromVersion: number | null
  toVersion: number | null
}

interface DiffResponse {
  from_version: number
  to_version: number
  identical: boolean
  unified_diff: string
}

export function RoutineProposalDiff({ workspaceId, slug, fromVersion, toVersion }: Props) {
  const [diff, setDiff] = React.useState<DiffResponse | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const [loading, setLoading] = React.useState(false)

  React.useEffect(() => {
    // v1 has no predecessor. There is nothing to diff against, and
    // asking would 400 — the whole routine IS the change.
    if (!workspaceId || !slug || !fromVersion || !toVersion) return
    let cancelled = false
    setLoading(true)
    apiFetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}/diff?from=${fromVersion}&to=${toVersion}`,
    )
      .then(async (res) => {
        if (cancelled) return
        if (!res.ok) {
          setError(`Could not load the diff (${res.status})`)
          return
        }
        setDiff((await res.json()) as DiffResponse)
      })
      .catch(() => {
        if (!cancelled) setError("Could not load the diff")
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [workspaceId, slug, fromVersion, toVersion])

  const versionLabel =
    fromVersion && toVersion ? `v${fromVersion} → v${toVersion}` : toVersion ? `v${toVersion}` : ""

  return (
    <Appear order={2}>
      <DetailCard
        title="What changed"
        subtitle={versionLabel}
        icon={GitCompare}
        bare
        action={
          <Link
            href={`/routines?slug=${encodeURIComponent(slug)}`}
            className="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
          >
            Open routine
            <ArrowUpRight className="h-3 w-3" />
          </Link>
        }
      >
        <div className="px-4 py-3">
          {!fromVersion ? (
            <p className="text-[12px] text-muted-foreground">
              This is the routine&apos;s first version — the whole definition is the change. Open the
              routine to read it.
            </p>
          ) : loading ? (
            <p className="text-[12px] text-muted-foreground">Loading the diff…</p>
          ) : error ? (
            <p className="text-[12px] text-muted-foreground">{error}</p>
          ) : diff?.identical ? (
            <p className="text-[12px] text-muted-foreground">
              The definition is byte-identical to v{fromVersion}. Only the risk classification
              changed.
            </p>
          ) : diff ? (
            <DiffBody unified={diff.unified_diff} />
          ) : null}
        </div>
      </DetailCard>
    </Appear>
  )
}

/**
 * A unified diff, coloured by line.
 *
 * Deliberately not a syntax-highlighted side-by-side: the question a
 * reviewer has is "what is different", and additions and removals in
 * green and red answer it in one pass. Hunk headers are dimmed rather
 * than hidden — they are how you tell a change to step 2 from the same
 * change to step 9.
 */
function DiffBody({ unified }: { unified: string }) {
  const lines = React.useMemo(() => unified.split("\n"), [unified])
  if (unified.trim() === "") {
    return <p className="text-[12px] text-muted-foreground">No textual difference.</p>
  }
  return (
    <pre className="max-h-[420px] overflow-auto rounded-md border border-border/60 bg-background/40 p-2.5 font-mono text-[11px] leading-relaxed">
      {lines.map((line, i) => (
        <div
          key={i}
          className={cn(
            "whitespace-pre-wrap",
            line.startsWith("+") && !line.startsWith("+++") && "bg-success/10 text-success",
            line.startsWith("-") && !line.startsWith("---") && "bg-destructive/10 text-destructive",
            line.startsWith("@@") && "mt-1 text-muted-foreground-soft",
            (line.startsWith("+++") || line.startsWith("---")) && "text-muted-foreground-soft",
          )}
        >
          {line === "" ? " " : line}
        </div>
      ))}
    </pre>
  )
}
