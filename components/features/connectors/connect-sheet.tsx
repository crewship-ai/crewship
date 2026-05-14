"use client"

// ConnectorConnectSheet — the per-manifest connect form, opened when
// the user clicks a tile in ConnectorCatalog. Renders one of three
// shapes depending on manifest.auth_mode:
//
//   mcp_oauth → single "Connect" button (no fields). Submitting calls
//               install which returns next_step=mcp_oauth and the
//               OAuth flow begins.
//   pat / conn_string → SchemaForm with manifest.fields. Submit calls
//               install with the field values.
//   byo_oauth → manifest.docs.setup_md rendered above the form so the
//               user has step-by-step provider setup instructions,
//               plus SchemaForm with client_id+client_secret. Submit
//               calls install which returns oauth_url; the component
//               opens it in a popup and surfaces oauth-redirect to
//               the parent so it can wait for consent.
//
// The install endpoint is POST /api/v1/connectors/{id}/install with
// workspace_id in the query string and the field values in the JSON
// body. Errors are surfaced via the `sonner` toast helper.

import { useState, type ReactElement } from "react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"

import { SchemaForm } from "./schema-form"
import type { ConnectorManifest, InstallResponse, InstallResult } from "./types"

export interface ConnectorConnectSheetProps {
  manifest: ConnectorManifest | null
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  /**
   * Called once the install endpoint returns. `result.status` is
   * `installed` for synchronous paths (pat / conn_string / none) and
   * `oauth-redirect` for asynchronous paths (mcp_oauth / byo_oauth) —
   * in the latter case the component already opened the popup; the
   * parent typically just waits for consent to return and then
   * refreshes its credential list.
   */
  onInstalled: (result: InstallResult) => void
}

// classifyInstall folds the API response shape into the discriminated
// InstallResult so the parent doesn't have to special-case
// next_step="". An empty / missing next_step means the integration is
// fully active synchronously; "oauth" / "mcp_oauth" mean the user
// still owes us consent in the popup.
function classifyInstall(resp: InstallResponse): InstallResult {
  if (resp.next_step === "oauth" || resp.next_step === "mcp_oauth") {
    return {
      status: "oauth-redirect",
      integrationId: resp.integration_id,
      oauthUrl: resp.oauth_url ?? "",
      nextStep: resp.next_step,
    }
  }
  return { status: "installed", integrationId: resp.integration_id }
}

export function ConnectorConnectSheet(props: ConnectorConnectSheetProps): ReactElement | null {
  const { manifest, open, workspaceId, onInstalled } = props
  const [submitting, setSubmitting] = useState(false)

  if (!open || !manifest) return null

  async function callInstall(fields: Record<string, string>) {
    if (!manifest) return
    setSubmitting(true)
    try {
      const url = `/api/v1/connectors/${manifest.id}/install?workspace_id=${encodeURIComponent(workspaceId)}`
      const resp = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ fields }),
      })
      if (!resp.ok) {
        // Best-effort error extraction: API uses { error: "..." } but
        // some paths may return RFC 7807 Problem Details (detail field)
        // or nothing at all. Fall back to the HTTP status text so the
        // toast always says something useful.
        let msg = `install failed (${resp.status})`
        try {
          const body = (await resp.json()) as { error?: string; detail?: string }
          if (body.error) msg = body.error
          else if (body.detail) msg = body.detail
        } catch {
          // Body isn't JSON — keep the status-based message.
        }
        toast.error(msg)
        return
      }
      const body = (await resp.json()) as InstallResponse
      const result = classifyInstall(body)
      if (result.status === "oauth-redirect" && result.oauthUrl) {
        // Open the consent popup synchronously after the user click so
        // the browser doesn't flag it as a popup-blocker target. We're
        // already past the click handler at this point — modern browsers
        // generally let server-driven popups through within a few
        // hundred ms of a user gesture, which fetch latency fits inside.
        window.open(result.oauthUrl, "_blank", "noopener,noreferrer")
      }
      onInstalled(result)
    } catch (err) {
      const msg = err instanceof Error ? err.message : "install failed"
      toast.error(msg)
    } finally {
      setSubmitting(false)
    }
  }

  const fields = manifest.fields ?? []
  const setupMd = manifest.docs?.setup_md ?? ""
  const submitLabel = manifest.auth_mode === "mcp_oauth" || manifest.auth_mode === "byo_oauth"
    ? "Connect"
    : "Connect"

  return (
    <div role="dialog" aria-labelledby="connect-sheet-title" className="flex flex-col gap-4 p-4">
      <div className="flex flex-col gap-1">
        <h2 id="connect-sheet-title" className="text-lg font-semibold">
          Connect {manifest.name}
        </h2>
        {manifest.description && (
          <p className="text-sm text-muted-foreground">{manifest.description}</p>
        )}
      </div>

      {setupMd && (
        // Plain-text rendering keeps the bundle lean and matches what
        // the test queries against — substring match against the
        // markdown source. A future iteration can swap in a real
        // markdown renderer (e.g. react-markdown) without changing
        // this component's contract.
        <pre className="whitespace-pre-wrap rounded-md border bg-muted/30 p-3 text-xs">
          {setupMd}
        </pre>
      )}

      {fields.length > 0 ? (
        <SchemaForm
          fields={fields}
          onSubmit={callInstall}
          submitting={submitting}
          submitLabel={submitLabel}
        />
      ) : (
        // mcp_oauth (and the rare AuthModeNone case) — no fields to
        // fill in; just a Connect button that fires install with an
        // empty body.
        <div className="flex justify-end">
          <Button
            type="button"
            disabled={submitting}
            onClick={() => {
              void callInstall({})
            }}
          >
            {submitLabel}
          </Button>
        </div>
      )}
    </div>
  )
}
