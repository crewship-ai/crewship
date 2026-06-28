"use client"

import { useEffect, useState } from "react"

import { ModelSelector, type ModelOption } from "@/components/ai-elements/model-selector"
import { useComposerStore } from "@/stores/composer-store"
import { apiFetch } from "@/lib/api-fetch"

const FALLBACK_MODELS: ModelOption[] = [
  {
    id: "claude-opus-4-7",
    label: "Opus 4.7",
    provider: "Anthropic",
    description: "Most capable, best for complex analysis",
    badge: "Pro",
  },
  {
    id: "claude-sonnet-4-6",
    label: "Sonnet 4.6",
    provider: "Anthropic",
    description: "Balanced speed and capability",
  },
  {
    id: "claude-haiku-4-5",
    label: "Haiku 4.5",
    provider: "Anthropic",
    description: "Fast, lightweight",
  },
  {
    id: "gpt-4o",
    label: "GPT-4o",
    provider: "OpenAI",
    description: "Multimodal flagship",
  },
  {
    id: "llama3.1:70b",
    label: "Llama 3.1 70B",
    provider: "Ollama",
    description: "Local model",
  },
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
