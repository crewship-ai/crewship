"use client"

import { useCallback, useEffect, useRef, useState } from "react"

// PipelineWebhook mirrors the wire shape returned by /pipeline-webhooks.
// SigningSecret only surfaces on the create response (mirroring how
// Stripe / GitHub reveal secrets once); subsequent fetches return
// SigningSecretSet boolean only.
export interface PipelineWebhook {
  id: string
  workspace_id: string
  name: string
  target_pipeline_id: string
  target_pipeline_slug?: string
  target_pipeline_version?: number
  token: string
  signing_secret_set: boolean
  signing_secret?: string // only present on create response
  inputs_template: Record<string, unknown>
  enabled: boolean
  rate_limit_per_min: number
  last_fired_at?: string
  last_status?: string
  last_run_id?: string
  fire_count: number
  created_at: string
  updated_at: string
}

export interface WebhookSaveBody {
  name: string
  target_pipeline_slug?: string
  target_pipeline_id?: string
  target_pipeline_version?: number
  signing_secret?: string
  inputs_template?: Record<string, unknown>
  enabled?: boolean
  rate_limit_per_min?: number
}

export function usePipelineWebhooks(workspaceId: string | null | undefined) {
  const [webhooks, setWebhooks] = useState<PipelineWebhook[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setWebhooks([])
      return
    }
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipeline-webhooks`, {
        signal: ctrl.signal,
      })
      if (ctrl.signal.aborted) return
      if (!res.ok) {
        if (res.status === 503) {
          // Backend not wired (test server / no DB).
          setWebhooks([])
          setLoading(false)
          return
        }
        setError(`pipeline webhooks: ${res.status}`)
        setLoading(false)
        return
      }
      const data: PipelineWebhook[] = await res.json()
      if (ctrl.signal.aborted) return
      setWebhooks(Array.isArray(data) ? data : [])
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    refresh()
    return () => abortRef.current?.abort()
  }, [refresh])

  const create = useCallback(
    async (body: WebhookSaveBody): Promise<PipelineWebhook | null> => {
      if (!workspaceId) return null
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipeline-webhooks`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const t = await res.text()
        throw new Error(`create webhook: ${res.status} ${t}`)
      }
      const out: PipelineWebhook = await res.json()
      await refresh()
      return out
    },
    [workspaceId, refresh],
  )

  const remove = useCallback(
    async (id: string): Promise<void> => {
      if (!workspaceId) return
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipeline-webhooks/${id}`, {
        method: "DELETE",
      })
      if (!res.ok && res.status !== 404) {
        throw new Error(`delete webhook: ${res.status}`)
      }
      await refresh()
    },
    [workspaceId, refresh],
  )

  return { webhooks, loading, error, refresh, create, remove }
}
