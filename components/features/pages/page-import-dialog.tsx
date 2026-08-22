"use client"

/**
 * Installing a page somebody else exported.
 *
 * The whole difficulty of an import is one thing: a bundle names crews and
 * routines that exist in the workspace it came FROM, and those names may mean
 * nothing here. But they often DO — two workspaces seeded from the same
 * catalogue share slugs — so the server resolves what it can on its own, and
 * the mapping inputs below are an override rather than a gate. Demanding them
 * up front, which this first did, made people invent answers to a question
 * nobody had asked.
 *
 * The server refuses the whole import if anything is unbound (422, nothing
 * written) and names every reference it could not resolve. That refusal is not
 * an error to paraphrase into a toast: it is the worklist. `usePageImport`
 * hands it back parsed and this dialog renders it as inputs, so a person who
 * gets it wrong twice is filling in a form rather than re-reading a paragraph.
 *
 * `script` and `webhook` producers are NOT bindable and are not asked about —
 * there is no table of scripts to point them at, so they travel as
 * declarations. That is why `bindable` is on the wire at all.
 */

import * as React from "react"
import { toast } from "sonner"
import { AlertTriangle, Upload } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import {
  type WireBundleRef,
  type WirePageBundle,
  type WireUnresolvedRef,
  usePageImport,
} from "@/hooks/use-page-sharing"

export interface PageImportDialogProps {
  workspaceId: string
  onClose: () => void
  /** Called with the installed page's slug so the shell can open it. */
  onImported: (slug: string) => void
}

const BUNDLE_FORMAT = "crewship-page-bundle/v1"

