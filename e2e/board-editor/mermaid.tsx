import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { Excalidraw, CaptureUpdateAction, convertToExcalidrawElements } from '@excalidraw/excalidraw'
import type { ExcalidrawImperativeAPI } from '@excalidraw/excalidraw/types'
import { parseMermaidToExcalidraw } from '@excalidraw/mermaid-to-excalidraw'
import '@excalidraw/excalidraw/index.css'

// Compatibility fixture with a trusted literal, not a production generation
// service or proof that arbitrary Mermaid is safe. Job receipts here are a
// test-only stand-in; the product must persist them transactionally.
const source = 'flowchart TD\n A[Plan] --> B[Build]\n B --> C[Review]'
function App() {
  const [api, setAPI] = useState<ExcalidrawImperativeAPI | null>(null)
  const [state, setState] = useState('Waiting for generation')
  const [prepared, setPrepared] = useState<ReturnType<typeof convertToExcalidrawElements> | null>(null)
  const [inserted, setInserted] = useState(false)
  return <>
    <button onClick={async () => {
      try {
        const result = await parseMermaidToExcalidraw(source, { startOnLoad: false, maxEdges: 100, maxTextSize: 32000 })
        if (result.files && Object.keys(result.files).length) throw new Error('Unexpected files')
        setPrepared(convertToExcalidrawElements(result.elements))
        setState('Ready to insert')
      } catch (error) { setState(`Conversion failed: ${String(error)}`) }
    }}>Prepare diagram</button>
    <button disabled={!prepared || !api || inserted} onClick={() => {
      if (!prepared || !api || inserted) return
      api.updateScene({ elements: [...api.getSceneElements(), ...prepared], captureUpdate: CaptureUpdateAction.IMMEDIATELY })
      setInserted(true); setState('Inserted')
    }}>Insert diagram</button>
    <output>{state}</output>
    <div style={{ width: 1000, height: 650 }}><Excalidraw
      excalidrawAPI={value => {
        setAPI(value)
        Object.assign(window, { mermaidGate: { elements: () => value.getSceneElements() } })
      }}
      UIOptions={{ canvasActions: { loadScene: false, export: false, saveToActiveFile: false } }}
    /></div>
  </>
}
createRoot(document.getElementById('root')!).render(<App />)
