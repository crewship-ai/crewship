"use client"

import { useMemo, useState } from "react"
import { Boxes } from "lucide-react"

import { Input } from "@/components/ui/input"
import { CreateSurfaceField, CreateSurfacePicker } from "@/components/layout/create-surface"
import { getBrandColor } from "../runtime-config-brands"
import { BASE_IMAGES, DEFAULT_BASE_IMAGE, isCustomBaseImage } from "../runtime-config-data"
import type { WizardState } from "./types"

/**
 * The base image, as a row you read and a panel you pick in.
 *
 * The wizard used to render the catalogue inline: nine radio rows and a search
 * box, on a step that also carries tooling, network and sizing. /design shows
 * one summary row saying what the crew runs on with "Change" on the right,
 * and the catalogue as a PANEL the surface swaps to — the same shape as the
 * icon picker, and the reason a create surface can hold a nine-item catalogue
 * without the step becoming a list of lists.
 */

/** What the crew will actually run, given what the wizard has recorded. */
export function effectiveBaseImage(state: WizardState): string {
  return state.runtimeImage.trim() || DEFAULT_BASE_IMAGE
}

export function baseImageDef(image: string) {
  return BASE_IMAGES.find((b) => b.value === image) ?? null
}

/**
 * Set the image on both fields the wizard carries it in.
 *
 * `runtimeImage` is what submit sends; `devcontainerConfig.image` is what
 * RuntimeConfig reads back through its sync effect. Writing only the first
 * would let RuntimeConfig re-propagate its own unchanged image and quietly
 * revert the pick.
 */
export function patchImage(state: WizardState, image: string): Partial<WizardState> {
  let obj: Record<string, unknown> = {}
  if (state.devcontainerConfig.trim()) {
    try {
      const parsed = JSON.parse(state.devcontainerConfig)
      if (parsed && typeof parsed === "object") obj = parsed as Record<string, unknown>
    } catch {
      // Unparseable JSON is an operator's raw edit. Replacing it with a fresh
      // object would drop their work, so leave it alone and let the raw
      // buffer keep whatever image it names.
      return { runtimeImage: image }
    }
  }
  obj.image = image
  return { runtimeImage: image, devcontainerConfig: JSON.stringify(obj, null, 2) }
}

/** The row on the Container step. */
export function BaseImageRow({ state, onChange }: { state: WizardState; onChange: () => void }) {
  const image = effectiveBaseImage(state)
  const def = baseImageDef(image)
  const brand = def?.colorKey ? getBrandColor(def.colorKey) : null
  // DEFAULT_BASE_IMAGE is not one of BASE_IMAGES' full registry paths, so the
  // catalogue lookup above misses it — and calling the shipped default a
  // "Custom image" says an operator typed it. isCustomBaseImage() has always
  // drawn that line; this follows it.
  const label = def
    ? def.label
    : isCustomBaseImage(image)
      ? "Custom image"
      : "Debian slim — the shipped default"

  return (
    <button
      type="button"
      onClick={onChange}
      className="flex w-full items-center gap-3 rounded-lg border border-hairline bg-foreground/[0.02] p-3 text-left transition-colors hover:border-primary/30 hover:bg-primary/[0.04] max-sm:min-h-12"
    >
      {def ? (
        <def.icon className="h-6 w-6 shrink-0" style={brand ? { color: brand } : undefined} />
      ) : (
        <Boxes className="h-6 w-6 shrink-0 text-muted-foreground" />
      )}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] font-medium text-foreground">{label}</span>
        <span className="mt-0.5 block truncate font-mono text-[11px] text-muted-foreground">{image}</span>
      </span>
      <span className="shrink-0 text-[11px] text-muted-foreground">Change</span>
    </button>
  )
}

/** The panel the surface swaps to when that row is clicked. */
export function BaseImagePanel({
  value,
  onChange,
}: {
  value: string
  onChange: (image: string) => void
}) {
  const [search, setSearch] = useState("")

  const options = useMemo(() => {
    const q = search.trim().toLowerCase()
    return BASE_IMAGES.filter((b) => (q ? b.label.toLowerCase().includes(q) : true)).map((b) => ({
      id: b.value,
      label: b.label,
      // Brand colour, not a grey glyph: every entry carries a colorKey for
      // exactly this, because the value is a full registry path and parsing a
      // brand out of it is brittle.
      render: (
        <b.icon className="h-5 w-5" style={{ color: getBrandColor(b.colorKey ?? "") ?? undefined }} />
      ),
    }))
  }, [search])

  const def = baseImageDef(value)

  return (
    <CreateSurfacePicker
      columns={5}
      captions
      preview={
        <div className="flex flex-col items-center gap-2">
          {def ? (
            <def.icon
              className="h-10 w-10"
              style={{ color: getBrandColor(def.colorKey ?? "") ?? undefined }}
            />
          ) : (
            <Boxes className="h-10 w-10 text-muted-foreground" />
          )}
          <span className="font-mono text-[11px] text-muted-foreground">{value}</span>
        </div>
      }
      previewHint={def?.description ?? "A registry reference this workspace can pull."}
      search={{ value: search, onChange: setSearch, placeholder: "Search images…" }}
      options={options}
      value={value}
      onChange={onChange}
      extra={
        <CreateSurfaceField
          label="Or a registry reference"
          htmlFor="crew-custom-image"
          hint="Anything the registry can pull. A custom image is rebuilt on first run and can take several minutes."
        >
          <Input
            id="crew-custom-image"
            value={def ? "" : value}
            onChange={(e) => onChange(e.target.value)}
            placeholder="ghcr.io/your-org/your-image:tag"
            className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
          />
        </CreateSurfaceField>
      }
    />
  )
}