export function PageImportDialog({ workspaceId, onClose, onImported }: PageImportDialogProps) {
  const [bundle, setBundle] = React.useState<WirePageBundle | null>(null)
  const [fileName, setFileName] = React.useState("")
  const [slug, setSlug] = React.useState("")
  const [bind, setBind] = React.useState<Record<string, string>>({})
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const [unresolved, setUnresolved] = React.useState<WireUnresolvedRef[]>([])

  const install = usePageImport(workspaceId, {
    onOk: ({ slug: installed }) => {
      toast.success("Imported", { description: `${installed} is installed here.` })
      onImported(installed)
    },
    onRefused: (m) => {
      setUnresolved([])
      setRefusal(m)
    },
    onUnresolved: (refs, message) => {
      setUnresolved(refs)
      setRefusal(message)
    },
  })

  const bindable: WireBundleRef[] = React.useMemo(
    () => (bundle?.references ?? []).filter((r) => r.bindable),
    [bundle],
  )

  const readFile = async (file: File) => {
    setRefusal(null)
    setUnresolved([])
    setFileName(file.name)
    try {
      const parsed = JSON.parse(await file.text()) as WirePageBundle
      // Checked here rather than left to the server, because handing a page
      // DOCUMENT to an importer is the likely mistake and the local answer can
      // name the right command instead of a format string.
      if (parsed?.format !== BUNDLE_FORMAT) {
        setBundle(null)
        setRefusal(
          `That is not an export bundle — it declares format ${
            parsed?.format ? `"${parsed.format}"` : "nothing"
          }. A bundle is what the Export card writes; a page document (kind: Page) is authored in the editor instead.`,
        )
        return
      }
      setBundle(parsed)
      setSlug(parsed.page?.slug ?? "")
      setBind({})
    } catch {
      setBundle(null)
      setRefusal("That file is not JSON.")
    }
  }

  // NOT a required-fields check, and that was the first thing this got wrong.
  // A reference resolves on its own whenever a crew or routine of the same
  // slug already exists here — which is the common case when two workspaces
  // were seeded from the same catalogue — so demanding a mapping up front made
  // people invent answers to a question the server had not asked. Nothing is
  // written unless every reference resolves, and the refusal names the ones
  // that did not; so the honest flow is to let them try, and turn that refusal
  // into the form. These inputs are the override, not the gate.
  const overridden = bindable.filter((r) => bind[r.ref]?.trim()).length

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8">
      <button
        type="button"
        aria-label="Close import"
        onClick={onClose}
        className="absolute inset-0 bg-background/70 backdrop-blur-md"
      />
      <div
        role="dialog"
        aria-label="Import a page"
        className="relative flex max-h-[90vh] w-full max-w-[560px] flex-col overflow-hidden rounded-xl border border-border/60 bg-card shadow-2xl"
      >
        <div className="flex shrink-0 items-center justify-between border-b border-border/60 px-4 py-2.5">
          <span className="type-page-value font-medium">Import a page</span>
          <Button size="sm" variant="ghost" className="h-7 px-2 text-xs" onClick={onClose}>
            Cancel
          </Button>
        </div>

        <div className="flex flex-col gap-3 overflow-auto p-4">
          {refusal && (
            <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/[0.06] p-2.5">
              <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0 text-destructive" />
              <span className="type-page-meta text-foreground/90">{refusal}</span>
            </div>
          )}

          {/* The refusal IS the worklist. Rendered as rows so the next attempt
              is a form to fill rather than a paragraph to decode. */}
          {unresolved.length > 0 && (
            <div className="flex flex-col gap-1.5 rounded-md border border-border/50 p-2.5">
              <span className="type-page-label text-muted-foreground">Could not be bound</span>
              {unresolved.map((u) => (
                <div key={u.ref} className="type-page-meta">
                  <span className="type-page-stamp text-foreground/90">{u.ref}</span>
                  <span className="text-muted-foreground"> — {u.reason}</span>
                  {u.used_by.length > 0 && (
                    <span className="text-muted-foreground-soft"> (used by {u.used_by.join(", ")})</span>
                  )}
                </div>
              ))}
            </div>
          )}

          <label className="flex cursor-pointer items-center gap-2 rounded-md border border-dashed border-border p-3 hover:bg-white/[0.02]">
            <Upload className="h-4 w-4 shrink-0 text-muted-foreground-soft" />
            <span className="type-page-meta text-muted-foreground">
              {fileName || "Choose a bundle written by Export"}
            </span>
            <input
              type="file"
              accept="application/json,.json"
              className="hidden"
              onChange={(e) => {
                const f = e.target.files?.[0]
                if (f) void readFile(f)
              }}
            />
          </label>

          {bundle && (
            <>
              <div className="type-page-meta text-muted-foreground">
                <strong className="text-foreground/90">{bundle.page.name}</strong> ·{" "}
                {bundle.metadata?.panel_count ?? bundle.page.panels?.length ?? 0} panels · exported{" "}
                {bundle.metadata?.exported_at ?? "—"}
              </div>

              <div className="flex flex-col gap-1">
                <span className="type-page-label text-muted-foreground">Install as</span>
                <Input
                  value={slug}
                  onChange={(e) => setSlug(e.target.value)}
                  placeholder={bundle.page.slug}
                  aria-label="Slug to install under"
                  className="h-8 text-xs"
                />
              </div>

              {bindable.length > 0 && (
                <div className="flex flex-col gap-2">
                  <span className="type-page-label text-muted-foreground">
                    What this page needs here
                    {overridden > 0 ? ` · ${overridden} mapped` : ""}
                  </span>
                  <p className="type-page-meta text-muted-foreground-soft">
                    Leave these empty to let the same names resolve here. Fill one in only to point
                    it somewhere else — the install is refused as a whole if anything cannot be
                    found, and it will say which.
                  </p>
                  {bindable.map((r) => (
                    <div key={r.ref} className="flex flex-col gap-1">
                      <div className="type-page-meta text-muted-foreground">
                        <span className="type-page-stamp text-foreground/90">{r.ref}</span>
                        {r.used_by.length > 0 && <span> — used by {r.used_by.join(", ")}</span>}
                      </div>
                      <Input
                        value={bind[r.ref] ?? ""}
                        onChange={(e) => setBind((b) => ({ ...b, [r.ref]: e.target.value }))}
                        placeholder={r.ref}
                        aria-label={`Bind ${r.ref}`}
                        className="h-8 text-xs"
                      />
                    </div>
                  ))}
                </div>
              )}

              {bindable.length === 0 && (
                <p className="type-page-meta text-muted-foreground-soft">
                  Nothing to bind — this bundle names no crews or routines that have to exist here.
                </p>
              )}
            </>
          )}
        </div>

        <div className="flex shrink-0 items-center justify-end gap-2 border-t border-border/60 px-4 py-2.5">
          <Button
            size="sm"
            className="h-8 gap-1.5 px-3 text-xs"
            disabled={!bundle || install.isPending}
            onClick={() => {
              if (!bundle) return
              setRefusal(null)
              setUnresolved([])
              install.mutate({
                bundle,
                slug: slug.trim() && slug.trim() !== bundle.page.slug ? slug.trim() : undefined,
                bind,
              })
            }}
          >
            {install.isPending && <Spinner className="h-3 w-3" />}
            Install
          </Button>
        </div>
      </div>
    </div>
  )
}
