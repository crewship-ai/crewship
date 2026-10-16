import type { ExcalidrawElementSkeleton } from '@excalidraw/excalidraw/data/transform'
import type { FileId } from '@excalidraw/excalidraw/element/types'
import type { BoardImageUpload } from './files'

// Feed this to the public convertToExcalidrawElements API with regenerateIds:
// false. File bytes are supplied separately via addFiles, never in this object.
export function boardImageSkeleton(file: BoardImageUpload, x = 80, y = 80): ExcalidrawElementSkeleton {
  const ratio = Math.min(1, 640 / Math.max(file.width, file.height))
  return { type: 'image', id: crypto.randomUUID(), x, y, width: file.width * ratio, height: file.height * ratio,
    fileId: file.file_id as FileId, status: 'saved', scale: [1, 1], crop: null }
}
