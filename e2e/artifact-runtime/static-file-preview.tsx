import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { ArtifactHTMLPreview } from '../../components/features/chat/artifact/artifact-html-preview'

// Actual chat file preview component, isolated from authenticated file IO.
function Fixture() {
  const [text, setText] = useState('')
  Object.assign(window, { showStaticFiles: setText })
  return <><ArtifactHTMLPreview path="report.html" text={text} /><ArtifactHTMLPreview path="report.md" text={text} /></>
}
createRoot(document.getElementById('root')!).render(<Fixture />)
