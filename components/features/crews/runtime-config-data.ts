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
    description: "Go 1.23 toolchain on Debian.",
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
    description: "Node + Python + Go + Rust + Java + Ruby pre-installed. ~8GB.",
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

// ---- Container-privilege escape hatches (#1380) ---------------------------
//
// The devcontainer_config JSON also carries the highest-blast-radius runtime
// knobs: privileged, capAdd, extra mounts, the docker --init reaper, extra
// containerEnv and the start hook (postStartCommand). These used to be
// editable only as a raw JSON blob; the structured controls in
// runtime-security-config.tsx read/write the exact same top-level keys the
// backend already understands (see internal/devcontainer + internal/provider
// CrewConfig). Field names match the provider's Docker HostConfig mapping.

/** One extra bind/volume mount. `readonly` maps to a read-only mount. */
export interface MountEntry {
  source: string
  target: string
  /** "bind" (default) or "volume". */
  type?: string
  readonly?: boolean
}

/** Everything the structured controls plus the raw escape hatch need to
 *  reconstruct a devcontainer_config losslessly. */
export interface ParsedDevcontainer {
  image: string
  features: FeatureMap
  containerEnv: Record<string, string>
  privileged: boolean
  init: boolean
  capAdd: string[]
  mounts: MountEntry[]
  /** postStartCommand flattened to newline-joined text for the editor. */
  postStartCommand: string
  /** Top-level keys the structured UI does not model, preserved verbatim so
   *  round-tripping never drops an operator's advanced config. */
  passthrough: Record<string, unknown>
}

/** Extra fields buildDevcontainerJSON serializes alongside image + features. */
export interface DevcontainerExtras {
  containerEnv?: Record<string, string>
  privileged?: boolean
  init?: boolean
  capAdd?: string[]
  mounts?: MountEntry[]
  postStartCommand?: string
  passthrough?: Record<string, unknown>
}

// Keys the structured UI models — everything else is passthrough.
const MODELED_KEYS = new Set([
  "image",
  "features",
  "containerEnv",
  "privileged",
  "init",
  "capAdd",
  "mounts",
  "postStartCommand",
])

// Curated set of Linux capabilities surfaced in the capAdd control. Names are
// the kernel names WITHOUT the CAP_ prefix (how Docker's CapAdd expects them).
// NET_BIND_SERVICE is the only cap Crewship auto-allows for community features
// (internal/devcontainer/features.go allowedFeatureCapAdd); the rest are
// operator-only escape hatches, and the widest-blast-radius ones are flagged.
export const KNOWN_CAPS: ReadonlyArray<{
  name: string
  description: string
  danger?: boolean
}> = [
  { name: "NET_BIND_SERVICE", description: "Bind to ports below 1024" },
  { name: "NET_RAW", description: "Raw/packet sockets (ping, DNS tunneling risk)", danger: true },
  { name: "NET_ADMIN", description: "Configure networking, interfaces, firewall", danger: true },
  { name: "SYS_ADMIN", description: "Broad admin ops — near-root, mount/namespaces", danger: true },
  { name: "SYS_PTRACE", description: "Trace/inspect other processes", danger: true },
  { name: "SYS_TIME", description: "Set the system clock" },
  { name: "SYS_NICE", description: "Change process priority / scheduling" },
  { name: "DAC_OVERRIDE", description: "Bypass file read/write/exec permission checks", danger: true },
  { name: "CHOWN", description: "Change file ownership" },
  { name: "FOWNER", description: "Bypass permission checks on owned files" },
  { name: "SETUID", description: "Change process UID", danger: true },
  { name: "SETGID", description: "Change process GID", danger: true },
  { name: "MKNOD", description: "Create device nodes" },
  { name: "AUDIT_WRITE", description: "Write to the kernel audit log" },
]

const KNOWN_CAP_NAMES = new Set(KNOWN_CAPS.map((c) => c.name))

/** Uppercases and strips a leading CAP_ so "cap_net_bind_service" and
 *  "NET_BIND_SERVICE" normalize to the same Docker cap name. */
export function normalizeCap(raw: string): string {
  const up = raw.trim().toUpperCase()
  return up.startsWith("CAP_") ? up.slice(4) : up
}

/** True when the (normalized) capability is one Crewship recognizes. */
export function isKnownCap(raw: string): boolean {
  return KNOWN_CAP_NAMES.has(normalizeCap(raw))
}

// Mount-source allowlist — mirrors internal/devcontainer/mount_validate.go so
// the UI rejects a docker.sock / host-path bind before save instead of after.
const ALLOWED_MOUNT_SOURCES = new Set(["/dev/fuse"])
const VOLUME_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/
const MAX_VOLUME_NAME_LEN = 255

