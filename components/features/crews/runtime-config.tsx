"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import type { IconType } from "react-icons"
import {
  Search, Copy, Check, Pencil, X,
  Package, Cloud, AlertCircle, Boxes,
} from "lucide-react"
import {
  SiDebian, SiUbuntu, SiAlpinelinux,
  SiPython, SiNodedotjs, SiGo, SiRust, SiRuby, SiPhp,
  SiOpenjdk, SiElixir, SiErlang, SiDeno, SiBun,
  SiDotnet, SiKotlin, SiScala, SiSwift, SiZig, SiCrystal,
  SiDocker, SiKubernetes, SiTerraform, SiAnsible, SiHelm,
  SiGooglecloud, SiDigitalocean,
  SiGithub, SiGitlab, SiGit,
  SiPostgresql, SiMysql, SiMariadb, SiRedis, SiMongodb, SiSqlite,
  SiHashicorp, SiVault, SiPulumi,
  SiVim, SiZsh, SiGnubash,
  SiPnpm, SiYarn, SiNpm,
  SiFirebase, SiSupabase,
  SiNginx, SiApache,
  SiFlutter, SiDart, SiElm,
  SiHugo, SiVite, SiWebpack,
  SiJulia, SiLua, SiPerl, SiR, SiHaskell,
  SiGraphql,
  SiOpenai, SiAnthropic,
  SiHeroku, SiVercel, SiCloudflare, SiFlydotio,
  SiOllama, SiSentry, SiDatadog, SiRailway, SiNetlify,
} from "react-icons/si"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { Skeleton } from "@/components/ui/skeleton"
import { ScrollArea } from "@/components/ui/scroll-area"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { cn } from "@/lib/utils"
import { toast } from "sonner"

// ---- Types ----------------------------------------------------------------

export interface RuntimeConfigValue {
  runtimeImage: string
  devcontainerConfig: string
  miseConfig: string
}

interface RuntimeConfigProps {
  value: RuntimeConfigValue
  onChange: (value: RuntimeConfigValue) => void
}

interface CatalogFeature {
  ref: string
  name: string
  description: string
  category: string
  icon: string
  size_hint: string
}

interface RuntimeEntry {
  name: string
  tool: string
  description?: string
  category: string
  icon: string
  versions?: string[]
  default_version?: string
  backends?: string[]
}

type CategoryFilter = "all" | "languages" | "tools" | "cloud" | "databases"

// ---- Brand icon map -------------------------------------------------------

const BRAND_ICONS: Record<string, IconType> = {
  python: SiPython,
  node: SiNodedotjs,
  nodejs: SiNodedotjs,
  "node.js": SiNodedotjs,
  go: SiGo,
  golang: SiGo,
  rust: SiRust,
  ruby: SiRuby,
  php: SiPhp,
  java: SiOpenjdk,
  openjdk: SiOpenjdk,
  jdk: SiOpenjdk,
  elixir: SiElixir,
  erlang: SiErlang,
  deno: SiDeno,
  bun: SiBun,
  dotnet: SiDotnet,
  "dotnet-core": SiDotnet,
  kotlin: SiKotlin,
  scala: SiScala,
  swift: SiSwift,
  zig: SiZig,
  crystal: SiCrystal,
  docker: SiDocker,
  "docker-in-docker": SiDocker,
  "docker-outside-of-docker": SiDocker,
  "docker-from-docker": SiDocker,
  kubectl: SiKubernetes,
  "kubectl-helm-minikube": SiKubernetes,
  kubernetes: SiKubernetes,
  k8s: SiKubernetes,
  minikube: SiKubernetes,
  helm: SiHelm,
  terraform: SiTerraform,
  ansible: SiAnsible,
  gcloud: SiGooglecloud,
  "google-cloud": SiGooglecloud,
  "google-cloud-cli": SiGooglecloud,
  digitalocean: SiDigitalocean,
  doctl: SiDigitalocean,
  github: SiGithub,
  "github-cli": SiGithub,
  gh: SiGithub,
  gitlab: SiGitlab,
  "glab-cli": SiGitlab,
  git: SiGit,
  "git-lfs": SiGit,
  postgres: SiPostgresql,
  postgresql: SiPostgresql,
  psql: SiPostgresql,
  mysql: SiMysql,
  mariadb: SiMariadb,
  redis: SiRedis,
  "redis-cli": SiRedis,
  mongo: SiMongodb,
  mongodb: SiMongodb,
  mongosh: SiMongodb,
  sqlite: SiSqlite,
  sqlite3: SiSqlite,
  hashicorp: SiHashicorp,
  vault: SiVault,
  pulumi: SiPulumi,
  pnpm: SiPnpm,
  yarn: SiYarn,
  npm: SiNpm,
  vim: SiVim,
  neovim: SiVim,
  nvim: SiVim,
  zsh: SiZsh,
  bash: SiGnubash,
  sh: SiGnubash,
  firebase: SiFirebase,
  supabase: SiSupabase,
  nginx: SiNginx,
  apache: SiApache,
  httpd: SiApache,
  flutter: SiFlutter,
  dart: SiDart,
  elm: SiElm,
  hugo: SiHugo,
  vite: SiVite,
  webpack: SiWebpack,
  julia: SiJulia,
  lua: SiLua,
  perl: SiPerl,
  r: SiR,
  haskell: SiHaskell,
  ghc: SiHaskell,
  graphql: SiGraphql,
  openai: SiOpenai,
  anthropic: SiAnthropic,
  claude: SiAnthropic,
  // AWS family (no dedicated react-icons entry; use lucide Cloud fallback)
  aws: Cloud,
  "aws-cli": Cloud,
  awscli: Cloud,
  // Azure family (no dedicated react-icons entry; use lucide Cloud fallback)
  azure: Cloud,
  "azure-cli": Cloud,
  az: Cloud,
  // Heroku
  heroku: SiHeroku,
  "heroku-cli": SiHeroku,
  // Vercel
  vercel: SiVercel,
  "vercel-cli": SiVercel,
  // Cloudflare
  cloudflare: SiCloudflare,
  "cloudflare-cli": SiCloudflare,
  wrangler: SiCloudflare,
  flarectl: SiCloudflare,
  // Fly.io
  fly: SiFlydotio,
  "fly-cli": SiFlydotio,
  flyctl: SiFlydotio,
  // Ollama
  ollama: SiOllama,
  // Sentry
  sentry: SiSentry,
  "sentry-cli": SiSentry,
  // Datadog
  datadog: SiDatadog,
  "datadog-ci": SiDatadog,
  // Railway
  railway: SiRailway,
  // Netlify
  netlify: SiNetlify,
}

