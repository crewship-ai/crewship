"use client"

import type { InboxItem } from "@/hooks/use-inbox"

/**
 * The consequences of granting this credential, as facts.
 *
 * The card used to give the person deciding exactly one thing: the judge's
 * reason. That is a case FOR the verdict already reached — it argues, it does
 * not brief. Asked what he actually needed while ruling on escalations, the
 * operator said: is there a backup, would a narrower key do, and then leave me
 * to decide.
 *
 * # Why there is no advice here
 *
 * Every line is a server-computed query result and none of them is a
 * recommendation. That boundary is the whole point: the moment this says "this
 * would be safer", the reader anchors on the model and stops deciding — the
 * judgement moves back into the machine under a friendlier name. "No backup
 * recorded" is checkable. "You should probably deny this" is an opinion wearing
 * a fact's clothes.
 *
 * # Why this file computes nothing
 *
 * It renders what internal/keeper/evidence produced and derives nothing. A fact
 * invented in React is a fact nobody can test, and the next person to touch it
 * adds a heuristic and calls it advice.
 *
 * # The third state
 *
 * An absent field means the query failed and nobody knows; `exists: false`
 * means we looked and there is none. They are rendered differently on purpose —
 * showing "no backup" for a failed lookup would manufacture an argument against
 * approving out of a database outage, which is the mirror of the failure the
 * evidence package exists to prevent.
 */
export function EvidenceFacts({ item }: { item: InboxItem }) {
  const ev = item.evidence
  if (!ev) return null

  const rows: { label: string; value: string; alarming: boolean }[] = []

  if (ev.last_backup) {
    rows.push(
      ev.last_backup.exists
        ? {
            label: "Last backup",
            // The scope qualifier is not decoration. backup_catalog is scoped to
            // a workspace, never a table, and "6h ago" read as "this table can
            // be restored" is exactly the reassuring invention to avoid.
            value: `${ev.last_backup.age_hours}h ago (${ev.last_backup.scope}-wide, not this table)`,
            alarming: false,
          }
        : { label: "Last backup", value: "none recorded", alarming: true },
    )
  }

  if (ev.narrower_credential) {
    rows.push(
      ev.narrower_credential.exists
        ? {
            label: "Narrower credential",
            value: `${ev.narrower_credential.name} (L${ev.narrower_credential.security_level})`,
            alarming: false,
          }
        : { label: "Narrower credential", value: "none for this provider", alarming: false },
    )
  }

  if (rows.length === 0) return null

  return (
    <div className="rounded-md border border-border/60 p-2.5 space-y-1.5" data-testid="keeper-evidence">
      <span className="type-meta text-muted-foreground-soft">
        Checked against the database — not the agent&rsquo;s account
      </span>
      <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-[11px]">
        {rows.map((r) => (
          <div key={r.label} className="contents">
            <dt className="text-muted-foreground/70">{r.label}</dt>
            <dd className={r.alarming ? "text-warn" : "text-foreground/80"}>{r.value}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}
