"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Search, Copy, Check, Code, Pencil, X,
  Wrench, Hexagon, ArrowRight, Cog, Hash, Cloud, Ship, Blocks, Container,
  AlertCircle,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
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

interface MiseTool {
  name: string
  label: string
  versions: string[]
  defaultVersion: string
}

// ---- Constants ------------------------------------------------------------

const ICON_MAP: Record<string, React.ComponentType<{ className?: string }>> = {
  wrench: Wrench,
  code: Code,
  hexagon: Hexagon,
  "arrow-right": ArrowRight,
  cog: Cog,
  hash: Hash,
  cloud: Cloud,
  ship: Ship,
  blocks: Blocks,
  container: Container,
}

const CATEGORY_LABELS: Record<string, string> = {
  languages: "Languages",
  tools: "Tools",
  cloud: "Cloud",
  databases: "Databases",
}

const MISE_TOOLS: MiseTool[] = [
  { name: "node", label: "Node.js", versions: ["22", "20", "18", "16"], defaultVersion: "22" },
  { name: "python", label: "Python", versions: ["3.13", "3.12", "3.11", "3.10", "3.9"], defaultVersion: "3.12" },
  { name: "go", label: "Go", versions: ["1.23", "1.22", "1.21", "1.20"], defaultVersion: "1.23" },
  { name: "rust", label: "Rust", versions: ["stable", "nightly", "1.82", "1.81", "1.80"], defaultVersion: "stable" },
  { name: "ruby", label: "Ruby", versions: ["3.3", "3.2", "3.1", "3.0"], defaultVersion: "3.3" },
  { name: "java", label: "Java", versions: ["21", "17", "11"], defaultVersion: "21" },
]

