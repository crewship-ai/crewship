"use client"

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react"
import {
  AlertCircle, Boxes, Check, Copy, FileJson, HardDrive, Info as InfoIcon,
  Package, Pencil, Search, Wrench, X,
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
import {
  CreateSurfaceDisclosure,
  CreateSurfaceSection,
} from "@/components/layout/create-surface"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import { useCatalog } from "@/hooks/use-catalog"

// ---- Types ----------------------------------------------------------------

import {
  featureRefToTool,
  getBrandColor,
  getBrandIcon,
  imageBrandKey,
} from "./runtime-config-brands"
import {
  BASE_IMAGES,
  CATEGORY_FILTERS,
  CATEGORY_LABELS,
  buildDevcontainerJSON,
  buildMiseJSON,
  isCustomBaseImage,
  parseDevcontainerConfig,
  parseDevcontainerFull,
  parseMiseConfig,
} from "./runtime-config-data"
import type { CategoryFilter, FeatureMap } from "./runtime-config-data"
import { RuntimeSecurityConfig, type SecurityConfigValue } from "./runtime-security-config"

export interface RuntimeConfigValue {
  runtimeImage: string
  devcontainerConfig: string
  miseConfig: string
}

interface RuntimeConfigProps {
  value: RuntimeConfigValue
  onChange: (value: RuntimeConfigValue) => void
  /** Gates the privileged toggle in the Security tab (admin + workspace
   *  allow_privileged_credentials). Everything else stays editable. */
  canEditPrivileged?: boolean
  /**
   * Height of the feature / runtime browsers.
   *
   * 420px is right on the crew's Runtime tab, where this component owns the
   * page. Inside the create wizard it is not alone on the step — Network and
   * Size follow it — and a 420px list pushed both about two screens down.
   * The caller says how much room it can spare rather than the component
   * assuming it has the room it used to.
   */
  browserHeight?: string
  /**
   * Tab strip, or sections and disclosures.
   *
   * `tabs` is the crew Settings editor, where this component is one config
   * panel among several and four tabs read as four aspects of one thing.
   * `sections` is New crew's Container step: docs/prd/create-surface-parity.md
   * §6.3 puts base image and preinstalled tooling on the page as sections and
   * folds the rest away, so
   * the step leads with the two decisions most crews make and the other three
   * are one click deep instead of behind a tab strip a create surface has
   * nowhere else.
   */
  layout?: "tabs" | "sections"
  /**
   * Suppress the base-image block.
   *
   * New crew renders the image as a summary row plus a picker panel, the way
   * §6.3 specifies, so the component would otherwise show the catalogue a
   * second time on the same step. The image itself still flows through
   * `value.devcontainerConfig`, which this component syncs from — so the
   * caller owning the control does not mean the caller owning the state.
   */
  hideBaseImage?: boolean
}

interface CatalogFeature {
  ref: string
  name: string
  description: string
  category: string
  icon: string
  size_hint: string
  publisher: string
  tier: string
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

// A hoverable "i" for a base image's description, so the catalogue row stays
// one line instead of wrapping — the shape the owner asked for once the
// catalogue itself was in front of them to react to.
function ImageDescription({ children }: { children: ReactNode }) {
  // Self-contained TooltipProvider rather than relying on one further up the
  // tree: the app layout supplies one, but this component is also mounted
  // directly in unit tests that don't, and nesting a Provider is harmless.
  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            aria-label="Image details"
            onClick={(e) => e.stopPropagation()}
            className="inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-full text-muted-foreground transition-colors hover:text-foreground"
          >
            <InfoIcon className="h-3 w-3" />
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" className="max-w-[280px] text-left text-[11px] leading-relaxed">
          {children}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}


/**
 * A tool's brand mark, in a tinted tile.
 *
 * The catalogue rows used to draw a bare 16px glyph in muted grey, which for a
 * list of 1241 near-identical rows is the same as no icon at all: the thing
 * that tells Ubuntu from Anaconda at a glance is the brand's own colour, and
 * it was being thrown away. The tile is what makes it read as a mark rather
 * than as punctuation before the name.
 */
/**
 * Branded first.
 *
 * Both catalogues arrive in the server's order, which for the runtimes list
 * puts Agebox, Mkcert and Kubectl-Rolesum at the top — six rows of generic
 * package glyphs before anything anyone recognises. Sorting the entries that
 * have a brand mark ahead of the ones that do not turns the first screen into
 * the tools people are actually looking for, and it is the difference between
 * a list with icons and a list with one icon column of grey boxes.
 *
 * Stable within each group, so the server's own ordering still decides ties.
 */
function brandedFirst<T>(items: T[], keyOf: (item: T) => string, fallbackOf?: (item: T) => string): T[] {
  return items
    .map((item, i) => ({ item, i, branded: !!(getBrandIcon(keyOf(item)) || getBrandIcon(fallbackOf?.(item) ?? "")) }))
    .sort((a, b) => (a.branded === b.branded ? a.i - b.i : a.branded ? -1 : 1))
    .map((x) => x.item)
}


function ToolMark({ tool, fallbackIcon, size = "md" }: {
  tool: string
  fallbackIcon?: string
  size?: "sm" | "md"
}) {
  const BrandIcon = getBrandIcon(tool) || getBrandIcon(fallbackIcon || "")
  const color = getBrandColor(tool) || getBrandColor(fallbackIcon || "")
  const box = size === "sm" ? "h-6 w-6" : "h-7 w-7"
  const glyph = size === "sm" ? "h-3.5 w-3.5" : "h-4 w-4"
  return (
    <span
      className={cn(
        "flex shrink-0 items-center justify-center rounded-md border",
        box,
        color ? "border-transparent" : "border-hairline bg-foreground/[0.04]",
      )}
      style={color ? { backgroundColor: `${color}1F`, borderColor: `${color}40` } : undefined}
      aria-hidden
    >
      {BrandIcon ? (
        <BrandIcon className={glyph} style={color ? { color } : undefined} />
      ) : (
        <Package className={cn(glyph, "text-muted-foreground-soft")} />
      )}
    </span>
  )
}


/** One generated file: what it is called, how big it is, and its own copy. */
function FileCard({ name, hint, body, copied, onCopy, onEdit }: {
  name: string
  hint?: string
  body: string
  copied?: boolean
  onCopy?: () => void
  onEdit?: () => void
}) {
  const lines = body ? body.split("\n").length : 0
  return (
    <div className="overflow-hidden rounded-lg border border-hairline bg-foreground/[0.02]">
      <div className="flex items-center gap-2 border-b border-hairline px-3 py-2">
        <FileJson className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span className="truncate font-mono text-[11px] text-foreground">{name}</span>
        {hint && <span className="truncate text-[11px] text-muted-foreground-soft">— {hint}</span>}
        <span className="ml-auto shrink-0 text-[10px] tabular-nums text-muted-foreground-soft">
          {lines} {lines === 1 ? "line" : "lines"}
        </span>
        {onCopy && (
          <Button size="sm" variant="ghost" className="h-7 px-2" onClick={onCopy} aria-label={`Copy ${name}`}>
            {copied ? <Check className="h-3.5 w-3.5 text-success" /> : <Copy className="h-3.5 w-3.5" />}
          </Button>
        )}
        {onEdit && (
          <Button size="sm" variant="ghost" className="h-7 px-2" onClick={onEdit} aria-label={`Edit ${name}`}>
            <Pencil className="h-3.5 w-3.5" />
          </Button>
        )}
      </div>
      <pre className="max-h-[280px] overflow-auto whitespace-pre p-3 font-mono text-[11px] leading-relaxed text-foreground/85">
        {body}
      </pre>
    </div>
  )
}


export function RuntimeConfig({ value, onChange, canEditPrivileged = false, browserHeight = "420px", layout = "tabs", hideBaseImage = false }: RuntimeConfigProps) {
  // Parse initial state from value
  const initialDC = useMemo(() => parseDevcontainerConfig(value.devcontainerConfig), [value.devcontainerConfig])
  const initialFull = useMemo(() => parseDevcontainerFull(value.devcontainerConfig), [value.devcontainerConfig])
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
  const [isCustomImage, setIsCustomImage] = useState(isCustomBaseImage(initialDC.image))
  const [imageSearch, setImageSearch] = useState("")

  // Selected runtime tools (tool name -> version)
  const [miseTools, setMiseTools] = useState<Record<string, string>>(initialMise)

  // Container-privilege escape hatches (#1380). Held as one struct plus the
  // passthrough bucket of top-level keys the structured UI doesn't model, so
  // rebuilding the devcontainer_config never drops an operator's advanced JSON.
  const [security, setSecurity] = useState<SecurityConfigValue>({
    privileged: initialFull.privileged,
    init: initialFull.init,
    capAdd: initialFull.capAdd,
    mounts: initialFull.mounts,
    containerEnv: initialFull.containerEnv,
    postStartCommand: initialFull.postStartCommand,
  })
  const [passthrough, setPassthrough] = useState<Record<string, unknown>>(initialFull.passthrough)

  const syncingRef = useRef(false)

  useEffect(() => {
    syncingRef.current = true
    const dc = parseDevcontainerFull(value.devcontainerConfig)
    const mc = parseMiseConfig(value.miseConfig)
    setSelectedFeatures(dc.features)
    setBaseImage(dc.image)
    const isCustom = isCustomBaseImage(dc.image)
    setIsCustomImage(isCustom)
    if (isCustom) setCustomImage(dc.image)
    setMiseTools(mc)
    setSecurity({
      privileged: dc.privileged,
      init: dc.init,
      capAdd: dc.capAdd,
      mounts: dc.mounts,
      containerEnv: dc.containerEnv,
      postStartCommand: dc.postStartCommand,
    })
    setPassthrough(dc.passthrough)
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

  // Build JSON preview — includes the structured privilege fields and any
  // unmodeled passthrough keys so the visual builder is lossless.
  const devcontainerJSON = useMemo(
    () =>
      buildDevcontainerJSON(effectiveImage, selectedFeatures, {
        privileged: security.privileged,
        init: security.init,
        capAdd: security.capAdd,
        mounts: security.mounts,
        containerEnv: security.containerEnv,
        postStartCommand: security.postStartCommand,
        passthrough,
      }),
    [effectiveImage, selectedFeatures, security, passthrough]
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

  // Filter base-image catalogue. BASE_IMAGES is a short static list (no
  // backend catalog endpoint exists for base images — /api/v1/features and
  // /api/v1/runtimes have one each, images do not), so this filters in
  // memory rather than hitting the network like the two catalogs above.
  const filteredBaseImages = useMemo(() => {
    const q = imageSearch.trim().toLowerCase()
    if (!q) return BASE_IMAGES
    return BASE_IMAGES.filter(
      (img) =>
        img.label.toLowerCase().includes(q) ||
        img.description.toLowerCase().includes(q) ||
        img.value.toLowerCase().includes(q)
    )
  }, [imageSearch])

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
      let img = effectiveImage
      if (rawDevcontainer.trim()) {
        // Validate + fully parse so the structured tabs (incl. Security) and
        // the passthrough bucket resync from the operator's hand-edited JSON.
        const full = parseDevcontainerFull(rawDevcontainer)
        JSON.parse(rawDevcontainer) // surface a syntax error before applying
        img = full.image
        setBaseImage(full.image)
        setSelectedFeatures(full.features)
        setSecurity({
          privileged: full.privileged,
          init: full.init,
          capAdd: full.capAdd,
          mounts: full.mounts,
          containerEnv: full.containerEnv,
          postStartCommand: full.postStartCommand,
        })
        setPassthrough(full.passthrough)
        if (isCustomBaseImage(full.image)) {
          setIsCustomImage(true)
          setCustomImage(full.image)
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
        rawDevcontainer.trim() ||
          buildDevcontainerJSON(effectiveImage, selectedFeatures, {
            privileged: security.privileged,
            init: security.init,
            capAdd: security.capAdd,
            mounts: security.mounts,
            containerEnv: security.containerEnv,
            postStartCommand: security.postStartCommand,
            passthrough,
          }),
        rawMise.trim() || "",
        img
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

  // ---- Panes -------------------------------------------------------------
  //
  // Each of the four tabs' bodies, named. They are rendered either as a tab
  // strip (the crew's Settings tab, where this is a config editor among other
  // config editors) or as sections and disclosures (New crew's Container
  // step, where §6.3 asks for base image and tooling to lead and the rest
  // to fold away). Same controls either way — the two layouts differ in
  // chrome only, which is the whole point of the split.

  const baseImagePane = (
    <div className="space-y-2">
      {/* Under `sections` the surrounding CreateSurfaceSection is already
          titled "Base image", and two headings one line apart read as two
          things. Under `tabs` this label is the only thing naming the block,
          because the Features tab holds both it and the feature list. */}
      {layout === "tabs" && (
        <Label className="text-[11px] uppercase tracking-wider text-muted-foreground">Base Image</Label>
      )}
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
                  <div className="relative">
                    <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      value={imageSearch}
                      onChange={(e) => setImageSearch(e.target.value)}
                      placeholder="Search base images..."
                      aria-label="Search base images"
                      className="h-7 pl-8 text-xs"
                    />
                  </div>

                  <div
                    role="radiogroup"
                    aria-label="Base image"
                    className="rounded-md border border-border/40 bg-card/30 max-h-[220px] overflow-y-auto divide-y divide-border/40"
                  >
                    {filteredBaseImages.map((img) => {
                      const Icon = img.icon
                      const isSelected = baseImage === img.value
                      // colorKey is set explicitly on each entry above
                      // (e.g. "node", "debian", "ubuntu") because img.value
                      // is a full registry path. Falls back to muted
                      // foreground when no key is set (Universal/Boxes).
                      const brandColor = img.colorKey ? getBrandColor(img.colorKey) : null
                      return (
                        // The row is a div holding two siblings, not one
                        // button holding another. `ImageDescription` is a
                        // tooltip trigger — itself a <button> — and nesting it
                        // inside the radio is invalid HTML: React logged a
                        // hydration error for every row on every render, and
                        // what the inner control does for a keyboard or screen
                        // reader user is undefined.
                        <div
                          key={img.value}
                          className={cn(
                            "flex w-full items-center gap-2.5 pr-3 transition-colors",
                            isSelected ? "bg-accent/40" : "hover:bg-accent/30"
                          )}
                        >
                          <button
                            type="button"
                            role="radio"
                            aria-checked={isSelected}
                            onClick={() => setBaseImage(img.value)}
                            className="flex min-w-0 flex-1 items-center gap-2.5 px-3 py-1.5 text-left text-xs outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
                          >
                            <Icon
                              className="w-4 h-4 shrink-0"
                              style={brandColor ? { color: brandColor } : undefined}
                            />
                            <span className="min-w-0 flex-1 flex items-center gap-1.5">
                              <span className="font-medium truncate">{img.label}</span>
                              {img.recommended && (
                                <span className="shrink-0 text-[9px] px-1 py-0 rounded bg-primary/20 text-primary-hover">
                                  RECOMMENDED
                                </span>
                              )}
                            </span>
                            {isSelected && <Check className="w-3.5 h-3.5 shrink-0 text-success" />}
                          </button>
                          <ImageDescription>{img.description}</ImageDescription>
                        </div>
                      )
                    })}
                    {filteredBaseImages.length === 0 && (
                      <p className="text-xs text-muted-foreground text-center py-6">
                        No images found{imageSearch ? ` for "${imageSearch}"` : ""}.
                      </p>
                    )}
                  </div>
                  <Button variant="ghost" size="sm" onClick={() => setIsCustomImage(true)}>
                    Use custom image
                  </Button>
                </>
              )}
            </div>
  )

  const toolingPane = (
    <div className="space-y-3">
        {/* Selected summary */}
        {selectedFeatureCount > 0 && (
          <div className="flex items-center gap-2 px-3 py-1.5 rounded-md bg-accent/30 text-xs">
            <Check className="w-3 h-3 text-success" />
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
          <ScrollArea style={{ height: browserHeight }} className="rounded-md border border-border/40 bg-card/30">
            <div className="divide-y divide-border/40">
              {filteredCatalog.map((feature) => {
                const isSelected = feature.ref in selectedFeatures
                const toolName = featureRefToTool(feature.ref)
                return (
                  <div
                    key={feature.ref}
                    className={cn(
                      "flex items-center gap-3 px-3 py-1.5 text-xs hover:bg-accent/30 transition-colors",
                      isSelected && "bg-accent/20"
                    )}
                  >
                    <ToolMark tool={toolName} fallbackIcon={feature.icon} size="sm" />

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

                    {feature.publisher && (
                      <span
                        className={cn(
                          "shrink-0 text-[10px] font-mono",
                          feature.tier === "official"
                            ? "text-muted-foreground-soft"
                            : feature.tier === "community"
                              ? "text-info/70"
                              : "text-warn/80"
                        )}
                        title={`Published by ${feature.publisher} (${feature.tier})`}
                      >
                        {feature.publisher} · {feature.tier}
                      </span>
                    )}

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
    </div>
  )

  /**
   * The tooling browser, the way §6.3 draws it.
   *
   * The tabs-era list is a table: a row per feature carrying name, ref,
   * description, publisher, tier, size hint and a Switch. That is the right
   * density for the crew's Settings editor, where this component owns the
   * page. On a create step it is six columns of metadata for a decision that
   * is "yes or no", and the features already chosen scroll away above it.
   *
   * So: categories without counts, the picked ones as chips that stay put,
   * and rows that are one click each. The only annotation kept is the tier,
   * and only when it is NOT official — "this feature is published by someone
   * other than devcontainers" is a trust signal, and dropping it to match a
   * specimen would be losing a warning rather than losing chrome.
   */
  const toolingPaneSections = (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-1.5">
        {CATEGORY_FILTERS.filter((c) => c !== "all").map((cat) => {
          const active = featureCategoryFilter === cat
          return (
            <button
              key={cat}
              type="button"
              aria-pressed={active}
              onClick={() => setFeatureCategoryFilter(active ? "all" : cat)}
              className={cn(
                "h-8 rounded-full border px-3 text-xs transition-colors max-sm:h-12 group-data-[mobile=true]/surface:h-12",
                active
                  ? "border-primary/40 bg-primary/15 text-primary-hover"
                  : "border-hairline bg-foreground/[0.03] text-muted-foreground hover:text-foreground",
              )}
            >
              {CATEGORY_LABELS[cat] ?? cat}
            </button>
          )
        })}
      </div>

      <div className="relative">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground-soft" />
        <Input
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
          placeholder="Search features — ansible, terraform, docker, aws-cli…"
          aria-label="Search features"
          className="h-8 pl-8 text-xs max-sm:h-12 max-sm:text-sm"
        />
      </div>

      {/* Picked first, so what you chose never scrolls away. */}
      {selectedFeatureCount > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {Object.keys(selectedFeatures).map((ref) => {
            const tool = featureRefToTool(ref)
            const BrandIcon = getBrandIcon(tool)
            const brandColor = getBrandColor(tool)
            return (
              <button
                key={ref}
                type="button"
                onClick={() => toggleFeature(ref)}
                aria-label={`Remove ${tool}`}
                className="flex h-7 items-center gap-1.5 rounded-md border border-primary/40 bg-primary/[0.12] pl-2 pr-1.5 text-xs text-primary-hover transition-colors hover:bg-primary/20 max-sm:h-10 group-data-[mobile=true]/surface:h-10"
              >
                {BrandIcon && (
                  <BrandIcon className="h-3.5 w-3.5" style={brandColor ? { color: brandColor } : undefined} />
                )}
                {tool}
                <X className="h-3 w-3 opacity-60" />
              </button>
            )
          })}
        </div>
      )}

      {catalogLoading ? (
        <div className="space-y-1">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-7 rounded-md" />
          ))}
        </div>
      ) : catalogError ? (
        <div className="flex flex-col items-center gap-2 py-6">
          <AlertCircle className="h-5 w-5 text-destructive" />
          <p className="text-xs text-muted-foreground">The feature catalogue did not load.</p>
          <Button size="sm" variant="outline" onClick={fetchCatalog}>Try again</Button>
        </div>
      ) : filteredCatalog.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border/60 px-3 py-5 text-center text-xs text-muted-foreground">
          {searchQuery.trim() ? `Nothing matches “${searchQuery}”.` : "No features in this category."}
        </p>
      ) : (
        <div
          style={{ maxHeight: browserHeight }}
          className="space-y-1 overflow-y-auto overscroll-contain rounded-lg border border-hairline bg-foreground/[0.02] p-1.5"
        >
          {brandedFirst(filteredCatalog, (f) => featureRefToTool(f.ref), (f) => f.icon || "").map((feature) => {
            const picked = feature.ref in selectedFeatures
            const tool = featureRefToTool(feature.ref)
            return (
              <button
                key={feature.ref}
                type="button"
                aria-pressed={picked}
                onClick={() => toggleFeature(feature.ref)}
                className={cn(
                  "flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left transition-colors",
                  picked ? "bg-primary/[0.12]" : "hover:bg-foreground/[0.06]",
                )}
              >
                <ToolMark tool={tool} fallbackIcon={feature.icon} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-xs text-foreground">{feature.name}</span>
                  {feature.description && (
                    <span className="block truncate text-[11px] text-muted-foreground">
                      {feature.description}
                    </span>
                  )}
                </span>
                {feature.tier && feature.tier !== "official" && (
                  <span
                    className={cn(
                      "shrink-0 text-[10px] font-mono",
                      feature.tier === "community" ? "text-info/70" : "text-warn/80",
                    )}
                    title={`Published by ${feature.publisher} (${feature.tier})`}
                  >
                    {feature.tier}
                  </span>
                )}
                {picked && <Check className="h-3.5 w-3.5 shrink-0 text-primary-hover" />}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )

  const runtimesPane = (
    <div className="space-y-3">
        <p className="text-[11px] text-muted-foreground">
          Select language runtimes and CLI tools to install in the crew container. Versions are managed
          per-crew and installed on container start.
        </p>

        {/* Selected summary */}
        {selectedRuntimeCount > 0 && (
          <div className="flex items-center gap-2 px-3 py-1.5 rounded-md bg-accent/30 text-xs">
            <Check className="w-3 h-3 text-success" />
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
          <ScrollArea style={{ height: browserHeight }} className="rounded-md border border-border/40 bg-card/30">
            <div className="divide-y divide-border/40">
              {filteredRuntimes.map((entry) => {
                const isEnabled = entry.tool in miseTools
                const selectedVersion =
                  miseTools[entry.tool] ||
                  entry.default_version ||
                  (entry.versions?.[0] ?? "latest")
                const hasVersions = Array.isArray(entry.versions) && entry.versions.length > 0
                const defaultVersion = entry.default_version || (hasVersions ? entry.versions![0] : "latest")
                return (
                  <div
                    key={entry.tool}
                    className={cn(
                      "flex items-center gap-3 px-3 py-1.5 text-xs hover:bg-accent/30 transition-colors",
                      isEnabled && "bg-accent/20"
                    )}
                  >
                    <ToolMark tool={entry.tool} fallbackIcon={entry.icon} size="sm" />

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
    </div>
  )

  /**
   * Language runtimes, as marks rather than a table.
   *
   * Same reasoning as the tooling browser above: on a create step the
   * question is which toolchains this crew needs, and the answer is easier to
   * see as a row of brand marks than as a grid of names with a version
   * dropdown per line. Pinned versions stay editable — they move onto the
   * picked chip, which is where the version actually belongs.
   */
  const runtimesPaneSections = (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-1.5">
        {CATEGORY_FILTERS.filter((c) => c !== "all").map((cat) => {
          const active = runtimeCategoryFilter === cat
          return (
            <button
              key={cat}
              type="button"
              aria-pressed={active}
              onClick={() => setRuntimeCategoryFilter(active ? "all" : cat)}
              className={cn(
                "h-8 rounded-full border px-3 text-xs transition-colors max-sm:h-12 group-data-[mobile=true]/surface:h-12",
                active
                  ? "border-primary/40 bg-primary/15 text-primary-hover"
                  : "border-hairline bg-foreground/[0.03] text-muted-foreground hover:text-foreground",
              )}
            >
              {CATEGORY_LABELS[cat] ?? cat}
            </button>
          )
        })}
      </div>

      <div className="relative">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground-soft" />
        <Input
          value={runtimeSearchQuery}
          onChange={(e) => setRuntimeSearchQuery(e.target.value)}
          placeholder="Search runtimes — node, python, go, terraform, kubectl…"
          aria-label="Search runtimes"
          className="h-8 pl-8 text-xs max-sm:h-12 max-sm:text-sm"
        />
      </div>

      {/* Pinned versions live on the chip, not in a column of dropdowns. */}
      {selectedRuntimeCount > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {Object.entries(miseTools).map(([tool, version]) => (
            <span
              key={tool}
              className="flex h-8 items-center gap-1.5 rounded-md border border-primary/40 bg-primary/[0.12] pl-1.5 pr-1 text-xs text-primary-hover"
            >
              <ToolMark tool={tool} size="sm" />
              {tool}
              <Input
                value={version}
                onChange={(e) => updateRuntimeVersion(tool, e.target.value)}
                aria-label={`${tool} version`}
                className="h-6 w-16 border-transparent bg-black/20 px-1 text-center font-mono text-[11px]"
              />
              <button
                type="button"
                onClick={() => toggleRuntimeTool(tool, "")}
                aria-label={`Remove ${tool}`}
                className="px-0.5 opacity-60 transition-opacity hover:opacity-100"
              >
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
        </div>
      )}

      {runtimeCatalogLoading ? (
        <div className="space-y-1">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-7 rounded-md" />
          ))}
        </div>
      ) : runtimeCatalogError ? (
        <div className="flex flex-col items-center gap-2 py-6">
          <AlertCircle className="h-5 w-5 text-destructive" />
          <p className="text-xs text-muted-foreground">The runtime catalogue did not load.</p>
          <Button size="sm" variant="outline" onClick={fetchRuntimeCatalog}>Try again</Button>
        </div>
      ) : filteredRuntimes.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border/60 px-3 py-5 text-center text-xs text-muted-foreground">
          {runtimeSearchQuery.trim()
            ? `Nothing matches “${runtimeSearchQuery}”.`
            : "No runtimes in this category."}
        </p>
      ) : (
        <div
          style={{ maxHeight: browserHeight }}
          className="space-y-1 overflow-y-auto overscroll-contain rounded-lg border border-hairline bg-foreground/[0.02] p-1.5"
        >
          {brandedFirst(filteredRuntimes, (e) => e.tool, (e) => e.icon || "").map((entry) => {
            const picked = entry.tool in miseTools
            return (
              <button
                key={entry.tool}
                type="button"
                aria-pressed={picked}
                onClick={() => toggleRuntimeTool(entry.tool, entry.default_version ?? "")}
                className={cn(
                  "flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left transition-colors",
                  picked ? "bg-primary/[0.12]" : "hover:bg-foreground/[0.06]",
                )}
              >
                <ToolMark tool={entry.tool} fallbackIcon={entry.icon} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-xs text-foreground">{entry.name}</span>
                  {entry.description && (
                    <span className="block truncate text-[11px] text-muted-foreground">
                      {entry.description}
                    </span>
                  )}
                </span>
                {entry.default_version && !picked && (
                  <span className="shrink-0 font-mono text-[10px] text-muted-foreground-soft">
                    {entry.default_version}
                  </span>
                )}
                {picked && <Check className="h-3.5 w-3.5 shrink-0 text-primary-hover" />}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )

  const securityPane = (
        <RuntimeSecurityConfig
          value={security}
          onChange={setSecurity}
          canEditPrivileged={canEditPrivileged}
        />
  )

  /**
   * The generated files, as files.
   *
   * It was a label, a <pre>, another label and another <pre> — two documents
   * run together with no boundary and no way to tell which numbers belong to
   * which. A file card says what the file is called, how big it is, and gives
   * the copy button for THAT file rather than one button standing over both.
   */
  const previewPane = (
    <div className="space-y-3">
      <FileCard
        name=".devcontainer/devcontainer.json"
        body={devcontainerJSON}
        copied={copied}
        onCopy={handleCopy}
        onEdit={enterRawEdit}
      />
      {miseJSON && (
        <FileCard
          name="mise.toml"
          hint="language runtimes, as JSON"
          body={miseJSON}
        />
      )}
    </div>
  )

  /**
   * What is actually in the box — as marks, not as JSON.
   *
   * The Preview panel answered "what will the generated devcontainer.json
   * look like", which is a question an operator debugging a build asks and
   * nobody creating a crew does. The question people DO ask at this point is
   * simpler: what is going to be in there? So this is the image, the features
   * and the pinned runtimes as the brands they are, updating as they are
   * picked. The JSON is still one tab away on the crew's settings, where the
   * person asking the other question is.
   */
  const insideSummary = (
    <CreateSurfaceSection title="What's inside" icon={Boxes} accent="green" hint="the container this crew gets">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="flex h-8 items-center gap-2 rounded-md border border-hairline bg-foreground/[0.03] pl-1.5 pr-2.5 text-xs">
          <ToolMark tool={imageBrandKey(effectiveImage)} size="sm" />
          <span className="font-mono text-[11px] text-foreground/85">{effectiveImage}</span>
        </span>

        {Object.keys(selectedFeatures).map((ref) => {
          const tool = featureRefToTool(ref)
          return (
            <span
              key={ref}
              className="flex h-8 items-center gap-1.5 rounded-md border border-hairline bg-foreground/[0.03] pl-1.5 pr-2.5 text-xs text-foreground/85"
            >
              <ToolMark tool={tool} size="sm" />
              {tool}
            </span>
          )
        })}

        {Object.entries(miseTools).map(([tool, version]) => (
          <span
            key={tool}
            className="flex h-8 items-center gap-1.5 rounded-md border border-hairline bg-foreground/[0.03] pl-1.5 pr-2.5 text-xs text-foreground/85"
          >
            <ToolMark tool={tool} size="sm" />
            {tool}
            <span className="font-mono text-[11px] text-muted-foreground">{version}</span>
          </span>
        ))}
      </div>

      {selectedFeatureCount === 0 && selectedRuntimeCount === 0 && (
        <p className="text-[11px] text-muted-foreground">
          The image as it ships — nothing added. That is a fine place to start; anything below can be
          added later without rebuilding the crew.
        </p>
      )}
    </CreateSurfaceSection>
  )

  if (layout === "sections") {
    return (
      <div className="flex flex-col gap-4">
        {!hideBaseImage && (
          <CreateSurfaceSection title="Base image" icon={HardDrive} accent="teal">
            {baseImagePane}
          </CreateSurfaceSection>
        )}

        {/* The hints below are the whole difference between these two
         *  sections, in words that do not require knowing what a devcontainer
         *  feature or mise is.
         *
         *  They ARE two mechanisms and the split is real: a feature is an OCI
         *  artifact whose install.sh runs as root at image build time and can
         *  do anything (add a package, start a Postgres server, hang a
         *  postCreateCommand), and the result is cached as an image layer.
         *  mise runs after the build, as the agent user, into
         *  ~/.local/share/mise, and can express exactly one thing: this tool
         *  at this version. Capped at 20 tools (ErrMiseTooManyTools).
         *
         *  But 247 names appear in BOTH catalogues, and they are precisely the
         *  ones anybody types — node, python, go, rust, terraform, kubectl,
         *  aws-cli. For those the choice is real to us and meaningless to the
         *  person making a crew. So the hints name the only two situations
         *  where it actually decides something: "a specific version" and
         *  "anything else". */}
        <CreateSurfaceSection
          title="Preinstalled tooling"
          icon={Wrench}
          accent="amber"
          hint="things the container comes with — whatever version ships"
        >
          {toolingPaneSections}
        </CreateSurfaceSection>

        {/* Folded, not dropped. The specimen for this step (§6.3) shows base
            image and tooling only — but language runtimes and the privileged
            flags ARE settable here today, and quietly removing them from the
            create path would be a capability change wearing a redesign's
            clothes. They fold away instead, with the summary saying whether
            there is anything inside. */}
        <CreateSurfaceDisclosure
          icon={Boxes}
          accent="blue"
          label="Language runtimes"
          // Says what is inside AND why you would open it. The count alone
          // ("none pinned") answered the first and left the section looking
          // like a duplicate of the tooling list above it.
          summary={
            selectedRuntimeCount > 0
              ? `${selectedRuntimeCount} pinned to an exact version`
              : "none pinned — only needed for an exact version"
          }
        >
          {/* This comment used to say the opposite, and was wrong.
           *
           * It claimed "nothing found in this repo puts that shims directory
           * on PATH", reasoning from scripts/entrypoint.sh — which prepends
           * /opt/crew-tools/bin and /home/agent/.local/bin only. That is the
           * wrong file: entrypoint runs `exec sleep infinity` as PID 1 and
           * `docker exec` does not inherit its environment. The agent's PATH
           * comes from the IMAGE, and the image has the shims dir ahead of
           * /usr/local/bin:
           *
           *   PATH=…:/home/agent/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:…
           *
           * Measured, not reasoned: replaying InstallMise + InstallMiseTools
           * on a clean devcontainers/javascript-node:22-bookworm and pinning
           * jq 1.7 serves jq-1.7 over the system's jq-1.6. So the question the
           * old copy dodged — what happens when the same tool is picked in
           * both sections — has an answer, and the copy below now gives it. */}
          <p className="text-[11px] leading-relaxed text-muted-foreground">
            Use this when a version matters — <span className="text-foreground/85">Node 22.11.0</span>,
            not just &ldquo;Node&rdquo;. Anything you only need <em>present</em> belongs in Preinstalled
            tooling above, which also installs faster: its result is cached with the image, this runs
            on every build. Pick the same thing in both and the pinned version is what the agent
            gets. Capped at 20.
          </p>
          {runtimesPaneSections}
        </CreateSurfaceDisclosure>

        {/* Security and the generated files are NOT here, and that is the
            point.
         *
         * Privileged mode, Linux capabilities, extra mounts, container env
         * and the start hook are operator escape hatches for a crew that
         * exists and turned out to need one — /dev/fuse for a build, a bind
         * for a cache. Nobody knows any of that while filling in the form
         * that creates the crew, and putting it here asked every person
         * making their first crew to have an opinion about SYS_PTRACE.
         *
         * Nothing is lost: the crew's Settings tab renders this same
         * component with all four panels and saves by PATCH
         * (crew-canvas-tabs/settings-tab.tsx). The note below says so, so
         * that someone who came looking for it is not left guessing. */}
        {insideSummary}

        <p className="text-[11px] leading-relaxed text-muted-foreground">
          Privileged mode, Linux capabilities, extra mounts, container environment and the start
          hook live in the crew&apos;s settings once it exists — they are answers to problems a
          running crew has, not questions to answer now.
        </p>
      </div>
    )
  }

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
          <TabsTrigger value="security" className="gap-1.5">
            Security
            {security.privileged && (
              <span className="inline-block h-1.5 w-1.5 rounded-full bg-destructive" aria-hidden />
            )}
          </TabsTrigger>
          <TabsTrigger value="preview">Preview</TabsTrigger>
        </TabsList>

        <TabsContent value="features" className="space-y-3 pt-3">
          {baseImagePane}
          {toolingPane}
        </TabsContent>

        <TabsContent value="runtimes" className="space-y-3 pt-3">
          {runtimesPane}
        </TabsContent>

        <TabsContent value="security" className="pt-3">
          {securityPane}
        </TabsContent>

        <TabsContent value="preview" className="space-y-4 pt-3">
          {previewPane}
        </TabsContent>
      </Tabs>
    </div>
  )
}
