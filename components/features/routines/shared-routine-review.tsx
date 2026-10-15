"use client"

import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { z } from "zod"

import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"

const receiptSchema = z.object({
  id: z.string().min(1), routine_id: z.string().min(1), draft_id: z.string().min(1),
  revision: z.number().int().positive(), base_revision: z.number().int().nonnegative(),
  state: z.enum(["pending", "published", "denied", "cancelled", "expired", "superseded"]),
  published_version: z.number().int().nonnegative(),
}).refine(value => (value.state === "published") === (value.published_version > 0), "Invalid publication receipt")

const labels = {
  pending: "Awaiting publication review", published: "Published", denied: "Publication declined",
  cancelled: "Review cancelled", expired: "Review expired", superseded: "Draft changed since this request",
}

interface Props {
  url: string
  draftKey: string[]
  snapshot: { draft_id: string; revision: number; base_revision: number }
  dirty: boolean
  saving: boolean
}

export function SharedRoutineReview({ url, draftKey, snapshot, dirty, saving }: Props) {
  const client = useQueryClient()
  const [error, setError] = useState("")
  const key = ["shared-routine-review", ...draftKey]
  const saved = Boolean(snapshot.draft_id && snapshot.revision > 0)
  async function check(response: Response) {
    if ([401, 403, 404].includes(response.status)) {
      client.setQueryData(draftKey, null)
      client.removeQueries({ queryKey: key })
      throw new Error("Draft access is no longer available.")
    }
    if (!response.ok) throw new Error(response.status === 409
      ? "This saved revision can no longer be submitted. Your edits are retained; save a new revision or ask your administrator to review it."
      : "Could not refresh publication review. Your edits are retained.")
    return receiptSchema.nullable().parse(await response.json())
  }
  const query = useQuery({
    queryKey: key, enabled: saved, retry: false, refetchOnWindowFocus: false,
    queryFn: async ({ signal }) => check(await apiFetch(`${url}/reviews`, { signal })),
    refetchInterval: query => query.state.data?.state === "pending" ? 10_000 : false,
  })
  const mutation = useMutation({
    retry: false,
    mutationFn: async () => {
      const receipt = await check(await apiFetch(`${url}/reviews`, {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(snapshot),
      }))
      if (!receipt || receipt.draft_id !== snapshot.draft_id || receipt.revision !== snapshot.revision || receipt.base_revision !== snapshot.base_revision) {
        throw new Error("The publication review did not match this saved revision.")
      }
      return receipt
    },
    onSuccess: receipt => { client.setQueryData(key, receipt); setError("") },
    onError: failure => setError(failure.message),
  })
  const receipt = query.data
  const same = receipt?.draft_id === snapshot.draft_id && receipt.revision === snapshot.revision && receipt.base_revision === snapshot.base_revision
  return <section className="space-y-2" aria-label="Draft publication review">
    {receipt && <p role="status">{labels[receipt.state]} · Draft revision {receipt.revision}{receipt.state === "published" ? ` · Published version ${receipt.published_version}` : ""}</p>}
    {query.isError && <p role="alert">{query.error.message}</p>}
    {error && <p role="alert">{error}</p>}
    {dirty && <p className="text-sm text-muted-foreground">Save your changes before requesting publication review.</p>}
    {same && receipt.state !== "pending" && <p className="text-sm text-muted-foreground">Save a new draft revision before requesting another review.</p>}
    <Button type="button" variant="outline" disabled={!saved || dirty || saving || mutation.isPending || query.isPending || Boolean(same)} onClick={() => mutation.mutate()}>
      {mutation.isPending ? "Requesting review…" : "Request publication review"}
    </Button>
  </section>
}
