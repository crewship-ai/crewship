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
  Container,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Progress } from "@/components/ui/progress"
import { CLI_ADAPTERS } from "@/lib/cli-adapters"
import { StepWelcome } from "@/components/features/onboarding/onboarding-step-welcome"
import { StepSystemCheck } from "@/components/features/onboarding/onboarding-step-system"
import { StepCrew } from "@/components/features/onboarding/onboarding-step-crew"
import { StepAgent } from "@/components/features/onboarding/onboarding-step-agent"
import { StepCredential } from "@/components/features/onboarding/onboarding-step-credentials"
import { StepDone } from "@/components/features/onboarding/onboarding-step-complete"

const STEPS = [
  { id: "welcome", label: "Welcome", icon: Ship },
  { id: "system", label: "System", icon: Container },
  { id: "crew", label: "Create Crew", icon: Users },
  { id: "agent", label: "Add Agent", icon: Bot },
  { id: "credential", label: "API Key", icon: KeyRound },
  { id: "done", label: "Ready!", icon: Rocket },
] as const

/** Onboarding wizard page -- guides new users through crew, agent, and credential setup. */
export default function OnboardingPage() {
  const router = useRouter()
  const [currentStep, setCurrentStep] = useState(0)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [checkingStatus, setCheckingStatus] = useState(true)

  // Runtime detection state
  const [runtimeAvailable, setRuntimeAvailable] = useState<boolean | null>(null)
  const [runtimeInfo, setRuntimeInfo] = useState<{ runtime: string; version: string } | null>(null)
  const [runtimeChecking, setRuntimeChecking] = useState(false)
  const [installLinks, setInstallLinks] = useState<Record<string, string>>({})

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
      .then((res) => {
        if (!res.ok) return { completed: false }
        return res.json()
      })
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
    const cfg = CLI_ADAPTERS[cliAdapter]
    if (cfg) {
      setLlmProvider(cfg.provider)
      setCredentialName(cfg.envVar)
    }
  }, [cliAdapter])

  const progress = ((currentStep + 1) / STEPS.length) * 100

  const checkRuntime = useCallback(async () => {
    setRuntimeChecking(true)
    try {
      const res = await fetch("/api/v1/system/runtime")
      if (!res.ok) {
        setRuntimeAvailable(false)
        return
      }
      const data = await res.json()
      setRuntimeAvailable(data.available)
      if (data.available) {
        setRuntimeInfo({ runtime: data.runtime, version: data.version })
      } else {
        setInstallLinks(data.install_links ?? {})
      }
    } catch {
      setRuntimeAvailable(false)
    } finally {
      setRuntimeChecking(false)
    }
  }, [])

  const canGoNext = useCallback((): boolean => {
    const step = STEPS[currentStep].id
    if (step === "system") return runtimeAvailable !== null // checked at least once
    if (step === "crew") return crewName.trim().length >= 2
    if (step === "agent") return agentName.trim().length >= 2
    if (step === "credential") return true // credential is optional
    return true
  }, [currentStep, crewName, agentName, runtimeAvailable])

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
            {stepId === "system" && (
              <StepSystemCheck
                available={runtimeAvailable}
                info={runtimeInfo}
                checking={runtimeChecking}
                installLinks={installLinks}
                onCheck={checkRuntime}
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
