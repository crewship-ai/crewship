"use client"

import { useQuery } from "@tanstack/react-query"
import { Check, ChevronDown, Sparkles } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { apiFetch } from "@/lib/api-fetch"

import { useChatRunPreflight } from "../run-preflight"

interface CatalogueModel { id: string; display_name?: string; provider?: string }
interface Catalogue { provider: string; source: string; models: CatalogueModel[] }

// The chat's model is the one run-preflight reports for the configured agent.
// GET /api/v1/models lists what a provider can serve (live when a credential
// allows it, otherwise curated). It is neither proof that this user may use a
// model nor of what a run will use, so it is shown as reference only and
// choosing from it is not offered until the server can state effective
// capabilities.
export function ModelPicker({ chatId, workspaceId, userId }: {
  chatId: string; workspaceId: string; userId: string
}) {
  const preflight = useChatRunPreflight({ chatId, workspaceId, userId, enabled: true })
  const configured = preflight.data?.model || ""
  const provider = preflight.data?.payer.provider?.trim() || ""

  const catalogue = useQuery({
    queryKey: ["model-catalogue", workspaceId, provider],
    enabled: !!provider && !!workspaceId,
    retry: false, staleTime: 5 * 60_000,
    queryFn: async ({ signal }): Promise<Catalogue> => {
      const response = await apiFetch(`/api/v1/models?provider=${encodeURIComponent(provider)}&workspace_id=${encodeURIComponent(workspaceId)}`, { signal })
      if (!response.ok) throw new Error("unavailable")
      const body = await response.json() as Partial<Catalogue>
      if (!Array.isArray(body.models)) throw new Error("unavailable")
      return { provider: body.provider || provider, source: body.source || "", models: body.models }
    },
  })

  if (preflight.isError && preflight.error.message === "denied") return null
  const label = configured ? `Model: ${configured}` : "Model: unknown"

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="ghost" size="sm" aria-label={label}
          className="h-7 max-w-full gap-1.5 px-2 text-xs text-muted-foreground hover:text-foreground coarse:min-h-12">
          <Sparkles className="h-3 w-3" />
          <span className="truncate font-mono">{configured || "Model unknown"}</span>
          <ChevronDown className="h-3 w-3" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-[260px] max-w-[min(24rem,calc(100vw-2rem))]">
        <DropdownMenuLabel className="text-xs">
          {configured ? `Configured for this chat: ${configured}` : "The configured model could not be read."}
        </DropdownMenuLabel>
        <CatalogueBody provider={provider} configured={configured} query={catalogue} preflightPending={preflight.isPending} />
        <p className="px-2 py-1.5 text-xs text-muted-foreground">
          A listed model does not mean you may use it or that this chat will run it.
        </p>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function CatalogueBody({ provider, configured, query, preflightPending }: {
  provider: string; configured: string
  query: { isPending: boolean; isError: boolean; data?: Catalogue }
  preflightPending: boolean
}) {
  const note = "px-2 py-1.5 text-xs text-muted-foreground"
  if (preflightPending) return <p role="status" className={note}>Loading models…</p>
  if (!provider) return <p role="status" className={note}>No model list: the provider is not reported for this chat.</p>
  if (query.isPending) return <p role="status" className={note}>Loading models…</p>
  if (query.isError || !query.data) {
    return <p role="status" className={note}>Could not load the model list.{configured ? ` This chat still uses ${configured}.` : ""}</p>
  }
  const { models, source } = query.data
  if (!models.length) return <p role="status" className={note}>No models are listed for {provider}.</p>
  const kind = source === "live" ? "Live list" : source === "curated" ? "Curated list" : "List"
  return (
    <>
      <p className={note}>{kind} from {provider}{source === "curated" ? " (not checked against your account)" : ""}</p>
      {configured && !models.some((m) => m.id === configured) && (
        <p className={note}>{configured} is not in this list.</p>
      )}
      {models.map((m) => (
        <DropdownMenuItem key={m.id} disabled className="flex items-start gap-2">
          <div className="min-w-0 flex-1">
            <span className="block truncate text-sm font-medium">{m.display_name || m.id}</span>
            {m.display_name && m.display_name !== m.id && (
              <span className="block truncate font-mono text-xs text-muted-foreground">{m.id}</span>
            )}
          </div>
          {m.id === configured && <Check className="mt-0.5 h-4 w-4 shrink-0 text-primary" aria-label="Configured for this chat" />}
        </DropdownMenuItem>
      ))}
    </>
  )
}
