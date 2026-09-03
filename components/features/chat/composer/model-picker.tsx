"use client"

import { useEffect, useState } from "react"

import { ModelSelector, type ModelOption } from "@/components/ai-elements/model-selector"
import { useComposerStore } from "@/stores/composer-store"
import { apiFetch } from "@/lib/api-fetch"
import { providerDefaultModel, providerModels } from "@/lib/model-catalog"

// What the menu shows before — or instead of — the server's answer: the
// catalog's Anthropic rows minus legacy ones, the OpenAI default, and the
// first local suggestion. It used to be a hand-typed list of six that named
// gpt-4o; now it cannot disagree with the pickers elsewhere in the app.
const FALLBACK_MODELS: ModelOption[] = [
  ...providerModels("anthropic")
    .filter((m) => m.category !== "legacy")
    .map((m) => ({
      id: m.id,
      label: m.label.replace(/^Claude /, ""),
      provider: "Anthropic",
      badge: m.role === "default" ? "Default" : m.role === "top" ? "Pro" : undefined,
    })),
  ...providerModels("openai")
    .filter((m) => m.id === providerDefaultModel("openai"))
    .map((m) => ({ id: m.id, label: m.label, provider: "OpenAI" })),
  ...providerModels("ollama")
    .slice(0, 1)
    .map((m) => ({ id: m.id, label: m.label, provider: "Ollama", description: "Local model" })),
]

export function ModelPicker() {
  const { modelId, setModel } = useComposerStore()
  const [models, setModels] = useState<ModelOption[]>(FALLBACK_MODELS)

  useEffect(() => {
    const ac = new AbortController()
    apiFetch("/api/v1/llm/models", { signal: ac.signal })
      .then((r) => (r.ok ? r.json() : null))
      .then((data: { models?: ModelOption[] } | null) => {
        if (!data?.models?.length) return
        setModels(data.models)
        // Keep the composer store in sync with the loaded list. If the
        // persisted modelId is null or no longer offered by the server,
        // fall back to the first option so the visible selection in
        // ModelSelector matches what `submit` will actually send.
        if (!modelId || !data.models.some((m) => m.id === modelId)) {
          setModel(data.models[0].id)
        }
      })
      .catch(() => {})
    return () => ac.abort()
  }, [modelId, setModel])

  return (
    <ModelSelector
      models={models}
      value={modelId ?? models[0]?.id}
      onModelChange={setModel}
    />
  )
}