function getBrandIcon(tool: string): IconType | null {
  if (!tool) return null
  const key = tool.toLowerCase()
  if (BRAND_ICONS[key]) return BRAND_ICONS[key]
  // Try stripping common suffixes/prefixes
  const stripped = key.replace(/-cli$|^cli-/, "").replace(/\d+$/, "")
  return BRAND_ICONS[stripped] || null
}

// ---- Brand color map -----------------------------------------------------
//
// Official brand colors taken from each project's style guide / brand
// page (cross-referenced with Simple Icons, which mirrors the same
// official hex values). Pure-black logos (GitHub, HashiCorp, Vercel,
// Ollama, Railway, Crystal) are rendered as #FFFFFF on this dark
// theme — that's the brand-recommended dark-mode treatment.
const BRAND_COLORS: Record<string, string> = {
  python: "#3776AB",
  node: "#339933",
  nodejs: "#339933",
  "node.js": "#339933",
  go: "#00ADD8",
  golang: "#00ADD8",
  rust: "#DEA584",
  ruby: "#CC342D",
  php: "#777BB4",
  java: "#ED8B00",
  openjdk: "#ED8B00",
  jdk: "#ED8B00",
  elixir: "#4B275F",
  erlang: "#A90533",
  deno: "#70FFAF",
  bun: "#FBF0DF",
  dotnet: "#512BD4",
  "dotnet-core": "#512BD4",
  kotlin: "#7F52FF",
  scala: "#DC322F",
  swift: "#F05138",
  zig: "#F7A41D",
  crystal: "#FFFFFF",
  docker: "#2496ED",
  "docker-in-docker": "#2496ED",
  "docker-outside-of-docker": "#2496ED",
  "docker-from-docker": "#2496ED",
  kubectl: "#326CE5",
  "kubectl-helm-minikube": "#326CE5",
  kubernetes: "#326CE5",
  k8s: "#326CE5",
  minikube: "#326CE5",
  helm: "#0F1689",
  terraform: "#7B42BC",
  ansible: "#EE0000",
  gcloud: "#4285F4",
  "google-cloud": "#4285F4",
  "google-cloud-cli": "#4285F4",
  digitalocean: "#0080FF",
  doctl: "#0080FF",
  github: "#FFFFFF",
  "github-cli": "#FFFFFF",
  gh: "#FFFFFF",
  gitlab: "#FC6D26",
  "glab-cli": "#FC6D26",
  git: "#F05032",
  "git-lfs": "#F05032",
  postgres: "#4169E1",
  postgresql: "#4169E1",
  psql: "#4169E1",
  mysql: "#4479A1",
  mariadb: "#003545",
  redis: "#DC382D",
  "redis-cli": "#DC382D",
  mongo: "#47A248",
  mongodb: "#47A248",
  mongosh: "#47A248",
  sqlite: "#003B57",
  sqlite3: "#003B57",
  hashicorp: "#FFFFFF",
  vault: "#FFEC6E",
  pulumi: "#8A3391",
  pnpm: "#F69220",
  yarn: "#2C8EBB",
  npm: "#CB3837",
  vim: "#019733",
  neovim: "#019733",
  nvim: "#019733",
  zsh: "#FFFFFF",
  bash: "#4EAA25",
  sh: "#4EAA25",
  firebase: "#FFCA28",
  supabase: "#3ECF8E",
  nginx: "#009639",
  apache: "#D22128",
  httpd: "#D22128",
  flutter: "#02569B",
  dart: "#0175C2",
  elm: "#1293D8",
  hugo: "#FF4088",
  vite: "#646CFF",
  webpack: "#8DD6F9",
  julia: "#9558B2",
  lua: "#2C2D72",
  perl: "#39457E",
  r: "#276DC3",
  haskell: "#5D4F85",
  ghc: "#5D4F85",
  graphql: "#E10098",
  openai: "#412991",
  anthropic: "#D97757",
  claude: "#D97757",
  aws: "#FF9900",
  "aws-cli": "#FF9900",
  awscli: "#FF9900",
  azure: "#0078D4",
  "azure-cli": "#0078D4",
  az: "#0078D4",
  heroku: "#430098",
  "heroku-cli": "#430098",
  vercel: "#FFFFFF",
  "vercel-cli": "#FFFFFF",
  cloudflare: "#F38020",
  "cloudflare-cli": "#F38020",
  wrangler: "#F38020",
  flarectl: "#F38020",
  fly: "#7B3FE4",
  "fly-cli": "#7B3FE4",
  flyctl: "#7B3FE4",
  ollama: "#FFFFFF",
  sentry: "#362D59",
  "sentry-cli": "#362D59",
  datadog: "#632CA6",
  "datadog-ci": "#632CA6",
  railway: "#FFFFFF",
  netlify: "#00C7B7",
  // Base image distros
  debian: "#A81D33",
  ubuntu: "#E95420",
  alpinelinux: "#0D597F",
  alpine: "#0D597F",
  // Anaconda: official green
  anaconda: "#44A833",
}

