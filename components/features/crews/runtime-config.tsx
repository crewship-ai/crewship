"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  AlertCircle, Check, Cloud, Copy, Package, Pencil, Search, X,
} from "lucide-react"
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
import { useCatalog } from "@/hooks/use-catalog"

// ---- Types ----------------------------------------------------------------

import {
  featureRefToTool,
  getBrandColor,
  getBrandIcon,
} from "./runtime-config-brands"
import {
  BASE_IMAGES,
  CATEGORY_FILTERS,
  CATEGORY_LABELS,
  buildDevcontainerJSON,
  buildMiseJSON,
  parseDevcontainerConfig,
  parseMiseConfig,
} from "./runtime-config-data"
import type { CategoryFilter, FeatureMap } from "./runtime-config-data"

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

// Module-scope extractors so the function identity is stable across
// renders — avoids re-triggering useCatalog's effect on every render.
function extractFeatures(json: unknown): CatalogFeature[] {
  const features = (json as { features?: unknown })?.features
  return Array.isArray(features) ? (features as CatalogFeature[]) : []
}

function extractRuntimes(json: unknown): RuntimeEntry[] {
  const runtimes = (json as { runtimes?: unknown })?.runtimes
  return Array.isArray(runtimes) ? (runtimes as RuntimeEntry[]) : []
}


export function RuntimeConfig({ value, onChange }: RuntimeConfigProps) {
  // Parse initial state from value
  const initialDC = useMemo(() => parseDevcontainerConfig(value.devcontainerConfig), [value.devcontainerConfig])
  const initialMise = useMemo(() => parseMiseConfig(value.miseConfig), [value.miseConfig])

  // Feature catalog
  const {
    data: catalogData,
    loading: catalogLoading,
    error: catalogErrorObj,
    refetch: fetchCatalog,
  } = useCatalog<CatalogFeature>("/api/v1/features/catalog", extractFeatures)
  const catalog = useMemo(() => catalogData ?? [], [catalogData])
  const catalogError = catalogErrorObj !== null
  const [searchQuery, setSearchQuery] = useState("")
  const [featureCategoryFilter, setFeatureCategoryFilter] = useState<CategoryFilter>("all")

  // Runtime catalog
  const {
    data: runtimeData,
    loading: runtimeCatalogLoading,
    error: runtimeCatalogErrorObj,
    refetch: fetchRuntimeCatalog,
  } = useCatalog<RuntimeEntry>("/api/v1/runtimes/catalog", extractRuntimes)
  const runtimeCatalog = useMemo(() => runtimeData ?? [], [runtimeData])
  const runtimeCatalogError = runtimeCatalogErrorObj !== null
  const [runtimeSearchQuery, setRuntimeSearchQuery] = useState("")
  const [runtimeCategoryFilter, setRuntimeCategoryFilter] = useState<CategoryFilter>("all")

  // Selected features (ref -> options)
  const [selectedFeatures, setSelectedFeatures] = useState<FeatureMap>(initialDC.features)

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
                              <span className="text-[9px] px-1 py-0 rounded bg-primary/20 text-primary-hover">RECOMMENDED</span>
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
                        <span className="text-muted-foreground text-[10px] font-mono shrink-0">
                          {toolName}
                        </span>
                        {feature.description && (
                          <span className="text-muted-foreground truncate hidden md:inline">
                            {feature.description}
                          </span>
                        )}
                      </div>

                      {feature.size_hint && (
                        <span className="shrink-0 text-[10px] text-muted-foreground-soft font-mono">
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
                        <span className="text-muted-foreground text-[10px] font-mono shrink-0">
                          {entry.tool}
                        </span>
                        {entry.description && (
                          <span className="text-muted-foreground truncate hidden md:inline">
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
