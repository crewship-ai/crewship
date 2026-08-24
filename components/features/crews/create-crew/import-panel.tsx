"use client"

import { useMemo, useState } from "react"
import { CircleAlert, FileCode2, Info, TriangleAlert } from "lucide-react"
import {
  CreateSurfaceDropzone,
  CreateSurfaceNotice,
  CreateSurfaceSection,
} from "@/components/layout/create-surface"
import { parseCrewManifest, type CrewImportResult } from "./import-crew-yaml"
import type { WizardState } from "./types"

/**
 * Read a crew manifest into the form instead of applying it.
 *
 * A panel rather than a step, and rather than a dialog over the dialog — the
 * same shape the base-image catalogue and the icon picker already use here.
 * Importing is not a stage everybody passes through, so it does not get a
 * number in the step strip.
 *
 * The panel parses as you type or drop and shows BOTH halves of the answer
 * before you commit to it: what it will fill in, and what the file contains
 * that the wizard cannot create. The second half is the point. A manifest is
 * usually mostly `agents:`, and the wizard's submit path has no call that
 * makes an agent — importing one and quietly keeping the container config
 * would hand back a crew missing the thing the file was about.
 */

interface Props {
  onApply: (patch: Partial<WizardState>) => void
}

export function ImportCrewPanel({ onApply }: Props) {
  const [text, setText] = useState("")
  const [fileName, setFileName] = useState<string | null>(null)

  // Parse on every keystroke. The document is small, the parse is pure, and
  // a "Validate" button would be a second thing to press for an answer we
  // can already give.
  const parsed = useMemo((): { ok: CrewImportResult } | { error: string } | null => {
    if (!text.trim()) return null
    try {
      return { ok: parseCrewManifest(text) }
    } catch (e) {
      return { error: e instanceof Error ? e.message : String(e) }
    }
  }, [text])

  async function readFile(f: File) {
    setFileName(f.name)
    setText(await f.text())
  }

  const result = parsed && "ok" in parsed ? parsed.ok : null

  return (
    <div className="flex flex-col gap-4">
      <CreateSurfaceSection
        title="Crew manifest"
        icon={FileCode2}
        accent="sky"
        hint="the same kind: Crew file crewship apply takes"
      >
        <CreateSurfaceDropzone
          id="crew-import-file"
          accept=".yaml,.yml,text/yaml,application/x-yaml"
          fileName={fileName}
          placeholder="Choose a .yaml file"
          icon={FileCode2}
          accent="sky"
          onFile={readFile}
        />

        <label htmlFor="crew-import-text" className="text-[11px] text-muted-foreground">
          …or paste it
        </label>
        <textarea
          id="crew-import-text"
          value={text}
          onChange={(e) => { setText(e.target.value); setFileName(null) }}
          rows={10}
          spellCheck={false}
          placeholder={"apiVersion: crewship/v1\nkind: Crew\nmetadata:\n  name: Data Engineering\n  slug: data-eng\nspec:\n  devcontainer:\n    image: mcr.microsoft.com/devcontainers/python:3.12"}
          className="w-full rounded-lg border border-hairline bg-foreground/[0.02] p-2.5 font-mono text-[11px] leading-relaxed outline-none transition-shadow focus:border-primary focus:ring-2 focus:ring-primary/20"
        />
      </CreateSurfaceSection>

      {parsed && "error" in parsed && (
        <CreateSurfaceNotice tone="error" icon={CircleAlert}>
          {parsed.error}
        </CreateSurfaceNotice>
      )}

      {result && (
        <CreateSurfaceSection title="What this fills in" icon={Info} accent="green">
          <ImportSummary result={result} />

          {result.notImported.length > 0 && (
            /* Not a footnote. `crewship apply` is genuinely the right tool for
               a document with agents in it, and saying so is more useful than
               letting someone discover it after the crew exists. */
            <CreateSurfaceNotice tone="warn" icon={TriangleAlert}>
              This file also declares{" "}
              {result.notImported.map((b, i) => (
                <span key={b.label}>
                  {i > 0 && (i === result.notImported.length - 1 ? " and " : ", ")}
                  <strong>{b.count} {b.count === 1 ? singular(b.label) : b.label}</strong>
                </span>
              ))}
              {result.agentNames.length > 0 && <> ({result.agentNames.join(", ")})</>}. The wizard
              creates a crew, not its contents — those are left behind. To apply the whole document,
              run{" "}
              <code className="rounded bg-black/40 px-1 py-0.5 font-mono text-[11px]">
                crewship apply -f {fileName || "crew.yaml"}
              </code>{" "}
              instead.
            </CreateSurfaceNotice>
          )}

          <button
            type="button"
            onClick={() => onApply(result.patch)}
            className="h-9 self-start rounded-md bg-primary px-3 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary-hover max-sm:h-12 max-sm:w-full"
          >
            Fill the form from this file
          </button>
        </CreateSurfaceSection>
      )}
    </div>
  )
}

