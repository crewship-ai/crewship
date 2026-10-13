import { parseMermaidToExcalidraw } from '@excalidraw/mermaid-to-excalidraw'
import { convertToExcalidrawElements } from '@excalidraw/excalidraw'
import { validateBoardDiagram } from './diagram-profile'
declare global { interface Window { __CREWSHIP_BOARD_CONVERTER_PARENT__?: string } }
const parentOrigin = window.__CREWSHIP_BOARD_CONVERTER_PARENT__
const nonce = decodeURIComponent(location.hash.slice(1))
let connected = false
if (parentOrigin && /^[a-f0-9-]{36}$/.test(nonce)) {
  window.addEventListener('message', event => {
    if (connected || event.source !== parent || event.origin !== parentOrigin || event.data?.type !== 'crewship.converter.connect' || event.data?.nonce !== nonce || event.ports.length !== 1) return
    connected = true
    const port = event.ports[0]
    let consumed = false
    port.onmessage = async event => {
      if (consumed) return; consumed = true
      try {
        const data = event.data
        if (data?.nonce !== nonce || data?.type !== 'crewship.converter.input' || typeof data.source !== 'string') throw new Error('Invalid converter input.')
        const source = validateBoardDiagram(data.source)
        if (!data.target || !['x','y','width','height'].every(key => Number.isFinite(data.target[key]) && Math.abs(data.target[key]) <= 1000000)) throw new Error('Invalid diagram target.')
        const parsed = await parseMermaidToExcalidraw(source, { startOnLoad: false, maxEdges: 100, maxTextSize: 32768 })
        if (parsed.files && Object.keys(parsed.files).length) throw new Error('Unsupported diagram file assets.')
        const elements = convertToExcalidrawElements(parsed.elements)
        if (!elements.length || elements.length > 500) throw new Error('Diagram exceeds element limits.')
        const x = Math.min(...elements.map(item => item.x)), y = Math.min(...elements.map(item => item.y))
        const placed = elements.map(item => ({ ...item, x: item.x - x + data.target.x, y: item.y - y + data.target.y }))
        if (JSON.stringify(placed).length > 2 * 1024 * 1024) throw new Error('Diagram exceeds output limits.')
        port.postMessage({ type: 'crewship.converter.result', nonce, elements: placed })
      } catch (error) { port.postMessage({ type: 'crewship.converter.result', nonce, error: error instanceof Error ? error.message.slice(0,512) : 'Conversion failed.' }) }
      finally { port.close() }
    }
    port.start()
  })
  parent.postMessage({ type: 'crewship.converter.ready', nonce }, parentOrigin)
}
