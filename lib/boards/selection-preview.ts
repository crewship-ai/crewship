import type { BinaryFileData, BinaryFiles } from '@excalidraw/excalidraw/types'
import type { ExcalidrawElement, NonDeleted } from '@excalidraw/excalidraw/element/types'
import type { PageBoard } from '@/hooks/use-page-board'
import type { BoardElement } from './revision-adapter'

const check = (signal: AbortSignal) => { if (signal.aborted) throw new DOMException('Preview cancelled', 'AbortError') }

/** Only the captured selection is exported. Pins are generic rectangles, never
 * screenshots, links or source titles; the server's snapshot remains authority. */
export function selectionPreviewInput(board: PageBoard, selectedIds: readonly string[], availableFiles: readonly BinaryFileData[]) {
  const selected = new Set(selectedIds)
  const drawings = board.records.filter(record => selected.has(record.element_id) && !record.deleted && record.element)
  const ids = new Set(drawings.map(record => record.element_id))
  const files: BinaryFiles = {}
  const available = new Map(availableFiles.map(file => [file.id, file]))
  const elements = drawings.map(record => {
    const element: Record<string, unknown> = structuredClone(record.element!)
    delete element.link; delete element.customData
    for (const key of ['containerId', 'frameId']) if (typeof element[key] === 'string' && !ids.has(element[key] as string)) element[key] = null
    if (Array.isArray(element.boundElements)) element.boundElements = element.boundElements.filter(bound => bound && typeof bound.id === 'string' && ids.has(bound.id))
    for (const key of ['startBinding', 'endBinding']) {
      const binding = element[key] as { elementId?: string } | null
      if (binding && !ids.has(binding.elementId ?? '')) element[key] = null
    }
    if (element.type === 'image') {
      const file = available.get(element.fileId as BinaryFileData['id'])
      if (!file || !/^data:image\/(png|jpeg);base64,/.test(file.dataURL)) throw new Error('A selected image is not available. Wait for images to load or send without a preview.')
      files[file.id] = file
    }
    return element as BoardElement
  })
  const pins = board.placements.filter(pin => selected.has(`board-placement:${pin.id}`)).map(pin => ({
    type: 'rectangle' as const, id: `preview-pin:${pin.id}`, x: pin.x, y: pin.y, width: pin.width, height: pin.height,
    strokeColor: '#868e96', backgroundColor: '#f1f3f5', fillStyle: 'solid' as const,
  }))
  if (!elements.length && !pins.length) throw new Error('Choose a saved selection for the preview.')
  if (elements.length + pins.length > 100) throw new Error('Preview selection exceeds 100 elements.')
  return { elements, pins, files }
}

export async function renderSelectionPreview(board: PageBoard, selectedIds: readonly string[], files: readonly BinaryFileData[], signal: AbortSignal): Promise<string> {
  check(signal)
  const input = selectionPreviewInput(board, selectedIds, files)
  const { exportToBlob, convertToExcalidrawElements } = await import('@excalidraw/excalidraw')
  check(signal)
  const elements = [...input.elements, ...convertToExcalidrawElements(input.pins, { regenerateIds: false })] as NonDeleted<ExcalidrawElement>[]
  // A lower resolution is preferable to a large optional upload. No scene
  // metadata is embedded in the PNG; only this bounded raster is attached.
  for (const size of [1024, 768, 512]) {
    const blob = await exportToBlob({ elements, files: input.files, maxWidthOrHeight: size, mimeType: 'image/png', appState: { exportBackground: true, exportEmbedScene: false, viewBackgroundColor: '#ffffff' } })
    check(signal)
    if (blob.size > 1024 * 1024) continue
    const bytes = new Uint8Array(await blob.arrayBuffer())
    check(signal)
    let binary = ''
    for (let offset = 0; offset < bytes.length; offset += 8192) binary += String.fromCharCode(...bytes.subarray(offset, offset + 8192))
    return btoa(binary)
  }
  throw new Error('The optional preview is too large. Send the saved selection without a preview.')
}
