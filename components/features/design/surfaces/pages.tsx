"use client"

/**
 * Pages — New page, Import.
 *
 * These are the two surfaces that frost the page behind them
 * (`bg-background/70 backdrop-blur-md`) and the only two that are not a Radix
 * Dialog at all, so neither has a focus trap. Both are `xl`/`sm` here on the
 * shared shell — the blur and the hand-rolled overlay go, and nothing else
 * about them changes.
 *
 * Import keeps the one genuinely good idea in the surfaces being replaced: an
 * install that fails prints its unresolved references AS A FORM, so the next
 * attempt is fields to fill rather than a paragraph to decode.
 */

import * as React from "react"
import { AlertTriangle, Eye, FileJson, Save, Upload, Wand2 } from "lucide-react"

import { Input } from "@/components/ui/input"
import {
  CreateSurfaceBody,
  CreateSurfaceDropzone,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfaceRefusal,
  CreateSurfaceSecondaryAction,
  CreateSurfaceSection,
} from "@/components/layout/create-surface"

/* ══ Pages → New page ═══════════════════════════════════════════════════
 *
 * The first version of this specimen was a FORM — name, slug, visibility, a
 * plain textarea, a live toggle. That is not what the door opens. The real
 * surface (page-editor.tsx, 1116 lines) is a CodeMirror YAML buffer holding a
 * `crewship/v1` Page document, with no form controls at all: everything beside
 * the buffer is read out of it.
 *
 * The agent migrating it said so plainly — "the specimen is a different surface
 * entirely… if it is meant as a redesign proposal it should say so, because it
 * reads as the target". It read as the target, and a specimen that misdescribes
 * the thing it proposes to replace moves the migration in the wrong direction.
 * This is the third time I drew a shipped surface poorer or other than it is
 * (credential shapes, the crew base image, this), and all three had the same
 * cause: I did not read the component closely enough before drawing it.
 *
 * So this now shows the real shape — one buffer, the advisory bands that sit
 * above it, and the footer — rather than a form nobody asked for.
 * ══════════════════════════════════════════════════════════════════════ */

const PAGE_DOCUMENT = `apiVersion: crewship/v1
kind: Page
metadata:
  name: fleet-health
  title: Fleet health
spec:
  panels:
    - id: runs-today
      schema: metric.v1
      title: Runs today
      sla: 5m
      span: 3
    - id: failing
      schema: table.v1
      title: Failing routines
      span: 9
      actions:
        - label: Re-run all
          routine: nightly-sweep`

export function NewPageContent({ onClose }: { onClose: () => void }) {
  const [doc, setDoc] = React.useState(PAGE_DOCUMENT)
  const dirty = doc !== PAGE_DOCUMENT

  return (
    <>
      <CreateSurfaceHeader
        concept="pages"
        context="Pages"
        title="New page"
        onClose={onClose}
        meta={
          <span className="flex items-center gap-2">
            <span className="rounded border border-hairline bg-foreground/[0.05] px-1.5 py-px font-mono text-[10px]">
              kind: Page
            </span>
            {dirty && <span className="text-warn">unsaved</span>}
          </span>
        }
      />

      {/* The advisory bands sit ABOVE the buffer and outside the scrollport,
          because they warn about data loss. The kit has no band slot here yet
          — the migration hand-rolled these, and it is gap #19 on the list. */}
      <div className="shrink-0 border-b border-hairline bg-warn/[0.06] px-4 py-2 sm:px-5">
        <span className="flex items-start gap-2 text-[11px] leading-relaxed text-foreground/85">
          <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0 text-warn" />
          Saving replaces the panel list. A panel you cannot see here would be deleted.
        </span>
      </div>

      <CreateSurfaceBody padded={false} scroll={false} className="flex flex-col">
        <textarea
          value={doc}
          onChange={(e) => setDoc(e.target.value)}
          spellCheck={false}
          aria-label="Page document"
          className="min-h-[320px] flex-1 resize-none bg-background p-4 font-mono text-[11px] leading-relaxed text-foreground/85 outline-none sm:p-5"
        />
      </CreateSurfaceBody>

      <CreateSurfaceRefusal
        tone="info"
        message={
          <>
            The real surface is CodeMirror with schema completion and an inline linter, not a textarea — this
            specimen shows the SHAPE. Six panel schemas are available:{" "}
            <span className="font-mono">metric · series · status · table · narrative · embed</span>.
          </>
        }
      />

      <CreateSurfaceFooter
        hint="The document is validated before the page is written."
        onCancel={onClose}
        guardCancel
        secondary={<CreateSurfaceSecondaryAction icon={Eye}>Preview</CreateSurfaceSecondaryAction>}
        primaryLabel="Create page"
        primaryIcon={Save}
        onPrimary={onClose}
      />
    </>
  )
}

