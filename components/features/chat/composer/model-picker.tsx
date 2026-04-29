"use client"

import { useEffect, useState } from "react"

import { ModelSelector, type ModelOption } from "@/components/ai-elements/model-selector"
import { useComposerStore } from "@/stores/composer-store"

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
    fetch("/api/v1/llm/models", { credentials: "include", signal: ac.signal })
      .then((r) => (r.ok ? r.json() : null))
      .then((data: { models?: ModelOption[] } | null) => {
        if (data?.models?.length) setModels(data.models)
      })
      .catch(() => {})
    return () => ac.abort()
  }, [])

  return (
    <ModelSelector
      models={models}
      value={modelId ?? models[0]?.id}
      onModelChange={setModel}
    />
  )
}
