"use client"

import { useState } from "react"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { CLI_ADAPTERS, CLI_ADAPTER_KEYS } from "@/lib/cli-adapters"

export interface StepAgentProps {
  agentName: string
  onAgentNameChange: (v: string) => void
  cliAdapter: string
  onCliAdapterChange: (v: string) => void
  llmModel: string
  onLlmModelChange: (v: string) => void
}

export function StepAgent({
  agentName,
  onAgentNameChange,
  cliAdapter,
  onCliAdapterChange,
  llmModel,
  onLlmModelChange,
}: StepAgentProps) {
  const adapterCfg = CLI_ADAPTERS[cliAdapter]
  const models = adapterCfg?.models ?? []
  const isCustomModel = llmModel !== "" && !models.some((m) => m.value === llmModel)
  const [showCustom, setShowCustom] = useState(isCustomModel)

  function handleAdapterChange(key: string) {
    onCliAdapterChange(key)
    const cfg = CLI_ADAPTERS[key]
    if (cfg) onLlmModelChange(cfg.defaultModel)
    setShowCustom(false)
  }

  function handleModelSelect(value: string) {
    if (value === "__custom__") {
      setShowCustom(true)
      onLlmModelChange("")
    } else {
      setShowCustom(false)
      onLlmModelChange(value)
    }
  }

  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h2 className="text-lg font-semibold">Add your first agent</h2>
        <p className="text-sm text-muted-foreground">
          An agent is an AI virtual employee that runs in an isolated container.
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="agent_name">Agent Name *</Label>
        <Input
          id="agent_name"
          value={agentName}
          onChange={(e) => onAgentNameChange(e.target.value)}
          placeholder="e.g. Claude — Developer"
        />
      </div>
      <div className="space-y-2">
        <Label>CLI Adapter</Label>
        <div className="grid grid-cols-2 gap-2">
          {CLI_ADAPTER_KEYS.map((key) => {
            const cfg = CLI_ADAPTERS[key]
            const Icon = cfg.icon
            const isActive = cliAdapter === key
            return (
              <button
                key={key}
                type="button"
                aria-pressed={isActive}
                onClick={() => handleAdapterChange(key)}
                className={`flex items-center gap-3 rounded-lg border p-3 text-left transition-colors ${
                  isActive ? "border-primary bg-primary/5" : "border-border hover:bg-muted"
                }`}
              >
                <Icon className={`h-5 w-5 shrink-0 ${isActive ? "text-primary" : "text-muted-foreground"}`} />
                <div className="min-w-0">
                  <div className="text-sm font-medium">{cfg.label}</div>
                  <div className="text-[10px] text-muted-foreground">{cfg.description}</div>
                </div>
              </button>
            )
          })}
        </div>
      </div>
      <div className="space-y-2">
        <Label>Model</Label>
        {showCustom ? (
          <div className="flex gap-2">
            <Input
              value={llmModel}
              onChange={(e) => onLlmModelChange(e.target.value)}
              placeholder="Enter model name"
              className="font-mono text-xs"
            />
            <Button type="button" variant="outline" size="sm" onClick={() => {
              setShowCustom(false)
              if (adapterCfg) onLlmModelChange(adapterCfg.defaultModel)
            }}>
              Back
            </Button>
          </div>
        ) : (
          <Select value={llmModel} onValueChange={handleModelSelect}>
            <SelectTrigger className="w-full font-mono text-xs">
              <SelectValue placeholder="Select model" />
            </SelectTrigger>
            <SelectContent>
              {models.map((m) => (
                <SelectItem key={m.value} value={m.value} className="font-mono text-xs">
                  {m.label}
                </SelectItem>
              ))}
              <SelectItem value="__custom__" className="text-muted-foreground">
                Custom...
              </SelectItem>
            </SelectContent>
          </Select>
        )}
      </div>
    </div>
  )
}