/** True if `source` is a safe mount source (the /dev/fuse allowlist or a valid
 *  Docker named volume). Host paths (leading "/") other than the allowlist and
 *  the docker socket are rejected — a container-escape primitive. */
export function isAllowedMountSource(source: string): boolean {
  if (!source) return false
  if (ALLOWED_MOUNT_SOURCES.has(source)) return true
  if (source[0] === "/") return false
  if (source.length > MAX_VOLUME_NAME_LEN) return false
  return VOLUME_NAME_RE.test(source)
}

// Flatten a devcontainer postStartCommand (string | string[] | map) to text.
function flattenCommand(raw: unknown): string {
  if (raw == null) return ""
  if (typeof raw === "string") return raw
  if (Array.isArray(raw)) return raw.filter((x) => typeof x === "string").join("\n")
  if (typeof raw === "object") {
    return Object.keys(raw as Record<string, unknown>)
      .sort()
      .map((k) => (raw as Record<string, unknown>)[k])
      .filter((v) => typeof v === "string")
      .join("\n")
  }
  return ""
}

function normalizeMounts(raw: unknown): MountEntry[] {
  if (!Array.isArray(raw)) return []
  const out: MountEntry[] = []
  for (const m of raw) {
    if (!m || typeof m !== "object") continue
    const src = (m as Record<string, unknown>).source
    const tgt = (m as Record<string, unknown>).target
    if (typeof src !== "string" || typeof tgt !== "string") continue
    const type = (m as Record<string, unknown>).type
    out.push({
      source: src,
      target: tgt,
      type: typeof type === "string" ? type : "bind",
      readonly: Boolean((m as Record<string, unknown>).readonly),
    })
  }
  return out
}

/** Full lossless parse of a devcontainer_config JSON string. */
export function parseDevcontainerFull(jsonStr: string): ParsedDevcontainer {
  const empty: ParsedDevcontainer = {
    image: "debian:bookworm-slim",
    features: {},
    containerEnv: {},
    privileged: false,
    init: false,
    capAdd: [],
    mounts: [],
    postStartCommand: "",
    passthrough: {},
  }
  if (!jsonStr) return empty
  let parsed: Record<string, unknown>
  try {
    parsed = JSON.parse(jsonStr)
  } catch {
    return empty
  }
  if (!parsed || typeof parsed !== "object") return empty

  const passthrough: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(parsed)) {
    if (!MODELED_KEYS.has(k)) passthrough[k] = v
  }

  const capAddRaw = parsed.capAdd
  return {
    image: (typeof parsed.image === "string" && parsed.image) || "debian:bookworm-slim",
    features: (parsed.features as FeatureMap) || {},
    containerEnv: (parsed.containerEnv as Record<string, string>) || {},
    privileged: Boolean(parsed.privileged),
    init: Boolean(parsed.init),
    capAdd: Array.isArray(capAddRaw) ? capAddRaw.filter((c) => typeof c === "string") : [],
    mounts: normalizeMounts(parsed.mounts),
    postStartCommand: flattenCommand(parsed.postStartCommand),
    passthrough,
  }
}

function parseDevcontainerConfig(jsonStr: string): {
  image: string
  features: FeatureMap
} {
  const full = parseDevcontainerFull(jsonStr)
  return { image: full.image, features: full.features }
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
  features: FeatureMap,
  extra?: DevcontainerExtras
): string {
  const config: Record<string, unknown> = { image }
  if (Object.keys(features).length > 0) {
    config.features = features
  }

  if (extra) {
    if (extra.containerEnv && Object.keys(extra.containerEnv).length > 0) {
      config.containerEnv = extra.containerEnv
    }
    if (extra.privileged) config.privileged = true
    if (extra.init) config.init = true
    if (extra.capAdd && extra.capAdd.length > 0) {
      config.capAdd = extra.capAdd
    }
    if (extra.mounts && extra.mounts.length > 0) {
      config.mounts = extra.mounts.map((m) => {
        const out: Record<string, unknown> = {
          source: m.source,
          target: m.target,
          type: m.type || "bind",
        }
        if (m.readonly) out.readonly = true
        return out
      })
    }
    if (extra.postStartCommand && extra.postStartCommand.trim()) {
      config.postStartCommand = extra.postStartCommand
    }
    // Re-emit unmodeled keys last so the advanced escape hatch never loses an
    // operator's hand-written config. Modeled keys above take precedence.
    if (extra.passthrough) {
      for (const [k, v] of Object.entries(extra.passthrough)) {
        if (!(k in config)) config[k] = v
      }
    }
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
// parseDevcontainerFull, buildDevcontainerJSON extras, MountEntry,
// ParsedDevcontainer, DevcontainerExtras, KNOWN_CAPS, normalizeCap, isKnownCap
// and isAllowedMountSource are exported inline at their declarations above.
