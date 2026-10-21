"use client"

import { Copy } from "lucide-react"

import { Button } from "@/components/ui/button"
import { toast } from "@/lib/toast"

import { CrLine } from "./cr-card"

export interface CliCommandRow {
  cmd: string
  desc: string
}

/** Copies text and says so; a refusal says that instead. */
export function copyText(text: string, done: string) {
  void navigator.clipboard.writeText(text).then(() => toast.success(done), () => toast.error("Could not copy"))
}

/**
 * Command rows of a Settings card: the command in mono, what it does under
 * it, and a Copy button. Callers list only commands the CLI has.
 */
export function CliCommandRows({ rows }: { rows: readonly CliCommandRow[] }) {
  return (
    <>
      {rows.map(({ cmd, desc }) => (
        <CrLine
          key={cmd}
          title={<code className="font-mono text-label">{cmd}</code>}
          sub={desc}
          right={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`Copy ${cmd}`}
              className="coarse:min-h-[44px] coarse:min-w-[44px]"
              onClick={() => copyText(cmd, "Command copied")}
            >
              <Copy className="size-3.5" aria-hidden />
            </Button>
          }
        />
      ))}
    </>
  )
}
