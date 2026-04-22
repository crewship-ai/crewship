"use client"

import { Network, Bot, Key, ArrowRight } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import Link from "next/link"

interface SetupNudgeProps {
  crewCount: number
  agentCount: number
  credentialCount: number
}

export function SetupNudge({ crewCount, agentCount, credentialCount }: SetupNudgeProps) {
  const steps = [
    {
      done: crewCount > 0,
      icon: Network,
      label: "Create a crew",
      description: "Group your agents by department or function",
      href: "/crews/crews/new",
    },
    {
      done: agentCount > 0,
      icon: Bot,
      label: "Add an agent",
      description: "Create your first AI virtual employee",
      href: "/crews/agents/new",
    },
    {
      done: credentialCount > 0,
      icon: Key,
      label: "Add credentials",
      description: "Add API keys so agents can use LLM providers",
      href: "/credentials",
    },
  ]

  const allDone = steps.every((s) => s.done)
  if (allDone) return null

  const completedCount = steps.filter((s) => s.done).length

  return (
    <Card className="border-primary/20 bg-primary/[0.02]">
      <CardContent className="p-4 sm:p-5">
        <div className="flex items-center justify-between mb-3">
          <div>
            <h3 className="text-body font-semibold">Get started with Crewship</h3>
            <p className="text-label text-muted-foreground mt-0.5">
              {completedCount} of {steps.length} steps completed
            </p>
          </div>
          <div className="flex gap-1" role="group" aria-label={`${completedCount} of ${steps.length} steps completed`}>
            {steps.map((step, i) => (
              <div
                key={i}
                aria-hidden="true"
                className={`h-1.5 w-6 rounded-full ${
                  step.done ? "bg-primary" : "bg-muted"
                }`}
              />
            ))}
          </div>
        </div>

        <div className="space-y-2">
          {steps
            .filter((s) => !s.done)
            .map((step) => (
              <Link
                key={step.label}
                href={step.href}
                className="flex items-center gap-3 p-2.5 rounded-lg border bg-background hover:bg-accent transition-colors group"
              >
                <div className="flex h-8 w-8 items-center justify-center rounded-md bg-muted shrink-0">
                  <step.icon className="h-4 w-4 text-muted-foreground" />
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-body font-medium">{step.label}</p>
                  <p className="text-label text-muted-foreground">{step.description}</p>
                </div>
                <ArrowRight className="h-4 w-4 text-muted-foreground group-hover:text-foreground transition-colors shrink-0" />
              </Link>
            ))}
        </div>
      </CardContent>
    </Card>
  )
}