/** "agents" → "agent". Only ever fed the labels import-crew-yaml emits. */
function singular(label: string): string {
  if (label === "shared files") return "shared file"
  return label.endsWith("s") ? label.slice(0, -1) : label
}

function ImportSummary({ result }: { result: CrewImportResult }) {
  const { patch } = result

  const rows: { label: string; value: string }[] = []
  if (patch.name) rows.push({ label: "Name", value: patch.name })
  if (patch.slug) rows.push({ label: "Slug", value: patch.slug })
  if (patch.description) rows.push({ label: "Description", value: patch.description })
  if (patch.runtimeImage) rows.push({ label: "Runtime image", value: patch.runtimeImage })

  if (patch.devcontainerConfig) {
    try {
      const dc = JSON.parse(patch.devcontainerConfig) as Record<string, unknown>
      if (typeof dc.image === "string") rows.push({ label: "Base image", value: dc.image })
      const features = dc.features && typeof dc.features === "object" ? Object.keys(dc.features) : []
      if (features.length) {
        rows.push({ label: "Tooling", value: `${features.length} ${features.length === 1 ? "feature" : "features"}` })
      }
      const env = dc.containerEnv && typeof dc.containerEnv === "object" ? Object.keys(dc.containerEnv) : []
      if (env.length) rows.push({ label: "Environment", value: env.join(", ") })
    } catch {
      // devcontainerJSON() built this string with JSON.stringify, so a parse
      // failure here is impossible rather than merely unlikely — and a
      // summary row is not worth throwing the whole import away for.
    }
  }

  if (patch.miseConfig) {
    try {
      const tools = (JSON.parse(patch.miseConfig) as { tools?: Record<string, string> }).tools ?? {}
      const pinned = Object.entries(tools).map(([t, v]) => (v ? `${t} ${v}` : t))
      if (pinned.length) rows.push({ label: "Pinned runtimes", value: pinned.join(", ") })
    } catch { /* same as above */ }
  }

  if (patch.memoryMB || patch.cpus) {
    rows.push({
      label: "Size",
      value: [
        patch.cpus && `${patch.cpus} ${patch.cpus === 1 ? "core" : "cores"}`,
        patch.memoryMB && `${patch.memoryMB} MB`,
      ].filter(Boolean).join(" · "),
    })
  }

  if (rows.length === 0) {
    return (
      <p className="text-[11px] text-muted-foreground">
        A valid crew manifest, but it sets nothing the wizard asks about. Continue with the form as
        it stands.
      </p>
    )
  }

  return (
    <dl className="grid grid-cols-[minmax(0,7rem)_1fr] gap-x-3 gap-y-1.5 text-xs">
      {rows.map((r) => (
        <div key={r.label} className="contents">
          <dt className="truncate text-[11px] text-muted-foreground">{r.label}</dt>
          <dd className="min-w-0 truncate text-foreground/85">{r.value}</dd>
        </div>
      ))}
    </dl>
  )
}
