import React from "react"
import { createRoot } from "react-dom/client"
import { PageWorkbookSurface } from "@/components/features/pages/workbook-editor-host"

/** The workbook Page surface alone, as the Pages layout mounts it, with its leave-guard signal shown. */
function Fixture() {
  const [dirty, setDirty] = React.useState(false)
  return <main style={{ display: "flex", flexDirection: "column", height: "100vh" }}>
    <output aria-label="Unsent edits">{String(dirty)}</output>
    <PageWorkbookSurface workspaceId="ws-fixture" pageId="page-1" name="Budget" onDirtyChange={setDirty} />
  </main>
}

createRoot(document.getElementById("root")!).render(<Fixture />)