/* ══ Pages → Import ═════════════════════════════════════════════════════ */

const BINDABLE = [
  { ref: "crew:platform", usedBy: "2 panels" },
  { ref: "routine:nightly-sweep", usedBy: "1 panel" },
]

export function ImportPageContent({ onClose }: { onClose: () => void }) {
  const [fileName, setFileName] = React.useState<string | null>(null)
  const [slug, setSlug] = React.useState("fleet-health")
  const [bind, setBind] = React.useState<Record<string, string>>({})

  const mapped = Object.values(bind).filter((v) => v.trim()).length

  return (
    <>
      <CreateSurfaceHeader
        icon={Upload}
        accent="green"
        context="Pages"
        title="Import a page"
        description="A bundle written by Export."
        onClose={onClose}
      />

      <CreateSurfaceBody className="space-y-3">
        <CreateSurfaceDropzone
          id="page-import-file"
          icon={FileJson}
          accent="green"
          accept="application/json,.json"
          fileName={fileName}
          placeholder="Choose a bundle written by Export"
          onFile={(f) => setFileName(f.name)}
        />

        {fileName && (
          <>
            <div className="text-xs text-muted-foreground">
              <strong className="text-foreground/90">Fleet health</strong> · 6 panels · exported 2026-08-19
            </div>

            <CreateSurfaceField label="Install as" htmlFor="page-import-slug">
              <Input
                id="page-import-slug"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
              />
            </CreateSurfaceField>

            <CreateSurfaceSection
              title="What this page needs here"
              hint={mapped > 0 ? `${mapped} mapped` : "leave empty to resolve by name"}
            >
              <p className="text-[11px] leading-relaxed text-muted-foreground-soft">
                Fill one in only to point it somewhere else. The install is refused as a whole if anything
                cannot be found, and it will say which.
              </p>
              {BINDABLE.map((r) => (
                <CreateSurfaceField
                  key={r.ref}
                  label={<span className="font-mono normal-case tracking-normal">{r.ref}</span>}
                  hint={`used by ${r.usedBy}`}
                >
                  <Input
                    value={bind[r.ref] ?? ""}
                    onChange={(e) => setBind((b) => ({ ...b, [r.ref]: e.target.value }))}
                    placeholder={r.ref}
                    aria-label={`Bind ${r.ref}`}
                    className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
                  />
                </CreateSurfaceField>
              ))}
            </CreateSurfaceSection>

            {/* The refusal IS the worklist — rendered as rows so the next
                attempt is a form to fill rather than a paragraph to decode. */}
            <CreateSurfaceNotice tone="error" icon={AlertTriangle}>
              <strong className="text-foreground">routine:nightly-sweep</strong> — no routine of that slug
              exists here <span className="text-muted-foreground">(used by 1 panel)</span>
            </CreateSurfaceNotice>
          </>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        onCancel={onClose}
        primaryLabel="Install"
        primaryIcon={Wand2}
        primaryDisabled={!fileName}
        onPrimary={onClose}
      />
    </>
  )
}
