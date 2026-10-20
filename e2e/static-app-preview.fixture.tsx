import React, { useState } from "react"
import { createRoot } from "react-dom/client"
import { staticArtifactDocument } from "../lib/pages/artifact-static"
import { StaticAppPreview } from "../components/features/pages/static-app-preview"

const hostile = `<h1>Static calculator</h1><style>h1 { color: rgb(1, 2, 3) }</style>
<script>parent.postMessage('author-executed', '*'); fetch('/escape/script'); window.authorExecuted = true</script>
<img src="/escape/image" onerror="parent.postMessage('author-executed','*')">
<img src="data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' onload='alert(1)'/>">
<img alt="Local raster" src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAEUlEQVR4nGNQSFjwH4QZMBgAo1MMu8a2NUsAAAAASUVORK5CYII=">
<style>@import '/escape/import'; body { background: url('/escape/css') }</style>
<div style="background-image:url('/escape/style')">Visible text</div>
<link rel="stylesheet" href="/escape/link"><meta http-equiv="refresh" content="0;url=/escape/meta">
<base href="/escape/base"><iframe src="/escape/frame"></iframe><object data="/escape/object"></object>
<svg><image href="/escape/svg"/></svg><math href="/escape/math">Math</math>
<a href="/escape/anchor" ping="/escape/ping" target="_top">No navigation</a>
<form action="/escape/form"><input autofocus onfocus="fetch('/escape/focus')"><button>Submit</button></form>
<video poster="/escape/poster"><source src="/escape/media"></video>`
const first = { workspaceId: "workspace", pageId: "page-a", revision: 1, profile: "static-html-v1", html: hostile }
function Fixture() {
  const [publication, setPublication] = useState<typeof first | null>(first)
  return <>
    <button onClick={() => setPublication({ ...first, revision: 2, html: "<h1>Version two</h1>" })}>Publish second</button>
    <button onClick={() => setPublication(null)}>Revoke access</button>
    <button onClick={() => setPublication({ ...first, pageId: "replacement" })}>Replace Page</button>
    <StaticAppPreview publication={publication} />
  </>
}
createRoot(document.getElementById("root")!).render(<Fixture />)

;(window as unknown as { prepareStatic: typeof staticArtifactDocument }).prepareStatic = staticArtifactDocument
