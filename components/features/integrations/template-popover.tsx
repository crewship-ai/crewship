"use client"

import * as React from "react"
import { Plug, Terminal, Search } from "lucide-react"

import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { MCP_TEMPLATES, TEMPLATE_ICONS } from "@/components/features/mcp/templates"
import type { MCPTemplate } from "@/components/features/mcp/types"

interface TemplatePopoverProps {
  open: boolean
  onOpenChange: (v: boolean) => void
  onSelect: (t: MCPTemplate | null) => void
  onBrowseRegistry: () => void
  trigger: React.ReactNode
}

/**
 * Shared popover used by the integrations page header and its empty
 * state. Presents the curated MCP template catalogue plus escape
 * hatches to a blank custom server and the global registry.
 */
export function TemplatePopover({
  open,
  onOpenChange,
  onSelect,
  onBrowseRegistry,
  trigger,
}: TemplatePopoverProps) {
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>{trigger}</PopoverTrigger>
      <PopoverContent className="w-80 p-3" align="end">
        <div className="space-y-2">
          <p className="text-body font-medium">Add from template</p>
          <div className="grid grid-cols-2 gap-2">
            {MCP_TEMPLATES.map((t) => {
              const Icon = TEMPLATE_ICONS[t.icon] ?? Plug
              return (
                <button
                  key={t.name}
                  type="button"
                  className="flex items-center gap-2 rounded-md border border-border px-3 py-2 text-left text-body hover:bg-muted/60 transition-colors"
                  onClick={() => onSelect(t)}
                >
                  <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
                  {t.label}
                </button>
              )
            })}
          </div>
          <div className="flex gap-2">
            <button
              type="button"
              className="flex flex-1 items-center gap-2 rounded-md border border-dashed border-border px-3 py-2 text-body text-muted-foreground hover:bg-muted/60 transition-colors"
              onClick={() => onSelect(null)}
            >
              <Terminal className="h-4 w-4" />
              Custom server
            </button>
            <button
              type="button"
              className="flex flex-1 items-center gap-2 rounded-md border border-dashed border-border px-3 py-2 text-body text-muted-foreground hover:bg-muted/60 transition-colors"
              onClick={onBrowseRegistry}
            >
              <Search className="h-4 w-4" />
              Browse Registry
            </button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  )
}
