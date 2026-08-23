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
import { AlertTriangle, Eye, FileJson, LayoutTemplate, Upload, Wand2 } from "lucide-react"

import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import {
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceDropzone,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceGrid,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfaceSecondaryAction,
  CreateSurfaceSection,
  CreateSurfaceTitleInput,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"

/* ══ Pages → New page ═══════════════════════════════════════════════════ */

const STARTER_YAML = `name: Fleet health
panels:
  - type: stat
    title: Runs today
    query: runs.count(since: "24h")
  - type: table
    title: Failing routines
    query: routines.failing()`

export function NewPageContent({ onClose }: { onClose: () => void }) {
  const [name, setName] = React.useState("")
  const [slug, setSlug] = React.useState("")
  const [visibility, setVisibility] = React.useState<"private" | "workspace" | "public">("workspace")
  const [yaml, setYaml] = React.useState(STARTER_YAML)
  const [live, setLive] = React.useState(true)

  const derived = slug || name.trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")

  return (
    <>
      <CreateSurfaceHeader
        concept="pages"
        context="Pages"
        title="New page"
        description="A page is panels on a grid. Write the YAML, or start from the template and edit it."
        onClose={onClose}
      />

      <CreateSurfaceBody className="space-y-5">
        <CreateSurfaceSection title="Identity" icon={LayoutTemplate} accent="green">
          <CreateSurfaceTitleInput
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Page name"
          />
          <CreateSurfaceGrid>
            <CreateSurfaceField label="Slug" htmlFor="page-slug" required hint="The URL this page lives at.">
              <Input
                id="page-slug"
                value={derived}
                onChange={(e) => setSlug(e.target.value)}
                placeholder="fleet-health"
                className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Visible to">
              <CreateSurfaceChoice
                ariaLabel="Visibility"
                value={visibility}
                onChange={setVisibility}
                options={[
                  { value: "private", label: "Me" },
                  { value: "workspace", label: "Workspace" },
                  { value: "public", label: "Public link" },
                ]}
              />
            </CreateSurfaceField>
          </CreateSurfaceGrid>
          {visibility === "public" && (
            <CreateSurfaceNotice tone="warn" icon={AlertTriangle}>
              A public page is readable by anyone with the link, without signing in. Panels that read
              credentials refuse to render there.
            </CreateSurfaceNotice>
          )}
        </CreateSurfaceSection>

        <CreateSurfaceSection title="Definition" icon={FileJson} accent="teal" hint="YAML">
          <textarea
            value={yaml}
            onChange={(e) => setYaml(e.target.value)}
            rows={12}
            spellCheck={false}
            aria-label="Page definition"
            className="w-full resize-none rounded-lg border border-hairline bg-background p-2.5 font-mono text-[11px] leading-relaxed text-foreground/85 outline-none focus:border-primary"
          />
        </CreateSurfaceSection>

        <CreateSurfaceToggleRow
          concept="activity"
          label="Live"
          hint="Panels re-query on their own schedule. Off renders once and shows the timestamp it was rendered at."
          control={<Switch checked={live} onCheckedChange={setLive} />}
        />
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        hint="The definition is validated before the page is written."
        onCancel={onClose}
        secondary={<CreateSurfaceSecondaryAction icon={Eye}>Preview</CreateSurfaceSecondaryAction>}
        primaryLabel="Create page"
        primaryDisabled={!name.trim() || !derived}
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
