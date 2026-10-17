"use client"

import { Check, Copy, Terminal } from "lucide-react"
import { useState } from "react"

import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"

// "Skills from the command line" (Harbor 2 #3396). Only commands the CLI has
// today; the mockup's `skill add` and `skill update --all` do not exist yet.

export const SKILL_CLI_COMMANDS: [string, string][] = [
  ["crewship skill list --source CUSTOM", "What is in the library"],
  ["crewship skill import --repo https://github.com/<owner>/<repo> --paths skills/<name>/SKILL.md", "Add from a repository"],
  ["crewship skill assign <skill> --to-crew <crew>", "Give it to a whole crew"],
  ["crewship skill init <slug>", "Start a new SKILL.md"],
  ["crewship skill proposed list --crew <crew>", "What agents propose"],
]

function CopyButton({ text }: { text: string }) {
  const [done, setDone] = useState(false)
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      aria-label={`Copy ${text}`}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text)
          setDone(true)
          setTimeout(() => setDone(false), 1500)
        } catch {
          toast.error("The browser blocked the clipboard. Select the command and copy it.")
        }
      }}
    >
      {done ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
    </Button>
  )
}

export function SkillsCliDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="md" restoreOpenerFocus>
      <CreateSurfaceHeader icon={Terminal} title="Skills from the command line" onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody>
        <ol className="flex flex-col gap-2.5" data-slot="skills-cli-commands">
          {SKILL_CLI_COMMANDS.map(([cmd, what]) => (
            <li key={cmd} className="rounded-md border border-border bg-muted/40 px-3 py-2">
              <div className="flex items-start gap-2">
                <code className="min-w-0 flex-1 break-all font-mono text-label text-foreground">{cmd}</code>
                <CopyButton text={cmd} />
              </div>
              <small className="text-label text-muted-foreground">{what}</small>
            </li>
          ))}
        </ol>
      </CreateSurfaceBody>
      <CreateSurfaceFooter hint={null} onCancel={() => onOpenChange(false)} cancelLabel="Close" />
    </CreateSurface>
  )
}
