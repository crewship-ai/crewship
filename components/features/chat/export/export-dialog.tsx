"use client"

import { useState } from "react"
import { useHotkeys } from "react-hotkeys-hook"
import { Download, FileText, Copy } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import type { ChatTurn } from "@/hooks/use-chat"

interface ExportDialogProps {
  turns: ChatTurn[]
  agentName?: string
}

function turnsToMarkdown(turns: ChatTurn[], agentName?: string): string {
  const ts = new Date().toISOString()
  const out: string[] = [
    `# ${agentName ? `Conversation with ${agentName}` : "Conversation"}`,
    `Exported ${ts}`,
    "",
  ]
  for (const turn of turns) {
    if (turn.role === "user") {
      out.push(`## You`)
      out.push(turn.parts.find((p) => p.type === "text")?.content ?? "")
      out.push("")
    } else if (turn.role === "assistant") {
      out.push(`## ${agentName ?? "Assistant"}`)
      for (const p of turn.parts) {
        if (p.type === "text") out.push(p.content)
        else if (p.type === "thinking") out.push(`> _Thinking:_ ${p.content}`)
        else if (p.type === "tool_call")
          out.push(`> _Tool:_ \`${p.metadata?.tool_name ?? p.content}\``)
      }
      out.push("")
    }
  }
  return out.join("\n")
}

export function ExportDialog({ turns, agentName }: ExportDialogProps) {
  const [open, setOpen] = useState(false)

  useHotkeys(
    "mod+e",
    (e) => {
      e.preventDefault()
      setOpen(true)
    },
    { enableOnFormTags: true, enableOnContentEditable: true },
  )

  const handleDownload = () => {
    const md = turnsToMarkdown(turns, agentName)
    const blob = new Blob([md], { type: "text/markdown" })
    const url = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = url
    a.download = `${agentName ?? "conversation"}-${Date.now()}.md`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
    setOpen(false)
    toast.success("Exported as Markdown")
  }

  const handleCopy = async () => {
    const md = turnsToMarkdown(turns, agentName)
    try {
      await navigator.clipboard.writeText(md)
      toast.success("Copied to clipboard")
      setOpen(false)
    } catch {
      toast.error("Failed to copy")
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Export conversation</DialogTitle>
          <DialogDescription>
            Save or share this chat. {turns.length} turn{turns.length !== 1 ? "s" : ""} in transcript.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-2">
          <Button variant="outline" onClick={handleDownload} className="justify-start gap-2">
            <Download className="h-4 w-4" />
            Download as Markdown
          </Button>
          <Button variant="outline" onClick={handleCopy} className="justify-start gap-2">
            <Copy className="h-4 w-4" />
            Copy to clipboard
          </Button>
          <Button variant="outline" disabled className="justify-start gap-2 opacity-60">
            <FileText className="h-4 w-4" />
            Download as PDF (coming soon)
          </Button>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
