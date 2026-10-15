import React from 'react'
import { createRoot } from 'react-dom/client'
import { Excalidraw, convertToExcalidrawElements, exportToBlob, exportToSvg } from '@excalidraw/excalidraw'
import type { ExcalidrawImperativeAPI } from '@excalidraw/excalidraw/types'
import type { ExcalidrawElement } from '@excalidraw/excalidraw/element/types'
import '@excalidraw/excalidraw/index.css'
import { staticArtifactDocument } from '../../lib/pages/artifact-static'

// Test-only source registry. This exercises rendering, not product ACL.
const source = '<h1>Shared source secret marker</h1><script>fetch("https://blocked.example/script")</script><img src="https://blocked.example/image"><style>@import url("https://blocked.example/style");</style>'
const safeLinks = new Set(['https://artifact.invalid/first', 'https://artifact.invalid/second'])
const initial = convertToExcalidrawElements([
  { type: 'rectangle', id: 'first', x: 80, y: 100, width: 280, height: 220 },
  { type: 'rectangle', id: 'second', x: 440, y: 100, width: 280, height: 220 },
  { type: 'rectangle', id: 'external', x: 800, y: 100, width: 240, height: 220 },
], { regenerateIds: false }).map(element => ({ ...element, type: 'embeddable', link: element.id === 'external' ? 'https://blocked.example/embed' : `https://artifact.invalid/${element.id}` })) as ExcalidrawElement[]

createRoot(document.getElementById('root')!).render(<div style={{ height: '750px', width: '1200px' }}><Excalidraw
  initialData={{ elements: initial }}
  validateEmbeddable={link => safeLinks.has(link)}
  renderEmbeddable={element => safeLinks.has(element.link ?? '') ? <iframe title={`Artifact ${element.id}`} sandbox="" srcDoc={staticArtifactDocument(source)} style={{ width: '100%', height: '100%', pointerEvents: 'none', border: 0 }} /> : <div>External embed blocked</div>}
  UIOptions={{ canvasActions: { loadScene: false, export: false, saveToActiveFile: false } }}
  excalidrawAPI={api => installAPI(api)}
/></div>)

function installAPI(api: ExcalidrawImperativeAPI) {
  Object.assign(window, { boardEmbed: {
    scene: () => api.getSceneElements().map(({ id, type, link, x, y, width, height }) => ({ id, type, link, x, y, width, height })),
    select: (ids: string[]) => api.updateScene({ appState: { selectedElementIds: Object.fromEntries(ids.map(id => [id, true])) } }),
    selected: () => Object.keys(api.getAppState().selectedElementIds),
    exportSelection: async () => {
      const selected = api.getSceneElements().filter(element => api.getAppState().selectedElementIds[element.id])
      // Excalidraw export cannot photograph a custom iframe. Explicitly project
      // embeds into labelled placeholders, without copying HTML or remote URLs.
      const projected = convertToExcalidrawElements(selected.flatMap(element => [
        { type: 'rectangle' as const, id: `${element.id}-placeholder`, x: element.x, y: element.y, width: element.width, height: element.height },
        { type: 'text' as const, id: `${element.id}-label`, x: element.x + 12, y: element.y + 12, text: 'Artifact preview — placeholder', fontSize: 16 },
      ]))
      const options = { elements: projected, files: {}, appState: { exportBackground: true, viewBackgroundColor: '#fff' } }
      const svg = await exportToSvg({ ...options, skipInliningFonts: true, renderEmbeddables: false })
      const blob = await exportToBlob(options)
      return { svg: svg.outerHTML, blobType: blob.type, blobBytes: blob.size, selectedCount: selected.length }
    },
  } })
}
