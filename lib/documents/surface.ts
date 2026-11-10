import * as Y from "yjs"
import { createSurface } from "../../tools/docs-service/surface-core.mjs"

/** The Canvas surface model with this browser's own pinned Yjs classes. */
export const documentSurface = createSurface({ Y })

export interface SurfacePlacement { id: string; x: number; y: number; w: number; h: number; z: number }
export type SurfaceItem =
  | { id: string; kind: "note"; x: number; y: number; w: number; h: number; z: number; text: string; tone: string }
  | { id: string; kind: "shape"; x: number; y: number; w: number; h: number; z: number; shape: string; tone: string; text?: string }
  | { id: string; kind: "connector"; z: number; from: { ref: string }; to: { ref: string }; label?: string }
export interface SurfaceView { placements: SurfacePlacement[]; items: SurfaceItem[]; orphans: string[]; conflicts: string[]; detached: string[] }
