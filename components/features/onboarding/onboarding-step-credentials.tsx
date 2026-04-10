"use client"

import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export interface StepCredentialProps {
  credentialName: string
  credentialValue: string
  onCredentialValueChange: (v: string) => void
  llmProvider: string
}

export function StepCredential({
  credentialName,
  credentialValue,
  onCredentialValueChange,
  llmProvider,
}: StepCredentialProps) {
  const providerLabels: Record<string, string> = {
    ANTHROPIC: "Anthropic",
    OPENAI: "OpenAI",
    GOOGLE: "Google AI",
  }
  const providerLabel = providerLabels[llmProvider] || llmProvider

  const providerPlaceholders: Record<string, string> = {
    ANTHROPIC: "sk-ant-...",
    OPENAI: "sk-...",
    GOOGLE: "AIza...",
  }
  const placeholder = providerPlaceholders[llmProvider] || "Enter API key"

  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h2 className="text-lg font-semibold">Add your API key</h2>
        <p className="text-sm text-muted-foreground">
          Your {providerLabel} API key will be encrypted (AES-256-GCM) and injected into the agent
          container as an environment variable. You can skip this and add it later.
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="credential_name">Environment Variable</Label>
        <Input
          id="credential_name"
          value={credentialName}
          readOnly
          className="font-mono text-sm bg-muted"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="credential_value">API Key</Label>
        <Input
          id="credential_value"
          type="password"
          value={credentialValue}
          onChange={(e) => onCredentialValueChange(e.target.value)}
          placeholder={placeholder}
        />
      </div>
    </div>
  )
}
