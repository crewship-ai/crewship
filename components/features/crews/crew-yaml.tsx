"use client"

import { useState } from "react"
import { Copy, Download } from "lucide-react"

import { Button } from "@/components/ui/button"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { toast } from "@/lib/toast"

import { CrCard, CrLine } from "./cr-card"
import { exportCrewManifest, type CrewExport } from "./crew-manifest-export"

/** The commands of a crew that exist in the CLI today, with what each does. */
export function crewCommandRows(slug: string): { cmd: string; note: string }[] {
  return [
    { cmd: `crewship crew get ${slug}`, note: "Show the crew, its agents and container" },
    { cmd: `crewship export crew ${slug} > ${slug}.yaml`, note: "Save the crew as YAML" },
    { cmd: `crewship apply --file ${slug}.yaml`, note: "Create or update it from YAML" },
    { cmd: `crewship agent list --crew ${slug}`, note: "List its agents" },
  ]
}

export function crewCommands(slug: string): string[] {
  return crewCommandRows(slug).map((r) => r.cmd)
}

/** Saves text as a file through a temporary link. */
function saveText(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/yaml" }))
  const a = document.createElement("a")
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 0)
}

type YamlState = { kind: "idle" } | { kind: "loading" } | { kind: "error" } | { kind: "ready"; out: CrewExport }

/**
 * The crew as YAML, composed in the browser. `view` opens the read-only
 * surface, `download` saves <slug>.yaml; both the Settings card and the crew
 * options menu use it, so they cannot drift.
 */
export function useCrewYaml(workspaceId: string, crew: { slug: string; name: string }) {
  const [open, setOpen] = useState(false)
  const [state, setState] = useState<YamlState>({ kind: "idle" })
  const file = `${crew.slug}.yaml`

  const load = async (): Promise<CrewExport | null> => {
    if (state.kind === "ready") return state.out
    setState({ kind: "loading" })
    try {
      const out = await exportCrewManifest(workspaceId, crew.slug)
      setState({ kind: "ready", out })
      return out
    } catch {
      setState({ kind: "error" })
      return null
    }
  }

  const view = () => { setOpen(true); void load() }
  const download = async () => {
    const out = await load()
    if (!out) { toast.error(`${crew.name} could not be exported`); return }
    saveText(file, out.yaml)
    toast.success(`${file} downloaded`)
  }
  const copy = (text: string, done: string) => {
    void navigator.clipboard.writeText(text).then(() => toast.success(done), () => toast.error("Could not copy"))
  }

  const surface = (
    <CreateSurface open={open} onOpenChange={setOpen} size="lg">
      <CreateSurfaceHeader
        concept="settings"
        context={crew.name}
        title={file}
        description={<>The same crew as code. Apply it with <code className="font-mono">crewship apply --file {file}</code>.</>}
        onClose={() => setOpen(false)}
      />
      <CreateSurfaceBody className="space-y-3">
        {state.kind === "error" ? (
          <p role="alert" className="text-body text-chip-danger-fg">{crew.name} could not be exported. Try again, or run <code className="font-mono">crewship export crew {crew.slug}</code>.</p>
        ) : state.kind === "ready" ? (
          <>
            {state.out.warnings.length > 0 && (
              <ul role="status" className="list-disc pl-5 text-label text-chip-warn-fg">{state.out.warnings.map((w) => <li key={w}>{w}</li>)}</ul>
            )}
            <div className="flex justify-end">
              <Button variant="ghost" size="sm" onClick={() => copy(state.out.yaml, "YAML copied")}><Copy className="size-3.5" aria-hidden /> Copy</Button>
            </div>
            <pre data-testid="crew-yaml" tabIndex={0} className="max-h-[50vh] overflow-auto rounded-md bg-muted px-3 py-2 font-mono text-label text-foreground">{state.out.yaml}</pre>
          </>
        ) : (
          <p role="status" className="text-body text-muted-foreground">Reading the crew…</p>
        )}
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        hint={null}
        onCancel={() => setOpen(false)}
        cancelLabel="Close"
        primaryLabel="Download"
        primaryIcon={Download}
        primaryDisabled={state.kind !== "ready"}
        onPrimary={() => { if (state.kind === "ready") { saveText(file, state.out.yaml); toast.success(`${file} downloaded`) } }}
      />
    </CreateSurface>
  )

  return { view, download, copy, surface }
}

/**
 * Settings › Command line and crewship.yaml. The commands, and the crew as
 * YAML: the same text `crewship export crew <slug>` prints, composed in the
 * browser from existing reads (there is no export route, #3451). Credential
 * values are never in it.
 */
export function CrewCommandLine({ workspaceId, crew }: { workspaceId: string; crew: { slug: string; name: string } }) {
  const { view, download, copy, surface } = useCrewYaml(workspaceId, crew)

  return (
    <>
      <CrCard title="Command line and crewship.yaml" concept="settings">
        {crewCommandRows(crew.slug).map(({ cmd, note }) => (
          <CrLine
            key={cmd}
            title={<code className="font-mono text-label">{cmd}</code>}
            sub={note}
            right={
              <Button variant="ghost" size="icon-sm" aria-label={`Copy ${cmd}`} onClick={() => copy(cmd, "Command copied")}>
                <Copy className="size-3.5" />
              </Button>
            }
          />
        ))}
        <div className="flex flex-wrap gap-2 border-t border-border px-4 py-2.5">
          <Button variant="outline" size="sm" onClick={view}>View as YAML</Button>
          <Button variant="ghost" size="sm" onClick={() => void download()}><Download className="size-3.5" aria-hidden /> Download</Button>
        </div>
        <p className="px-4 pb-3 text-label text-muted-foreground">
          The file is the same text the export command above prints. It does not carry autonomy, budget, persona layers, people or links yet; those stay in the web app. Credential values are never in it.
        </p>
      </CrCard>
      {surface}
    </>
  )
}
