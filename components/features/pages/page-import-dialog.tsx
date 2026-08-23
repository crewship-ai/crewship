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
 *
 * The chrome is `CreateSurface` and nothing else. This used to be one of the
 * two surfaces that hand-rolled `fixed inset-0` with
 * `bg-background/70 backdrop-blur-md` — no focus trap, no Esc, and a frosted
 * page that made Import read as a different application. Everything above is
 * unchanged by that move: same local format check, same request body, same
 * rule about when `slug` and `bind` reach the wire. The one thing that moved
 * is where the refusal is DRAWN — `CreateSurfaceRefusal` sits between the body
 * and the footer, outside the scrollport, which is the same argument for the
 * worklist that this dialog already made for itself.
 */

import * as React from "react"
import { toast } from "sonner"
import { FileJson, Upload, Wand2 } from "lucide-react"

import { Input } from "@/components/ui/input"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceDropzone,
  CreateSurfaceField,
  CreateSurfaceFooter,
  type CreateSurfaceFooterProps,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
  CreateSurfaceSection,
  useCreateSurfaceClose,
} from "@/components/layout/create-surface"
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
      // The format string alone is not the shape. A file that declares the
      // right format and carries no `page` would render as `bundle.page.name`
      // on the very next line, which is a crash rather than a refusal — and a
      // truncated or hand-edited bundle is exactly the file somebody drags in.
      if (!parsed.page || typeof parsed.page !== "object" || !Array.isArray(parsed.page.panels)) {
        setBundle(null)
        setRefusal("That bundle declares the right format but carries no page — it is incomplete.")
        return
      }
      if (parsed.references !== undefined && !Array.isArray(parsed.references)) {
        setBundle(null)
        setRefusal("That bundle's `references` is not a list, so its shape cannot be trusted.")
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

  const submit = () => {
    if (!bundle || install.isPending) return
    setRefusal(null)
    setUnresolved([])
    install.mutate({
      bundle,
      slug: slug.trim() && slug.trim() !== bundle.page.slug ? slug.trim() : undefined,
      bind,
    })
  }

  return (
    <CreateSurface
      open
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
      size="sm"
      // A read bundle plus whatever slug or mapping was typed on top of it is
      // the unsaved input. A file that was REFUSED leaves nothing to lose, so
      // it does not arm the guard.
      dirty={bundle !== null}
      discardLabel="this import"
      onSubmit={submit}
    >
      <CreateSurfaceHeader
        icon={Upload}
        accent="green"
        context="Pages"
        title="Import a page"
        description="A bundle written by Export."
        onClose={onClose}
      />

      <CreateSurfaceBody className="flex flex-col gap-3">
        {/* `htmlFor` rather than a wrapping label around a `hidden` input.
            `display: none` takes the input out of the tab order entirely, so
            the only way to reach this control was a mouse. The kit's dropzone
            keeps the `sr-only` + `htmlFor` pairing this had. */}
        <CreateSurfaceDropzone
          id="page-import-file"
          icon={FileJson}
          accent="green"
          accept="application/json,.json"
          fileName={fileName || null}
          placeholder="Choose a bundle written by Export"
          onFile={(f) => void readFile(f)}
        />

        {bundle && (
          <>
            <div className="type-page-meta text-muted-foreground">
              <strong className="text-foreground/90">{bundle.page.name}</strong> ·{" "}
              {bundle.metadata?.panel_count ?? bundle.page.panels?.length ?? 0} panels · exported{" "}
              {bundle.metadata?.exported_at ?? "—"}
            </div>

            <CreateSurfaceField label="Install as" htmlFor="page-import-slug">
              <Input
                id="page-import-slug"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                placeholder={bundle.page.slug}
                aria-label="Slug to install under"
                className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
              />
            </CreateSurfaceField>

            {bindable.length > 0 && (
              // The count rides in the TITLE rather than the section's `hint`
              // slot, which is hidden below `sm` — a phone would lose it.
              <CreateSurfaceSection
                title={`What this page needs here${overridden > 0 ? ` · ${overridden} mapped` : ""}`}
              >
                <p className="type-page-meta text-muted-foreground-soft">
                  Leave these empty to let the same names resolve here. Fill one in only to point
                  it somewhere else — the install is refused as a whole if anything cannot be
                  found, and it will say which.
                </p>
                {bindable.map((r) => (
                  <CreateSurfaceField
                    key={r.ref}
                    label={
                      // A ref is machine text: the field label's uppercasing
                      // and tracking would make `crew:platform` unreadable.
                      <span className="type-page-stamp normal-case tracking-normal text-foreground/90">
                        {r.ref}
                      </span>
                    }
                    hint={r.used_by.length > 0 ? `used by ${r.used_by.join(", ")}` : undefined}
                  >
                    <Input
                      value={bind[r.ref] ?? ""}
                      onChange={(e) => setBind((b) => ({ ...b, [r.ref]: e.target.value }))}
                      placeholder={r.ref}
                      aria-label={`Bind ${r.ref}`}
                      className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
                    />
                  </CreateSurfaceField>
                ))}
              </CreateSurfaceSection>
            )}

            {bindable.length === 0 && (
              <p className="type-page-meta text-muted-foreground-soft">
                Nothing to bind — this bundle names no crews or routines that have to exist here.
              </p>
            )}
          </>
        )}
      </CreateSurfaceBody>

      {/* The refusal IS the worklist, and now it is outside the scrollport:
          a list of references you have to scroll down to find is a list you
          will not find. Each row keeps what it is used by, because that is
          what says which panel breaks if you leave it. */}
      <CreateSurfaceRefusal
        message={refusal}
        fields={unresolved.map((u) => ({
          field: u.ref,
          reason: u.used_by.length > 0 ? `${u.reason} (used by ${u.used_by.join(", ")})` : u.reason,
        }))}
      />

      <ImportFooter
        onCancel={onClose}
        primaryLabel="Install"
        primaryIcon={Wand2}
        primaryDisabled={!bundle}
        busy={install.isPending}
        onPrimary={submit}
      />
    </CreateSurface>
  )
}

/**
 * The footer, with its Cancel routed through the discard guard.
 *
 * The shell guards Esc and the overlay click because it owns them, and
 * deliberately does NOT guard the footer's Cancel — on half the surfaces that
 * button means "back out of this panel". Here it means close the import, which
 * is the case `useCreateSurfaceClose` exists for. The hook reads a context the
 * shell provides INSIDE `CreateSurface`, so this has to be a child component
 * rather than three lines in the parent.
 */
function ImportFooter({ onCancel, ...rest }: CreateSurfaceFooterProps) {
  const guard = useCreateSurfaceClose()
  return <CreateSurfaceFooter {...rest} onCancel={() => guard(onCancel)} />
}
