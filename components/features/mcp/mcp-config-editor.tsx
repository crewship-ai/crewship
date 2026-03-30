"use client"

import { useState, useCallback, useEffect } from "react"
import { Plug, Plus, Terminal } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { cn } from "@/lib/utils"
import type { ServerEntry, MCPTemplate, MCPConfigEditorProps } from "./types"
import { parseConfig, serializeConfig, emptyEntry, entryFromTemplate } from "./lib/config-parser"
import { MCP_TEMPLATES, TEMPLATE_ICONS } from "./templates"
import { useCredentials } from "./hooks/use-credentials"
import { ServerCard } from "./components/server-card"

// ---------------------------------------------------------------------------
// MCPConfigEditor — main orchestrator
// ---------------------------------------------------------------------------

export function MCPConfigEditor({
  value,
  onChange,
  readOnly = false,
  label,
  workspaceId,
}: MCPConfigEditorProps) {
  const [entries, setEntries] = useState<ServerEntry[]>(() => parseConfig(value))
  const [templatePopoverOpen, setTemplatePopoverOpen] = useState(false)
  const { credentials, loading: credLoading, fetchCredentials, addCredential } = useCredentials(
    readOnly ? undefined : workspaceId,
  )

  // Sync from parent when value changes externally
  useEffect(() => {
    const parsed = parseConfig(value)
    if (serializeConfig(parsed) !== serializeConfig(entries)) {
      setEntries(parsed)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value])

  const emit = useCallback(
    (updated: ServerEntry[]) => {
      setEntries(updated)
      onChange(serializeConfig(updated))
    },
    [onChange],
  )

  function updateEntry(index: number, patch: Partial<ServerEntry>) {
    const updated = entries.map((e, i) => (i === index ? { ...e, ...patch } : e))
    emit(updated)
  }

  function removeEntry(index: number) {
    emit(entries.filter((_, i) => i !== index))
  }

  function addEntry() {
    emit([...entries, emptyEntry()])
  }

  function addFromTemplate(template: MCPTemplate) {
    emit([...entries, entryFromTemplate(template)])
    setTemplatePopoverOpen(false)
  }

  // -- Key-value pair helpers -----------------------------------------------

  function addEnvVar(index: number) {
    const updated = [...entries]
    updated[index] = { ...updated[index], env: [...updated[index].env, { key: "", value: "" }] }
    emit(updated)
  }

  function updateEnvVar(serverIdx: number, envIdx: number, field: "key" | "value", val: string) {
    const updated = [...entries]
    const env = [...updated[serverIdx].env]
    env[envIdx] = { ...env[envIdx], [field]: val }
    updated[serverIdx] = { ...updated[serverIdx], env }
    emit(updated)
  }

  function removeEnvVar(serverIdx: number, envIdx: number) {
    const updated = [...entries]
    updated[serverIdx] = {
      ...updated[serverIdx],
      env: updated[serverIdx].env.filter((_, i) => i !== envIdx),
    }
    emit(updated)
  }

  function addHeader(index: number) {
    const updated = [...entries]
    updated[index] = { ...updated[index], headers: [...updated[index].headers, { key: "", value: "" }] }
    emit(updated)
  }

  function updateHeader(serverIdx: number, hdrIdx: number, field: "key" | "value", val: string) {
    const updated = [...entries]
    const headers = [...updated[serverIdx].headers]
    headers[hdrIdx] = { ...headers[hdrIdx], [field]: val }
    updated[serverIdx] = { ...updated[serverIdx], headers }
    emit(updated)
  }

  function removeHeader(serverIdx: number, hdrIdx: number) {
    const updated = [...entries]
    updated[serverIdx] = {
      ...updated[serverIdx],
      headers: updated[serverIdx].headers.filter((_, i) => i !== hdrIdx),
    }
    emit(updated)
  }

  const hasCredentialSupport = Boolean(workspaceId) && !readOnly

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <div className="space-y-4">
      {label && (
        <div className="flex items-center gap-2">
          <Plug className="h-4 w-4 text-muted-foreground" />
          <span className="text-sm font-medium">{label}</span>
        </div>
      )}

      {entries.length === 0 && (
        <p className="text-sm text-muted-foreground">
          No MCP servers configured.
        </p>
      )}

      {entries.map((entry, idx) => (
        <ServerCard
          key={entry._key}
          entry={entry}
          index={idx}
          readOnly={readOnly}
          credentials={credentials}
          credLoading={credLoading}
          hasCredentialSupport={hasCredentialSupport}
          workspaceId={workspaceId}
          onFetchCredentials={fetchCredentials}
          onAddCredential={addCredential}
          onUpdate={updateEntry}
          onRemove={removeEntry}
          onAddEnvVar={addEnvVar}
          onUpdateEnvVar={updateEnvVar}
          onRemoveEnvVar={removeEnvVar}
          onAddHeader={addHeader}
          onUpdateHeader={updateHeader}
          onRemoveHeader={removeHeader}
        />
      ))}

      {!readOnly && (
        <Popover open={templatePopoverOpen} onOpenChange={setTemplatePopoverOpen}>
          <PopoverTrigger asChild>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="gap-1.5"
            >
              <Plus className="h-3.5 w-3.5" />
              Add MCP Server
            </Button>
          </PopoverTrigger>
          <PopoverContent align="start" className="w-80 p-3">
            <div className="space-y-3">
              <div className="text-xs font-medium text-muted-foreground">Popular servers</div>
              <div className="grid grid-cols-2 gap-1.5">
                {MCP_TEMPLATES.map((tpl) => {
                  const Icon = TEMPLATE_ICONS[tpl.icon] ?? Terminal
                  return (
                    <button
                      key={tpl.name}
                      type="button"
                      className={cn(
                        "flex items-center gap-2 px-2.5 py-2 text-xs rounded-md",
                        "border border-transparent hover:border-border hover:bg-accent/50",
                        "transition-colors text-left",
                      )}
                      onClick={() => addFromTemplate(tpl)}
                    >
                      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      <span className="truncate">{tpl.label}</span>
                    </button>
                  )
                })}
              </div>
              <div className="border-t pt-2">
                <button
                  type="button"
                  className={cn(
                    "flex items-center gap-2 w-full px-2.5 py-2 text-xs rounded-md",
                    "hover:bg-accent/50 transition-colors text-left",
                  )}
                  onClick={() => {
                    addEntry()
                    setTemplatePopoverOpen(false)
                  }}
                >
                  <Plus className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  <span>Custom server</span>
                </button>
              </div>
            </div>
          </PopoverContent>
        </Popover>
      )}
    </div>
  )
}