const BASE_IMAGES = [
  { value: "debian:bookworm-slim", label: "Debian Bookworm (slim)" },
  { value: "ubuntu:24.04", label: "Ubuntu 24.04" },
  { value: "custom", label: "Custom image..." },
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

  // Mise tools (tool name -> version)
  const [miseTools, setMiseTools] = useState<Record<string, string>>(initialMise)

  // Raw editing mode
  const [editRaw, setEditRaw] = useState(false)
  const [rawDevcontainer, setRawDevcontainer] = useState("")
  const [rawMise, setRawMise] = useState("")

  // Copy feedback
  const [copied, setCopied] = useState(false)

  // Fetch catalog
  const fetchCatalog = useCallback(() => {
    setCatalogLoading(true)
    setCatalogError(false)
    fetch("/api/v1/features/catalog")
      .then((r) => (r.ok ? r.json() : { features: [] }))
      .then((data) => setCatalog(Array.isArray(data.features) ? data.features : []))
      .catch(() => { setCatalog([]); setCatalogError(true) })
      .finally(() => setCatalogLoading(false))
  }, [])

  useEffect(() => {
    fetchCatalog()
  }, [fetchCatalog])

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

  // Effect: propagate on structured changes
  useEffect(() => {
    if (!editRaw) {
      propagate(devcontainerJSON, miseJSON, effectiveImage)
    }
  }, [devcontainerJSON, miseJSON, effectiveImage, editRaw, propagate])

  // Filter catalog
  const filteredCatalog = useMemo(() => {
    if (!searchQuery.trim()) return catalog
    const q = searchQuery.toLowerCase()
    return catalog.filter(
      (f) =>
        f.name.toLowerCase().includes(q) ||
        f.description.toLowerCase().includes(q) ||
        f.category.toLowerCase().includes(q)
    )
  }, [catalog, searchQuery])

  // Group by category
  const groupedCatalog = useMemo(() => {
    const groups: Record<string, CatalogFeature[]> = {}
    for (const f of filteredCatalog) {
      if (!groups[f.category]) groups[f.category] = []
      groups[f.category].push(f)
    }
    return groups
  }, [filteredCatalog])

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

  // Toggle mise tool
  function toggleMiseTool(toolName: string, defaultVersion: string) {
    setMiseTools((prev) => {
      const next = { ...prev }
      if (toolName in next) {
        delete next[toolName]
      } else {
        next[toolName] = defaultVersion
      }
      return next
    })
  }

  // Update mise tool version
  function updateMiseVersion(toolName: string, version: string) {
    setMiseTools((prev) => ({ ...prev, [toolName]: version }))
  }

  // Copy to clipboard
  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(devcontainerJSON)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard API may not be available
    }
  }

  // Apply raw edits
  function applyRawEdits() {
    try {
      // Validate devcontainer JSON
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

      // Validate mise JSON
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

  // Enter raw edit mode
  function enterRawEdit() {
    setRawDevcontainer(devcontainerJSON)
    setRawMise(miseJSON)
    setEditRaw(true)
  }

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
            mise config (JSON)
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
    <div className="space-y-6">
      <Tabs defaultValue="features" className="w-full">
        <TabsList className="w-full justify-start">
          <TabsTrigger value="features">
            Features{Object.keys(selectedFeatures).length > 0 ? ` (${Object.keys(selectedFeatures).length})` : ""}
          </TabsTrigger>
          <TabsTrigger value="mise">
            Mise Runtimes{Object.keys(miseTools).length > 0 ? ` (${Object.keys(miseTools).length})` : ""}
          </TabsTrigger>
          <TabsTrigger value="preview">Preview</TabsTrigger>
        </TabsList>

        {/* ---- Features tab ---- */}
        <TabsContent value="features" className="space-y-4 pt-3">
          {/* Base Image */}
          <div className="space-y-2">
            <Label className="text-xs font-medium">Base Image</Label>
            <div className="flex items-center gap-2">
              <Select
                value={isCustomImage ? "custom" : baseImage}
                onValueChange={(v) => {
                  if (v === "custom") {
                    setIsCustomImage(true)
                  } else {
                    setIsCustomImage(false)
                    setBaseImage(v)
                  }
                }}
              >
                <SelectTrigger className="h-8 text-xs w-64">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {BASE_IMAGES.map((img) => (
                    <SelectItem key={img.value} value={img.value}>
                      {img.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {isCustomImage && (
                <Input
                  value={customImage}
                  onChange={(e) => setCustomImage(e.target.value)}
                  placeholder="e.g. mcr.microsoft.com/devcontainers/base:ubuntu"
                  className="h-8 text-xs flex-1"
                />
              )}
            </div>
            <p className="text-[11px] text-muted-foreground">
              Base container image. Must be glibc-based (Debian/Ubuntu recommended).
            </p>
          </div>

          {/* Search */}
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder="Search features..."
              aria-label="Search features"
              className="h-8 pl-8 text-xs"
            />
          </div>

          {/* Feature cards */}
          {catalogLoading ? (
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} className="h-20 rounded-lg" />
              ))}
            </div>
          ) : (
            Object.entries(groupedCatalog).map(([category, features]) => (
              <div key={category} className="space-y-2">
                <Label className="text-[11px] uppercase tracking-wider text-muted-foreground">
                  {CATEGORY_LABELS[category] || category}
                </Label>
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                  {features.map((feature) => {
                    const isSelected = feature.ref in selectedFeatures
                    const IconComp = ICON_MAP[feature.icon] || Code
                    return (
                      <button
                        key={feature.ref}
                        type="button"
                        role="checkbox"
                        aria-checked={isSelected}
                        aria-label={`${feature.name}: ${feature.description}`}
                        onClick={() => toggleFeature(feature.ref)}
                        className={cn(
                          "flex items-start gap-3 rounded-lg border p-3 text-left transition-all",
                          isSelected
                            ? "border-primary/60 bg-primary/5"
                            : "border-border hover:border-border/80 hover:bg-accent/50"
                        )}
                      >
                        <div
                          className={cn(
                            "flex h-8 w-8 shrink-0 items-center justify-center rounded-md",
                            isSelected
                              ? "bg-primary/10 text-primary"
                              : "bg-muted text-muted-foreground"
                          )}
                        >
                          <IconComp className="h-4 w-4" />
                        </div>
                        <div className="min-w-0 flex-1">
                          <div className="flex items-center gap-2">
                            <span className="text-xs font-medium">{feature.name}</span>
                            <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                              {feature.size_hint}
                            </Badge>
                          </div>
                          <p className="text-[11px] text-muted-foreground line-clamp-2 mt-0.5">
                            {feature.description}
                          </p>
                        </div>
                        <div
                          className={cn(
                            "mt-0.5 h-4 w-4 shrink-0 rounded border transition-colors",
                            isSelected
                              ? "border-primary bg-primary"
                              : "border-muted-foreground/30"
                          )}
                        >
                          {isSelected && <Check className="h-4 w-4 text-primary-foreground p-0.5" />}
                        </div>
                      </button>
                    )
                  })}
                </div>
              </div>
            ))
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

        {/* ---- Mise tab ---- */}
        <TabsContent value="mise" className="space-y-4 pt-3">
          <p className="text-xs text-muted-foreground">
            Use mise to install language runtimes. Versions are managed per-crew and installed on container start.
          </p>

          <div className="space-y-2">
            {MISE_TOOLS.map((tool) => {
              const isEnabled = tool.name in miseTools
              return (
                <div
                  key={tool.name}
                  className={cn(
                    "flex items-center justify-between gap-3 rounded-lg border p-3 transition-all",
                    isEnabled
                      ? "border-primary/60 bg-primary/5"
                      : "border-border"
                  )}
                >
                  <div className="flex items-center gap-3">
                    <Switch
                      size="sm"
                      checked={isEnabled}
                      onCheckedChange={() => toggleMiseTool(tool.name, tool.defaultVersion)}
                      aria-label={tool.label}
                    />
                    <span className="text-xs font-medium">{tool.label}</span>
                  </div>
                  {isEnabled && (
                    <Select
                      value={miseTools[tool.name]}
                      onValueChange={(v) => updateMiseVersion(tool.name, v)}
                    >
                      <SelectTrigger className="h-7 text-xs w-28">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {tool.versions.map((v) => (
                          <SelectItem key={v} value={v}>{v}</SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  )}
                </div>
              )
            })}
          </div>
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
              <Label className="text-xs font-medium">Mise Config</Label>
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
