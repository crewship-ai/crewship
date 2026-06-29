import type { IconType } from "react-icons"
import {
  SiAlpinelinux,
  SiDebian,
  SiGo,
  SiNodedotjs,
  SiOpenjdk,
  SiPython,
  SiRust,
  SiUbuntu,
} from "react-icons/si"
import { Boxes } from "lucide-react"

// Pure data + serialization helpers for the runtime configuration UI.
// CategoryFilter / BASE_IMAGES / CATEGORY_LABELS describe the catalog;
// parse/build helpers are the only place that knows the
// devcontainer.json + mise.toml-as-JSON shapes. Extracted from
// runtime-config.tsx for readability.

type CategoryFilter = "all" | "languages" | "tools" | "cloud" | "databases"


const CATEGORY_LABELS: Record<string, string> = {
  languages: "Languages",
  tools: "Tools",
  cloud: "Cloud",
  databases: "Databases",
}

const CATEGORY_FILTERS: CategoryFilter[] = ["all", "languages", "tools", "cloud", "databases"]


const BASE_IMAGES: Array<{
  value: string
  label: string
  description: string
  icon: IconType
  /** BRAND_COLORS key — used to tint the card icon with the official
   *  brand color. Stored explicitly because img.value is a full
   *  registry path (mcr.microsoft.com/devcontainers/…) and parsing
   *  it for a brand key is brittle. */
  colorKey?: string
  recommended?: boolean
}> = [
  {
    value: "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm",
    label: "Node 22 (Debian) — recommended",
    description: "Node.js 22 + npm + git + curl. Best for Claude Code and most AI workloads.",
    icon: SiNodedotjs,
    colorKey: "node",
    recommended: true,
  },
  {
    value: "mcr.microsoft.com/devcontainers/base:bookworm",
    label: "Debian 12 (bookworm)",
    description: "Minimal Debian with common utilities. Add features/runtimes as needed.",
    icon: SiDebian,
    colorKey: "debian",
  },
  {
    value: "mcr.microsoft.com/devcontainers/base:ubuntu-24.04",
    label: "Ubuntu 24.04",
    description: "Ubuntu LTS with common utilities.",
    icon: SiUbuntu,
    colorKey: "ubuntu",
  },
  {
    value: "mcr.microsoft.com/devcontainers/python:3.12-bookworm",
    label: "Python 3.12 (Debian)",
    description: "Python 3.12 + pip + venv pre-installed on Debian.",
    icon: SiPython,
    colorKey: "python",
  },
  {
    value: "mcr.microsoft.com/devcontainers/go:1.23-bookworm",
    label: "Go 1.23 (Debian)",
    description:
      "Go 1.23 toolchain on Debian. KNOWN ISSUE: ships a stale yarn apt repo whose GPG key breaks provisioning. Prefer Debian + the Go feature for now.",
    icon: SiGo,
    colorKey: "go",
  },
  {
    value: "mcr.microsoft.com/devcontainers/rust:bookworm",
    label: "Rust (Debian)",
    description: "Rust stable + cargo on Debian.",
    icon: SiRust,
    colorKey: "rust",
  },
  {
    value: "mcr.microsoft.com/devcontainers/java:21-bookworm",
    label: "Java 21 (OpenJDK)",
    description: "OpenJDK 21 + Maven/Gradle on Debian.",
    icon: SiOpenjdk,
    colorKey: "java",
  },
  {
    value: "mcr.microsoft.com/devcontainers/universal:2",
    label: "Universal (kitchen sink)",
    description:
      "Node + Python + Go + Rust + Java + Ruby pre-installed. ~8GB. KNOWN ISSUE: ships a stale yarn apt repo whose GPG key breaks provisioning.",
    icon: Boxes,
    // No colorKey — Boxes is a generic lucide icon, not a brand mark.
    // Falls through to muted-foreground.
  },
  {
    value: "mcr.microsoft.com/devcontainers/base:alpine-3.20",
    label: "Alpine 3.20 (experimental)",
    description: "Tiny (~7MB). WARNING: musl incompatible with Claude Code.",
    icon: SiAlpinelinux,
    colorKey: "alpine",
  },
]

// ---- Helpers --------------------------------------------------------------


// Per containers.dev features spec, option values are primitives
// (string | boolean | number); the outer map is feature-ref -> options.
type FeatureOptions = Record<string, string | number | boolean>
type FeatureMap = Record<string, FeatureOptions>

function parseDevcontainerConfig(jsonStr: string): {
  image: string
  features: FeatureMap
} {
  if (!jsonStr) return { image: "debian:bookworm-slim", features: {} }
  try {
    const parsed = JSON.parse(jsonStr)
    return {
      image: parsed.image || "debian:bookworm-slim",
      features: parsed.features || {},
    }
  } catch {
    return { image: "debian:bookworm-slim", features: {} }
  }
}

function parseMiseConfig(jsonStr: string): Record<string, string> {
  if (!jsonStr) return {}
  try {
    const parsed = JSON.parse(jsonStr)
    return parsed.tools || {}
  } catch {
    return {}
  }
}


function buildDevcontainerJSON(
  image: string,
  features: FeatureMap
): string {
  const config: Record<string, unknown> = { image }
  if (Object.keys(features).length > 0) {
    config.features = features
  }
  return JSON.stringify(config, null, 2)
}

function buildMiseJSON(tools: Record<string, string>): string {
  if (Object.keys(tools).length === 0) return ""
  return JSON.stringify({ tools }, null, 2)
}

// ---- Component ------------------------------------------------------------


export type { CategoryFilter, FeatureOptions, FeatureMap }
export {
  CATEGORY_LABELS,
  CATEGORY_FILTERS,
  BASE_IMAGES,
  parseDevcontainerConfig,
  parseMiseConfig,
  buildDevcontainerJSON,
  buildMiseJSON,
}
