"use client"

import { useMemo } from "react"
import { Box, Plug } from "lucide-react"
import { RuntimeConfig } from "../runtime-config"
import { MCPConfigEditor } from "@/components/features/mcp/mcp-config-editor"
import type { WizardState } from "./types"

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
  workspaceId: string
}

export function StepContainer({ state, setState, workspaceId }: Props) {
  return (
    <div className="space-y-4">
      <p className="text-[12px] text-muted-foreground -mt-1">
        All optional. Use the defaults and the crew runs on
        {" "}<code className="text-[11px] font-mono bg-black/30 px-1 py-0.5 rounded">debian:bookworm-slim</code>{" "}
        with no devcontainer features and no MCP servers.
      </p>

      <ImageFeaturesSection state={state} setState={setState} />
      <MCPSection state={state} setState={setState} workspaceId={workspaceId} />
    </div>
  )
}

// =============================================================================
// Image & features — always-visible card wrapping RuntimeConfig.
// =============================================================================

function ImageFeaturesSection({ state, setState }: { state: WizardState; setState: (p: Partial<WizardState>) => void }) {
  const summary = useMemo(() => {
    const baseImage = parseBaseImage(state.devcontainerConfig) || state.runtimeImage || "debian:bookworm-slim"
    const featureCount = countFeatures(state.devcontainerConfig)
    const runtimeCount = countMiseRuntimes(state.miseConfig)
    return { baseImage, featureCount, runtimeCount }
  }, [state.devcontainerConfig, state.runtimeImage, state.miseConfig])

  const isCustomized = summary.baseImage !== "debian:bookworm-slim" || summary.featureCount > 0 || summary.runtimeCount > 0

  return (
    <section className="rounded-lg border border-white/10 bg-card/60 backdrop-blur-sm overflow-hidden">
      <header className="px-3.5 py-3 flex items-center gap-3 border-b border-white/5 bg-white/[0.02]">
        <div className="h-7 w-7 rounded-md bg-blue-500/15 text-blue-300 flex items-center justify-center shrink-0">
          <Box className="h-3.5 w-3.5" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-medium leading-tight">Image &amp; features</div>
          <div className="text-[11px] text-muted-foreground leading-tight truncate mt-0.5 font-mono">
            {summary.baseImage}
            {summary.featureCount > 0 && (
              <span className="ml-2 text-foreground/70">· {summary.featureCount} feature{summary.featureCount === 1 ? "" : "s"}</span>
            )}
            {summary.runtimeCount > 0 && (
              <span className="ml-2 text-foreground/70">· {summary.runtimeCount} runtime{summary.runtimeCount === 1 ? "" : "s"}</span>
            )}
          </div>
        </div>
        {isCustomized && (
          <span className="text-[9px] uppercase tracking-wider px-1.5 py-0.5 rounded-full bg-blue-500/15 text-blue-300 font-semibold shrink-0">
            customized
          </span>
        )}
      </header>
      <div className="px-3.5 py-3">
        <RuntimeConfig
          value={{
            runtimeImage: state.runtimeImage,
            devcontainerConfig: state.devcontainerConfig,
            miseConfig: state.miseConfig,
          }}
          onChange={(v) => setState({
            runtimeImage: v.runtimeImage,
            devcontainerConfig: v.devcontainerConfig,
            miseConfig: v.miseConfig,
          })}
        />
      </div>
    </section>
  )
}

// =============================================================================
// MCP servers — always-visible card wrapping MCPConfigEditor.
// =============================================================================

function MCPSection({ state, setState, workspaceId }: { state: WizardState; setState: (p: Partial<WizardState>) => void; workspaceId: string }) {
  const serverCount = useMemo(() => countMCPServers(state.mcpConfig), [state.mcpConfig])

  return (
    <section className="rounded-lg border border-white/10 bg-card/60 backdrop-blur-sm overflow-hidden">
      <header className="px-3.5 py-3 flex items-center gap-3 border-b border-white/5 bg-white/[0.02]">
        <div className="h-7 w-7 rounded-md bg-violet-500/15 text-violet-300 flex items-center justify-center shrink-0">
          <Plug className="h-3.5 w-3.5" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-medium leading-tight">MCP servers</div>
          <div className="text-[11px] text-muted-foreground leading-tight mt-0.5">
            {serverCount === 0
              ? "No servers configured · agents have no external MCP tools"
              : `${serverCount} server${serverCount === 1 ? "" : "s"} configured`}
          </div>
        </div>
        {serverCount > 0 && (
          <span className="text-[9px] uppercase tracking-wider px-1.5 py-0.5 rounded-full bg-violet-500/15 text-violet-300 font-semibold shrink-0">
            configured
          </span>
        )}
      </header>
      <div className="px-3.5 py-3">
        <MCPConfigEditor
          value={state.mcpConfig}
          onChange={(json) => setState({ mcpConfig: json })}
          workspaceId={workspaceId}
        />
      </div>
    </section>
  )
}

// =============================================================================
// Lightweight parsers — read summary chips without depending on the heavier
// parsers inside RuntimeConfig / MCPConfigEditor (which are component-internal).
// =============================================================================

function parseBaseImage(devcontainerConfig: string): string {
  if (!devcontainerConfig.trim()) return ""
  try {
    const parsed = JSON.parse(devcontainerConfig) as { image?: unknown }
    return typeof parsed.image === "string" ? parsed.image : ""
  } catch {
    return ""
  }
}

function countFeatures(devcontainerConfig: string): number {
  if (!devcontainerConfig.trim()) return 0
  try {
    const parsed = JSON.parse(devcontainerConfig) as { features?: Record<string, unknown> }
    return parsed.features ? Object.keys(parsed.features).length : 0
  } catch {
    return 0
  }
}

function countMiseRuntimes(miseConfig: string): number {
  if (!miseConfig.trim()) return 0
  // mise.toml: count `[tools]` entries. Quick line-based count of `key = "..."`
  // inside a tools block. Don't over-engineer; this is a chip number.
  let inTools = false
  let count = 0
  for (const raw of miseConfig.split(/\r?\n/)) {
    const line = raw.trim()
    if (line.startsWith("[")) {
      inTools = line === "[tools]"
      continue
    }
    if (inTools && /^[\w-]+\s*=/.test(line)) count++
  }
  return count
}

function countMCPServers(mcpConfig: string): number {
  if (!mcpConfig.trim()) return 0
  try {
    const parsed = JSON.parse(mcpConfig) as { mcpServers?: Record<string, unknown> }
    return parsed.mcpServers ? Object.keys(parsed.mcpServers).length : 0
  } catch {
    return 0
  }
}