function getBrandColor(tool: string): string | null {
  if (!tool) return null
  const key = tool.toLowerCase()
  if (BRAND_COLORS[key]) return BRAND_COLORS[key]
  const stripped = key.replace(/-cli$|^cli-/, "").replace(/\d+$/, "")
  return BRAND_COLORS[stripped] || null
}

function featureRefToTool(ref: string): string {
  // ghcr.io/devcontainers/features/python:1 -> "python"
  const withoutTag = ref.split(":")[0]
  const parts = withoutTag.split("/")
  return parts[parts.length - 1] || ""
}

// ---- Constants ------------------------------------------------------------

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

function parseDevcontainerConfig(jsonStr: string): {
  image: string
  features: Record<string, Record<string, unknown>>
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
  features: Record<string, Record<string, unknown>>
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

export function RuntimeConfig({ value, onChange }: RuntimeConfigProps) {
  // Parse initial state from value
  const initialDC = useMemo(() => parseDevcontainerConfig(value.devcontainerConfig), [value.devcontainerConfig])
  const initialMise = useMemo(() => parseMiseConfig(value.miseConfig), [value.miseConfig])

  // Feature catalog
  const [catalog, setCatalog] = useState<CatalogFeature[]>([])
  const [catalogLoading, setCatalogLoading] = useState(true)
  const [catalogError, setCatalogError] = useState(false)
  const [searchQuery, setSearchQuery] = useState("")
  const [featureCategoryFilter, setFeatureCategoryFilter] = useState<CategoryFilter>("all")

  // Runtime catalog
  const [runtimeCatalog, setRuntimeCatalog] = useState<RuntimeEntry[]>([])
  const [runtimeCatalogLoading, setRuntimeCatalogLoading] = useState(true)
  const [runtimeCatalogError, setRuntimeCatalogError] = useState(false)
  const [runtimeSearchQuery, setRuntimeSearchQuery] = useState("")
  const [runtimeCategoryFilter, setRuntimeCategoryFilter] = useState<CategoryFilter>("all")

  // Selected features (ref -> options)
  const [selectedFeatures, setSelectedFeatures] = useState<Record<string, Record<string, unknown>>>(initialDC.features)

  // Base image
  const [baseImage, setBaseImage] = useState(initialDC.image)
  const [customImage, setCustomImage] = useState(
    BASE_IMAGES.some((b) => b.value === initialDC.image) ? "" : initialDC.image
  )
  const [isCustomImage, setIsCustomImage] = useState(
    !BASE_IMAGES.some((b) => b.value === initialDC.image) && initialDC.image !== "debian:bookworm-slim"
  )

  // Selected runtime tools (tool name -> version)
  const [miseTools, setMiseTools] = useState<Record<string, string>>(initialMise)

  const syncingRef = useRef(false)

  useEffect(() => {
    syncingRef.current = true
    const dc = parseDevcontainerConfig(value.devcontainerConfig)
    const mc = parseMiseConfig(value.miseConfig)
    setSelectedFeatures(dc.features)
    setBaseImage(dc.image)
    const isCustom = !BASE_IMAGES.some((b) => b.value === dc.image)
    setIsCustomImage(isCustom)
    if (isCustom) setCustomImage(dc.image)
    setMiseTools(mc)
    requestAnimationFrame(() => { syncingRef.current = false })
  }, [value.devcontainerConfig, value.miseConfig])

  // Raw editing mode
  const [editRaw, setEditRaw] = useState(false)
  const [rawDevcontainer, setRawDevcontainer] = useState("")
  const [rawMise, setRawMise] = useState("")

  // Copy feedback
  const [copied, setCopied] = useState(false)

  // Fetch feature catalog
  const fetchCatalog = useCallback(() => {
    setCatalogLoading(true)
    setCatalogError(false)
    fetch("/api/v1/features/catalog")
      .then((r) => {
        if (!r.ok) throw new Error(`Catalog fetch failed: ${r.status}`)
        return r.json()
      })
      .then((data) => setCatalog(Array.isArray(data.features) ? data.features : []))
      .catch(() => { setCatalog([]); setCatalogError(true) })
      .finally(() => setCatalogLoading(false))
  }, [])

  useEffect(() => {
    fetchCatalog()
  }, [fetchCatalog])

  // Fetch runtime catalog
  const fetchRuntimeCatalog = useCallback(async () => {
    setRuntimeCatalogLoading(true)
    setRuntimeCatalogError(false)
    try {
      const r = await fetch("/api/v1/runtimes/catalog")
      if (!r.ok) throw new Error(`Runtime catalog fetch failed: ${r.status}`)
      const data = await r.json()
      setRuntimeCatalog(Array.isArray(data.runtimes) ? data.runtimes : [])
    } catch {
      setRuntimeCatalog([])
      setRuntimeCatalogError(true)
    } finally {
      setRuntimeCatalogLoading(false)
    }
  }, [])

  useEffect(() => {
    fetchRuntimeCatalog()
  }, [fetchRuntimeCatalog])

  // Compute effective image
  const effectiveImage = isCustomImage ? customImage || "debian:bookworm-slim" : baseImage

  // Build JSON preview
  const devcontainerJSON = useMemo(
    () => buildDevcontainerJSON(effectiveImage, selectedFeatures),
    [effectiveImage, selectedFeatures]
  )
  const miseJSON = useMemo(() => buildMiseJSON(miseTools), [miseTools])

  // Propagate changes upstream
  const propagate = useCallback(
    (dcJson: string, mJson: string, img: string) => {
      onChange({
        runtimeImage: img,
        devcontainerConfig: dcJson,
        miseConfig: mJson,
      })
    },
    [onChange]
  )

  useEffect(() => {
    if (syncingRef.current) return
    if (!editRaw) {
      propagate(devcontainerJSON, miseJSON, effectiveImage)
    }
  }, [devcontainerJSON, miseJSON, effectiveImage, editRaw, propagate])

  // Filter feature catalog
  const filteredCatalog = useMemo(() => {
    const q = searchQuery.trim().toLowerCase()
    return catalog.filter((f) => {
      if (featureCategoryFilter !== "all" && f.category !== featureCategoryFilter) return false
      if (!q) return true
      return (
        f.name.toLowerCase().includes(q) ||
        f.description.toLowerCase().includes(q) ||
        f.category.toLowerCase().includes(q) ||
        f.ref.toLowerCase().includes(q)
      )
    })
  }, [catalog, searchQuery, featureCategoryFilter])

  // Filter runtime catalog
  const filteredRuntimes = useMemo(() => {
    const q = runtimeSearchQuery.trim().toLowerCase()
    return runtimeCatalog.filter((r) => {
      if (runtimeCategoryFilter !== "all" && r.category !== runtimeCategoryFilter) return false
      if (!q) return true
      return (
        r.name.toLowerCase().includes(q) ||
        r.tool.toLowerCase().includes(q) ||
        (r.description?.toLowerCase().includes(q) ?? false) ||
        r.category.toLowerCase().includes(q)
      )
    })
  }, [runtimeCatalog, runtimeSearchQuery, runtimeCategoryFilter])

  // Counts per category for filter pills
  const featureCategoryCounts = useMemo(() => {
    const c: Record<string, number> = { all: catalog.length }
    for (const f of catalog) c[f.category] = (c[f.category] || 0) + 1
    return c
  }, [catalog])

  const runtimeCategoryCounts = useMemo(() => {
    const c: Record<string, number> = { all: runtimeCatalog.length }
    for (const r of runtimeCatalog) c[r.category] = (c[r.category] || 0) + 1
    return c
  }, [runtimeCatalog])

  // Toggle feature
  function toggleFeature(ref: string) {
    setSelectedFeatures((prev) => {
      const next = { ...prev }
      if (ref in next) {
        delete next[ref]
      } else {
        next[ref] = {}
      }
      return next
    })
  }

  // Toggle runtime tool
  function toggleRuntimeTool(toolName: string, defaultVersion: string) {
    setMiseTools((prev) => {
      const next = { ...prev }
      if (toolName in next) {
        delete next[toolName]
      } else {
        next[toolName] = defaultVersion || "latest"
      }
      return next
    })
  }

  function updateRuntimeVersion(toolName: string, version: string) {
    setMiseTools((prev) => ({ ...prev, [toolName]: version }))
  }

  function clearAllFeatures() {
    setSelectedFeatures({})
  }

  function clearAllRuntimes() {
    setMiseTools({})
  }

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(devcontainerJSON)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // noop
    }
  }

  function applyRawEdits() {
    try {
      if (rawDevcontainer.trim()) {
        const parsed = JSON.parse(rawDevcontainer)
        const img = parsed.image || "debian:bookworm-slim"
        const feats = parsed.features || {}
        setBaseImage(img)
        setSelectedFeatures(feats)
        if (!BASE_IMAGES.some((b) => b.value === img)) {
          setIsCustomImage(true)
          setCustomImage(img)
        } else {
          setIsCustomImage(false)
        }
      }

      if (rawMise.trim()) {
        const parsed = JSON.parse(rawMise)
        setMiseTools(parsed.tools || {})
      } else {
        setMiseTools({})
      }

      propagate(
        rawDevcontainer.trim() || buildDevcontainerJSON(effectiveImage, selectedFeatures),
        rawMise.trim() || "",
        effectiveImage
      )
      setEditRaw(false)
    } catch {
      toast.error("Invalid JSON syntax. Please check your configuration.")
      return
    }
  }

  function enterRawEdit() {
    setRawDevcontainer(devcontainerJSON)
    setRawMise(miseJSON)
    setEditRaw(true)
  }

  const selectedFeatureCount = Object.keys(selectedFeatures).length
  const selectedRuntimeCount = Object.keys(miseTools).length

  // ---- Raw edit mode -------------------------------------------------------

  if (editRaw) {
    return (
      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <Label className="text-xs font-medium">Edit Raw Configuration</Label>
          <div className="flex gap-2">
            <Button size="sm" variant="outline" onClick={() => setEditRaw(false)}>
              <X className="mr-1.5 h-3 w-3" />
              Cancel
            </Button>
            <Button size="sm" onClick={applyRawEdits}>
              <Check className="mr-1.5 h-3 w-3" />
              Apply
            </Button>
          </div>
        </div>

        <div className="space-y-2">
          <Label htmlFor="raw-devcontainer" className="text-xs text-muted-foreground">
            devcontainer.json
          </Label>
          <Textarea
            id="raw-devcontainer"
            value={rawDevcontainer}
            onChange={(e) => setRawDevcontainer(e.target.value)}
            className="font-mono text-xs min-h-[200px] resize-y"
            placeholder='{"image": "debian:bookworm-slim", "features": {}}'
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="raw-mise" className="text-xs text-muted-foreground">
            Language runtimes config (JSON)
          </Label>
          <Textarea
            id="raw-mise"
            value={rawMise}
            onChange={(e) => setRawMise(e.target.value)}
            className="font-mono text-xs min-h-[100px] resize-y"
            placeholder='{"tools": {"node": "22", "python": "3.12"}}'
          />
        </div>
      </div>
    )
  }

  // ---- Visual mode ---------------------------------------------------------

  return (
    <div className="space-y-4">
      <Tabs defaultValue="features" className="w-full">
        <TabsList className="w-full justify-start">
          <TabsTrigger value="features">
            Features{selectedFeatureCount > 0 ? ` (${selectedFeatureCount})` : ""}
          </TabsTrigger>
          <TabsTrigger value="runtimes">
            Language Runtimes{selectedRuntimeCount > 0 ? ` (${selectedRuntimeCount})` : ""}
          </TabsTrigger>
          <TabsTrigger value="preview">Preview</TabsTrigger>
        </TabsList>

        {/* ---- Features tab ---- */}
        <TabsContent value="features" className="space-y-3 pt-3">
          {/* Base Image */}
          <div className="space-y-2">
            <Label className="text-[11px] uppercase tracking-wider text-muted-foreground">Base Image</Label>
            {isCustomImage ? (
              <div className="flex gap-2">
                <Input
                  value={customImage}
                  onChange={(e) => setCustomImage(e.target.value)}
                  placeholder="e.g., myregistry/myimage:tag"
                  className="flex-1 h-8 text-xs"
                />
                <Button variant="ghost" size="sm" onClick={() => setIsCustomImage(false)}>
                  Preset
                </Button>
              </div>
            ) : (
              <>
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 mb-2">
                  {BASE_IMAGES.map((img) => {
                    const Icon = img.icon
                    const isSelected = baseImage === img.value
                    // colorKey is set explicitly on each entry above
                    // (e.g. "node", "debian", "ubuntu") because img.value
                    // is a full registry path. Falls back to muted
                    // foreground when no key is set (Universal/Boxes).
                    const brandColor = img.colorKey ? getBrandColor(img.colorKey) : null
                    return (
                      <button
                        key={img.value}
                        type="button"
                        role="radio"
                        aria-checked={isSelected}
                        onClick={() => setBaseImage(img.value)}
                        className={cn(
                          "flex items-start gap-2 px-3 py-2 text-left rounded-md border text-xs transition-colors",
                          isSelected
                            ? "border-primary bg-accent/50"
                            : "border-border/40 hover:bg-accent/30"
                        )}
                      >
                        <Icon
                          className="w-4 h-4 mt-0.5 shrink-0"
                          style={brandColor ? { color: brandColor } : undefined}
                        />
                        <div className="min-w-0 flex-1">
                          <div className="font-medium flex items-center gap-1.5">
                            {img.label}
                            {img.recommended && (
                              <span className="text-[9px] px-1 py-0 rounded bg-primary/20 text-primary">RECOMMENDED</span>
                            )}
                          </div>
                          <div className="text-[10px] text-muted-foreground line-clamp-2 mt-0.5">
                            {img.description}
                          </div>
                        </div>
                      </button>
                    )
                  })}
                </div>
                <Button variant="ghost" size="sm" onClick={() => setIsCustomImage(true)}>
                  Use custom image
                </Button>
              </>
            )}
          </div>

          {/* Selected summary */}
          {selectedFeatureCount > 0 && (
            <div className="flex items-center gap-2 px-3 py-1.5 rounded-md bg-accent/30 text-xs">
              <Check className="w-3 h-3 text-emerald-500" />
              <span className="font-medium">{selectedFeatureCount} selected</span>
              <button
                onClick={clearAllFeatures}
                className="ml-auto text-muted-foreground hover:text-foreground text-[11px]"
              >
                Clear
              </button>
            </div>
          )}

          {/* Search */}
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder="Search features..."
              aria-label="Search features"
              className="h-7 pl-8 text-xs"
            />
          </div>

          {/* Category pills */}
          <div className="flex flex-wrap gap-1 text-[11px]">
            {CATEGORY_FILTERS.map((cat) => {
              const count = featureCategoryCounts[cat] ?? 0
              if (cat !== "all" && count === 0) return null
              const active = featureCategoryFilter === cat
              return (
                <button
                  key={cat}
                  type="button"
                  onClick={() => setFeatureCategoryFilter(cat)}
                  className={cn(
                    "px-2 py-0.5 rounded-full border transition-colors",
                    active
                      ? "bg-primary text-primary-foreground border-primary"
                      : "border-border/40 text-muted-foreground hover:bg-accent/50"
                  )}
                >
                  {cat === "all" ? "All" : CATEGORY_LABELS[cat] || cat}
                  {count > 0 && <span className="ml-1 opacity-60">{count}</span>}
                </button>
              )
            })}
          </div>

          {/* List */}
          {catalogLoading ? (
            <div className="space-y-1">
              {Array.from({ length: 8 }).map((_, i) => (
                <Skeleton key={i} className="h-7 rounded-md" />
              ))}
            </div>
          ) : (
            <ScrollArea className="h-[420px] rounded-md border border-border/40 bg-card/30">
              <div className="divide-y divide-border/40">
                {filteredCatalog.map((feature) => {
                  const isSelected = feature.ref in selectedFeatures
                  const toolName = featureRefToTool(feature.ref)
                  const BrandIcon = getBrandIcon(toolName) || getBrandIcon(feature.icon || "")
                  const brandColor = getBrandColor(toolName) || getBrandColor(feature.icon || "")
                  const isCloud = feature.category === "cloud"
                  return (
                    <div
                      key={feature.ref}
                      className={cn(
                        "flex items-center gap-3 px-3 py-1.5 text-xs hover:bg-accent/30 transition-colors",
                        isSelected && "bg-accent/20"
                      )}
                    >
                      <div className="shrink-0 w-4 h-4 flex items-center justify-center text-muted-foreground">
                        {BrandIcon ? (
                          <BrandIcon
                            className="w-4 h-4"
                            style={brandColor ? { color: brandColor } : undefined}
                          />
                        ) : isCloud ? (
                          <Cloud className="w-4 h-4" />
                        ) : (
                          <Package className="w-4 h-4" />
                        )}
                      </div>

                      <div className="flex-1 min-w-0 flex items-center gap-2">
                        <span className="font-medium text-foreground truncate">{feature.name}</span>
                        <span className="text-muted-foreground/60 text-[10px] font-mono shrink-0">
                          {toolName}
                        </span>
                        {feature.description && (
                          <span className="text-muted-foreground/60 truncate hidden md:inline">
                            {feature.description}
                          </span>
                        )}
                      </div>

                      {feature.size_hint && (
                        <span className="shrink-0 text-[10px] text-muted-foreground/50 font-mono">
                          {feature.size_hint}
                        </span>
                      )}

                      <Switch
                        checked={isSelected}
                        onCheckedChange={() => toggleFeature(feature.ref)}
                        aria-label={feature.name}
                        className="scale-75"
                      />
                    </div>
                  )
                })}
              </div>
            </ScrollArea>
          )}

          {!catalogLoading && catalogError && (
            <div className="flex flex-col items-center gap-2 py-6">
              <AlertCircle className="h-5 w-5 text-destructive" />
              <p className="text-xs text-destructive">Failed to load feature catalog.</p>
              <Button size="sm" variant="outline" onClick={fetchCatalog}>
                Retry
              </Button>
            </div>
          )}

          {!catalogLoading && !catalogError && filteredCatalog.length === 0 && (
            <p className="text-xs text-muted-foreground text-center py-6">
              No features found{searchQuery ? ` for "${searchQuery}"` : ""}.
            </p>
          )}
        </TabsContent>

        {/* ---- Language Runtimes tab ---- */}
        <TabsContent value="runtimes" className="space-y-3 pt-3">
          <p className="text-[11px] text-muted-foreground">
            Select language runtimes and CLI tools to install in the crew container. Versions are managed
            per-crew and installed on container start.
          </p>

          {/* Selected summary */}
          {selectedRuntimeCount > 0 && (
            <div className="flex items-center gap-2 px-3 py-1.5 rounded-md bg-accent/30 text-xs">
              <Check className="w-3 h-3 text-emerald-500" />
              <span className="font-medium">{selectedRuntimeCount} selected</span>
              <button
                onClick={clearAllRuntimes}
                className="ml-auto text-muted-foreground hover:text-foreground text-[11px]"
              >
                Clear
              </button>
            </div>
          )}

          {/* Search */}
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={runtimeSearchQuery}
              onChange={(e) => setRuntimeSearchQuery(e.target.value)}
              placeholder="Search runtimes (node, python, terraform, kubectl...)"
              aria-label="Search language runtimes"
              className="h-7 pl-8 text-xs"
            />
          </div>

          {/* Category pills */}
          <div className="flex flex-wrap gap-1 text-[11px]">
            {CATEGORY_FILTERS.map((cat) => {
              const count = runtimeCategoryCounts[cat] ?? 0
              if (cat !== "all" && count === 0) return null
              const active = runtimeCategoryFilter === cat
              return (
                <button
                  key={cat}
                  type="button"
                  onClick={() => setRuntimeCategoryFilter(cat)}
                  className={cn(
                    "px-2 py-0.5 rounded-full border transition-colors",
                    active
                      ? "bg-primary text-primary-foreground border-primary"
                      : "border-border/40 text-muted-foreground hover:bg-accent/50"
                  )}
                >
                  {cat === "all" ? "All" : CATEGORY_LABELS[cat] || cat}
                  {count > 0 && <span className="ml-1 opacity-60">{count}</span>}
                </button>
              )
            })}
          </div>

          {runtimeCatalogLoading ? (
            <div className="space-y-1">
              {Array.from({ length: 8 }).map((_, i) => (
                <Skeleton key={i} className="h-7 rounded-md" />
              ))}
            </div>
          ) : (
            <ScrollArea className="h-[420px] rounded-md border border-border/40 bg-card/30">
              <div className="divide-y divide-border/40">
                {filteredRuntimes.map((entry) => {
                  const isEnabled = entry.tool in miseTools
                  const selectedVersion =
                    miseTools[entry.tool] ||
                    entry.default_version ||
                    (entry.versions?.[0] ?? "latest")
                  const BrandIcon = getBrandIcon(entry.tool) || getBrandIcon(entry.icon || "")
                  const brandColor = getBrandColor(entry.tool) || getBrandColor(entry.icon || "")
                  const hasVersions = Array.isArray(entry.versions) && entry.versions.length > 0
                  const defaultVersion = entry.default_version || (hasVersions ? entry.versions![0] : "latest")
                  const isCloud = entry.category === "cloud"
                  return (
                    <div
                      key={entry.tool}
                      className={cn(
                        "flex items-center gap-3 px-3 py-1.5 text-xs hover:bg-accent/30 transition-colors",
                        isEnabled && "bg-accent/20"
                      )}
                    >
                      <div className="shrink-0 w-4 h-4 flex items-center justify-center text-muted-foreground">
                        {BrandIcon ? (
                          <BrandIcon
                            className="w-4 h-4"
                            style={brandColor ? { color: brandColor } : undefined}
                          />
                        ) : isCloud ? (
                          <Cloud className="w-4 h-4" />
                        ) : (
                          <Package className="w-4 h-4" />
                        )}
                      </div>

                      <div className="flex-1 min-w-0 flex items-center gap-2">
                        <span className="font-medium text-foreground truncate">{entry.name}</span>
                        <span className="text-muted-foreground/60 text-[10px] font-mono shrink-0">
                          {entry.tool}
                        </span>
                        {entry.description && (
                          <span className="text-muted-foreground/60 truncate hidden md:inline">
                            {entry.description}
                          </span>
                        )}
                      </div>

                      {isEnabled && (
                        <div className="shrink-0">
                          {hasVersions ? (
                            <Select
                              value={selectedVersion}
                              onValueChange={(v) => updateRuntimeVersion(entry.tool, v)}
                            >
                              <SelectTrigger className="h-6 w-24 text-[11px] px-2">
                                <SelectValue />
                              </SelectTrigger>
                              <SelectContent>
                                {!entry.versions!.includes(selectedVersion) && (
                                  <SelectItem value={selectedVersion} className="text-[11px]">
                                    {selectedVersion}
                                  </SelectItem>
                                )}
                                {entry.versions!.map((v) => (
                                  <SelectItem key={v} value={v} className="text-[11px]">{v}</SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          ) : (
                            <Input
                              value={selectedVersion}
                              onChange={(e) => updateRuntimeVersion(entry.tool, e.target.value)}
                              placeholder="latest"
                              className="h-6 w-24 text-[11px] font-mono"
                              aria-label={`${entry.name} version`}
                            />
                          )}
                        </div>
                      )}

                      <Switch
                        checked={isEnabled}
                        onCheckedChange={() => toggleRuntimeTool(entry.tool, defaultVersion)}
                        aria-label={entry.name}
                        className="scale-75"
                      />
                    </div>
                  )
                })}
              </div>
            </ScrollArea>
          )}

          {!runtimeCatalogLoading && runtimeCatalogError && (
            <div className="flex flex-col items-center gap-2 py-6">
              <AlertCircle className="h-5 w-5 text-destructive" />
              <p className="text-xs text-destructive">Failed to load language runtimes catalog.</p>
              <Button size="sm" variant="outline" onClick={fetchRuntimeCatalog}>
                Retry
              </Button>
            </div>
          )}

          {!runtimeCatalogLoading && !runtimeCatalogError && filteredRuntimes.length === 0 && (
            <p className="text-xs text-muted-foreground text-center py-6">
              No runtimes found{runtimeSearchQuery ? ` for "${runtimeSearchQuery}"` : ""}.
            </p>
          )}
        </TabsContent>

        {/* ---- Preview tab ---- */}
        <TabsContent value="preview" className="space-y-4 pt-3">
          <div className="flex items-center justify-between">
            <Label className="text-xs font-medium">Generated devcontainer.json</Label>
            <div className="flex gap-1.5">
              <Button size="sm" variant="ghost" className="h-7 px-2" onClick={handleCopy} aria-label="Copy to clipboard">
                {copied ? (
                  <Check className="h-3.5 w-3.5 text-emerald-500" />
                ) : (
                  <Copy className="h-3.5 w-3.5" />
                )}
              </Button>
              <Button size="sm" variant="ghost" className="h-7 px-2" onClick={enterRawEdit} aria-label="Edit raw configuration">
                <Pencil className="h-3.5 w-3.5" />
              </Button>
            </div>
          </div>
          <pre className="rounded-lg border bg-muted/50 p-3 text-xs font-mono overflow-x-auto whitespace-pre-wrap max-h-[300px] overflow-y-auto">
            {devcontainerJSON}
          </pre>

          {miseJSON && (
            <>
              <Label className="text-xs font-medium">Language Runtimes Config</Label>
              <pre className="rounded-lg border bg-muted/50 p-3 text-xs font-mono overflow-x-auto whitespace-pre-wrap max-h-[300px] overflow-y-auto">
                {miseJSON}
              </pre>
            </>
          )}
        </TabsContent>
      </Tabs>
    </div>
  )
}
