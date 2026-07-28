"use client"

/**
 * Tool readiness for the credentials list (PRD-CREDENTIALS-V2-2026 §2.3,
 * blocker #3).
 *
 * "Has a valid PAT" and "has `gh` in the container" are two different facts,
 * and the vault only ever knew the first. GET /crews/{crewId}/credential-readiness
 * answers the second, per crew, and this hook fans it out over the workspace's
 * crews so the list can show it per credential.
 *
 * Two deliberate choices:
 *
 *  · A crew whose readiness call fails contributes nothing rather than
 *    failing the whole column. "We could not check this crew" must never
 *    render as "nothing is missing" — a green tick over a broken container is
 *    the failure this endpoint exists to remove.
 *  · Every response is checked against the workspace that asked for it before
 *    it is applied. Switching workspaces while a request is in flight is the
 *    normal case on a multi-workspace instance, and a late response from the
 *    previous one would otherwise repaint the new one's rows.
 */

import * as React from "react"
import { apiFetch } from "@/lib/api-fetch"

/** How many crews we will interrogate. The API is one call per crew and the
 *  instance runs a 120/min limiter, so a very large workspace reports on its
 *  first N crews rather than spending the user's budget on a sidebar. */
const MAX_CREWS = 24

export interface CredentialToolGap {
  crewId: string
  crewName: string
  /** The CLI binary the credential is meant to be read by, e.g. "gh". */
  tool: string
  /** Devcontainer feature ref that installs it, and its short id. */
  feature: string
  featureId: string
}

export interface CredentialReadiness {
  /** crew id → display name, for the sidebar's Scope facet. */
  crewNames: Record<string, string>
  /** Credential id → every crew that can use it but lacks its CLI. */
  gapsByCredential: Map<string, CredentialToolGap[]>
  /** The same information as a set, for the "Missing tool" filter. */
  missingToolIds: Set<string>
  /**
   * How many crews actually answered. Zero means "we could not check", which
   * the list must render differently from "checked, nothing missing" — a
   * green tick we did not earn is the exact failure this endpoint removes.
   */
  crewsChecked: number
  loading: boolean
}

const EMPTY: CredentialReadiness = {
  crewNames: {},
  gapsByCredential: new Map(),
  missingToolIds: new Set(),
  crewsChecked: 0,
  loading: false,
}

interface CrewRow {
  id?: string
  name?: string
}

interface ReadinessBody {
  crew_id?: string
  crew_slug?: string
  gaps?: {
    credential_id?: string
    tool?: string
    feature?: string
    feature_id?: string
  }[]
}

export function useCredentialReadiness(workspaceId: string | null): CredentialReadiness {
  const [state, setState] = React.useState<CredentialReadiness>({ ...EMPTY, loading: Boolean(workspaceId) })

  React.useEffect(() => {
    if (!workspaceId) {
      setState({ ...EMPTY, crewNames: {}, gapsByCredential: new Map(), missingToolIds: new Set() })
      return
    }
    let cancelled = false
    setState((prev) => ({ ...prev, loading: true }))

    void (async () => {
      let crews: CrewRow[] = []
      try {
        const res = await apiFetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`)
        if (res.ok) {
          const body = await res.json()
          if (Array.isArray(body)) crews = body as CrewRow[]
        }
      } catch {
        // Offline or 5xx: no crew list, so no readiness. Reported as "no
        // gaps known", never as "no gaps exist" — the column renders a
        // neutral state rather than a green one (see the page).
      }
      if (cancelled) return

      const named = crews.filter((c): c is { id: string; name?: string } => typeof c?.id === "string")
      const crewNames: Record<string, string> = {}
      for (const c of named) crewNames[c.id] = c.name ?? c.id

      const gapsByCredential = new Map<string, CredentialToolGap[]>()
      const missingToolIds = new Set<string>()

      const bodies = await Promise.all(
        named.slice(0, MAX_CREWS).map(async (crew) => {
          try {
            const res = await apiFetch(
              `/api/v1/crews/${encodeURIComponent(crew.id)}/credential-readiness` +
                `?workspace_id=${encodeURIComponent(workspaceId)}`,
            )
            if (!res.ok) return null
            return { crew, body: (await res.json()) as ReadinessBody }
          } catch {
            return null
          }
        }),
      )
      if (cancelled) return

      let crewsChecked = 0
      for (const entry of bodies) {
        if (!entry) continue
        crewsChecked++
        const gaps = Array.isArray(entry.body?.gaps) ? entry.body.gaps : []
        for (const gap of gaps) {
          const credentialId = gap?.credential_id
          if (!credentialId) continue
          const list = gapsByCredential.get(credentialId) ?? []
          list.push({
            crewId: entry.crew.id,
            crewName: crewNames[entry.crew.id] ?? entry.crew.id,
            tool: gap.tool ?? "",
            feature: gap.feature ?? "",
            featureId: gap.feature_id ?? "",
          })
          gapsByCredential.set(credentialId, list)
          missingToolIds.add(credentialId)
        }
      }

      setState({ crewNames, gapsByCredential, missingToolIds, crewsChecked, loading: false })
    })()

    return () => {
      cancelled = true
    }
  }, [workspaceId])

  return state
}
