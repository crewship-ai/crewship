"use client"

import { Rocket } from "lucide-react"

export function StepDone() {
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
