"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import {
  Ship,
  Users,
  Bot,
  KeyRound,
  Rocket,
  ArrowRight,
  ArrowLeft,
  Loader2,
  Check,
  SkipForward,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Progress } from "@/components/ui/progress"

const STEPS = [
  { id: "welcome", label: "Welcome", icon: Ship },
  { id: "crew", label: "Create Crew", icon: Users },
  { id: "agent", label: "Add Agent", icon: Bot },
  { id: "credential", label: "API Key", icon: KeyRound },
  { id: "done", label: "Ready!", icon: Rocket },
] as const

const CREW_TEMPLATES = [
  { name: "Development", description: "Software development & DevOps", icon: "💻" },
  { name: "Research", description: "Web research & data analysis", icon: "🔍" },
  { name: "Support", description: "Customer support & helpdesk", icon: "🎧" },
  { name: "Marketing", description: "Content creation & SEO", icon: "📈" },
] as const

export default function OnboardingPage() {
  const router = useRouter()
  const [currentStep, setCurrentStep] = useState(0)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [checkingStatus, setCheckingStatus] = useState(true)

  // Form state
  const [workspaceName, setWorkspaceName] = useState("")
  const [crewName, setCrewName] = useState("")
  const [agentName, setAgentName] = useState("")
  const [cliAdapter, setCliAdapter] = useState("CLAUDE_CODE")
  const [llmProvider, setLlmProvider] = useState("ANTHROPIC")
  const [llmModel, setLlmModel] = useState("")
  const [credentialName, setCredentialName] = useState("")
  const [credentialValue, setCredentialValue] = useState("")

  // Check if onboarding already completed
  useEffect(() => {
    fetch("/api/v1/onboarding/status")
      .then((res) => res.json())
      .then((data) => {
        if (data.completed) {
          router.push("/")
          return
        }
        setCheckingStatus(false)
      })
      .catch(() => setCheckingStatus(false))
  }, [router])

  // Set defaults based on CLI adapter
  useEffect(() => {
    if (cliAdapter === "CLAUDE_CODE") {
      setLlmProvider("ANTHROPIC")
      setCredentialName("ANTHROPIC_API_KEY")
    } else if (cliAdapter === "CODEX_CLI") {
      setLlmProvider("OPENAI")
      setCredentialName("OPENAI_API_KEY")
    } else if (cliAdapter === "GEMINI_CLI") {
      setLlmProvider("GOOGLE")
      setCredentialName("GOOGLE_API_KEY")
    } else {
      setCredentialName("API Key")
    }
  }, [cliAdapter])

  const progress = ((currentStep + 1) / STEPS.length) * 100

  const canGoNext = useCallback((): boolean => {
    const step = STEPS[currentStep].id
    if (step === "crew") return crewName.trim().length >= 2
    if (step === "agent") return agentName.trim().length >= 2
    if (step === "credential") return true // credential is optional
    return true
  }, [currentStep, crewName, agentName])

  function goNext() {
    if (currentStep < STEPS.length - 1) {
      setCurrentStep((s) => s + 1)
      setError(null)
    }
  }

  function goBack() {
    if (currentStep > 0) {
      setCurrentStep((s) => s - 1)
      setError(null)
    }
  }

  async function handleComplete() {
    setSubmitting(true)
    setError(null)

    try {
      const res = await fetch("/api/v1/onboarding/setup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          workspace_name: workspaceName || undefined,
          crew_name: crewName,
          agent_name: agentName,
          cli_adapter: cliAdapter,
          llm_provider: llmProvider,
          llm_model: llmModel || undefined,
          credential_name: credentialName || undefined,
          credential_value: credentialValue || undefined,
        }),
      })

      if (!res.ok) {
        const data = await res.json()
        setError(data.error || "Something went wrong")
        setSubmitting(false)
        return
      }

      const data = await res.json()
      router.push(`/agents/${data.agent_id}/chat`)
    } catch {
      setError("Network error. Please try again.")
      setSubmitting(false)
    }
  }

  async function handleSkip() {
    try {
      await fetch("/api/v1/onboarding/complete", { method: "POST" })
    } catch {
      // ignore
    }
    router.push("/")
  }

  if (checkingStatus) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    )
  }

  const stepId = STEPS[currentStep].id

  return (
    <div className="flex min-h-screen flex-col items-center justify-center bg-background p-4">
      <div className="w-full max-w-lg space-y-6">
        {/* Header */}
        <div className="text-center space-y-2">
          <div className="flex justify-center">
            <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-primary text-primary-foreground">
              <Ship className="h-6 w-6" />
            </div>
          </div>
          <h1 className="text-xl font-semibold">Set up Crewship</h1>
        </div>

        {/* Progress */}
        <div className="space-y-2">
          <Progress value={progress} className="h-1.5" />
          <div className="flex justify-between">
            {STEPS.map((step, i) => {
              const Icon = step.icon
              const isActive = i === currentStep
              const isDone = i < currentStep
              return (
                <div key={step.id} className="flex flex-col items-center gap-1">
                  <div
                    className={`flex h-8 w-8 items-center justify-center rounded-full text-xs font-medium transition-colors ${
                      isDone
                        ? "bg-primary text-primary-foreground"
                        : isActive
                          ? "bg-primary/10 text-primary border-2 border-primary"
                          : "bg-muted text-muted-foreground"
                    }`}
                  >
                    {isDone ? <Check className="h-4 w-4" /> : <Icon className="h-3.5 w-3.5" />}
                  </div>
                  <span
                    className={`text-[10px] ${
                      isActive ? "text-foreground font-medium" : "text-muted-foreground"
                    }`}
                  >
                    {step.label}
                  </span>
                </div>
              )
            })}
          </div>
        </div>

        {/* Step content */}
        <Card>
          <CardContent className="pt-6">
            {stepId === "welcome" && (
              <StepWelcome
                workspaceName={workspaceName}
                onWorkspaceNameChange={setWorkspaceName}
              />
            )}
            {stepId === "crew" && (
              <StepCrew crewName={crewName} onCrewNameChange={setCrewName} />
            )}
            {stepId === "agent" && (
              <StepAgent
                agentName={agentName}
                onAgentNameChange={setAgentName}
                cliAdapter={cliAdapter}
                onCliAdapterChange={setCliAdapter}
                llmModel={llmModel}
                onLlmModelChange={setLlmModel}
              />
            )}
            {stepId === "credential" && (
              <StepCredential
                credentialName={credentialName}
                credentialValue={credentialValue}
                onCredentialValueChange={setCredentialValue}
                llmProvider={llmProvider}
              />
            )}
            {stepId === "done" && <StepDone />}

            {error && <p className="text-sm text-destructive mt-4">{error}</p>}
          </CardContent>
        </Card>

        {/* Navigation */}
        <div className="flex items-center justify-between">
          <div>
            {currentStep > 0 && currentStep < STEPS.length - 1 && (
              <Button variant="ghost" size="sm" onClick={goBack}>
                <ArrowLeft className="mr-2 h-4 w-4" />
                Back
              </Button>
            )}
          </div>

          <div className="flex items-center gap-2">
            {currentStep < STEPS.length - 1 && (
              <Button
                variant="ghost"
                size="sm"
                onClick={handleSkip}
                className="text-muted-foreground"
              >
                <SkipForward className="mr-1 h-3.5 w-3.5" />
                Skip setup
              </Button>
            )}

            {stepId === "done" ? (
              <Button onClick={handleComplete} disabled={submitting}>
                {submitting ? (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                ) : (
                  <Rocket className="mr-2 h-4 w-4" />
                )}
                Launch & Start Chatting
              </Button>
            ) : (
              <Button onClick={goNext} disabled={!canGoNext()}>
                Continue
                <ArrowRight className="ml-2 h-4 w-4" />
              </Button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

function StepWelcome({
  workspaceName,
  onWorkspaceNameChange,
}: {
  workspaceName: string
  onWorkspaceNameChange: (v: string) => void
}) {
  return (
    <div className="space-y-4">
      <div className="text-center space-y-2">
        <h2 className="text-lg font-semibold">Welcome to Crewship!</h2>
        <p className="text-sm text-muted-foreground">
          Let&apos;s set up your workspace and get your first AI agent running in under a minute.
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="workspace_name">Workspace Name (optional)</Label>
        <Input
          id="workspace_name"
          value={workspaceName}
          onChange={(e) => onWorkspaceNameChange(e.target.value)}
          placeholder="e.g. My Company"
        />
        <p className="text-xs text-muted-foreground">
          A workspace was auto-created for you. Rename it if you like.
        </p>
      </div>
    </div>
  )
}

function StepCrew({
  crewName,
  onCrewNameChange,
}: {
  crewName: string
  onCrewNameChange: (v: string) => void
}) {
  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h2 className="text-lg font-semibold">Create your first crew</h2>
        <p className="text-sm text-muted-foreground">
          A crew is a team of AI agents. Pick a template or create your own.
        </p>
      </div>
      <div className="grid grid-cols-2 gap-2">
        {CREW_TEMPLATES.map((t) => (
          <button
            key={t.name}
            type="button"
            onClick={() => onCrewNameChange(t.name)}
            className={`flex items-start gap-2 rounded-lg border p-3 text-left transition-colors hover:bg-accent ${
              crewName === t.name ? "border-primary bg-primary/5" : "border-border"
            }`}
          >
            <span className="text-lg">{t.icon}</span>
            <div>
              <div className="text-sm font-medium">{t.name}</div>
              <div className="text-xs text-muted-foreground">{t.description}</div>
            </div>
          </button>
        ))}
      </div>
      <div className="space-y-2">
        <Label htmlFor="crew_name">Or enter a custom name</Label>
        <Input
          id="crew_name"
          value={crewName}
          onChange={(e) => onCrewNameChange(e.target.value)}
          placeholder="e.g. My Dev Team"
        />
      </div>
    </div>
  )
}

function StepAgent({
  agentName,
  onAgentNameChange,
  cliAdapter,
  onCliAdapterChange,
  llmModel,
  onLlmModelChange,
}: {
  agentName: string
  onAgentNameChange: (v: string) => void
  cliAdapter: string
  onCliAdapterChange: (v: string) => void
  llmModel: string
  onLlmModelChange: (v: string) => void
}) {
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
      <div className="grid grid-cols-2 gap-4">
        <div className="space-y-2">
          <Label htmlFor="cli_adapter">CLI Adapter</Label>
          <Select value={cliAdapter} onValueChange={onCliAdapterChange}>
            <SelectTrigger id="cli_adapter" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="CLAUDE_CODE">Claude Code</SelectItem>
              <SelectItem value="OPENCODE">OpenCode</SelectItem>
              <SelectItem value="CODEX_CLI">Codex CLI</SelectItem>
              <SelectItem value="GEMINI_CLI">Gemini CLI</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <Label htmlFor="llm_model">LLM Model</Label>
          <Input
            id="llm_model"
            value={llmModel}
            onChange={(e) => onLlmModelChange(e.target.value)}
            placeholder="e.g. claude-sonnet-4-20250514"
            className="font-mono text-xs"
          />
        </div>
      </div>
    </div>
  )
}

function StepCredential({
  credentialName,
  credentialValue,
  onCredentialValueChange,
  llmProvider,
}: {
  credentialName: string
  credentialValue: string
  onCredentialValueChange: (v: string) => void
  llmProvider: string
}) {
  const providerLabels: Record<string, string> = {
    ANTHROPIC: "Anthropic",
    OPENAI: "OpenAI",
    GOOGLE: "Google AI",
  }
  const providerLabel = providerLabels[llmProvider] || llmProvider

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
          placeholder={`sk-ant-...`}
        />
      </div>
    </div>
  )
}

function StepDone() {
  return (
    <div className="text-center space-y-4 py-4">
      <div className="flex justify-center">
        <div className="flex h-16 w-16 items-center justify-center rounded-full bg-primary/10">
          <Rocket className="h-8 w-8 text-primary" />
        </div>
      </div>
      <div className="space-y-2">
        <h2 className="text-lg font-semibold">You&apos;re all set!</h2>
        <p className="text-sm text-muted-foreground">
          Your workspace, crew, and agent are ready. Click the button below to start your first
          chat with your AI agent.
        </p>
      </div>
    </div>
  )
}
