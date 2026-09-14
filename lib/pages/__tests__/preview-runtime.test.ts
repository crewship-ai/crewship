import { describe, expect, it } from "vitest"
import { validatePreview, previewSnapshot } from "../preview-runtime"
import { parsePageBundle } from "../parse-bundle"

it("passes only visible panel snapshots and no authoring or identity fields", () => {
  const snapshot = previewSnapshot({ name: "Health", slug: "health", owner: "user/secret", panels: [
    { id: "visible", title: "MySQL", state: "fresh", data: { value: 1 }, producer: "routine/internal", actions: [{ routine: "privileged" }] },
    { panel_id: "sealed", sealed: true, data: { secret: true } },
  ] })
  expect(snapshot.panels).toHaveLength(1)
  expect(snapshot.panels[0].data).toEqual({ value: 1 })
  expect(JSON.stringify(snapshot)).not.toMatch(/secret|privileged|routine\/internal/)
})
it("refuses an inline or same-origin runtime", () => {
  const artifact = { format: "crewship-page-preview/v1" as const, javascript: "console.log('ok')", css: "", toolchain: "test" }
  expect(() => validatePreview(artifact, "about:srcdoc", "https://studio.example.com")).toThrow()
  expect(() => validatePreview(artifact, "https://studio.example.com/api/v1/pages/runtime/bootstrap", "https://studio.example.com")).toThrow()
  expect(() => validatePreview(artifact, "https://pages.example.net/api/v1/pages/runtime/bootstrap", "https://studio.example.com")).not.toThrow()
})
describe("portable UI bundle reader", () => {
  it("preserves source and unknown fields for server validation", () => {
    const bundle = parsePageBundle("format: crewship-page-bundle/v2\nproject:\n  files: []\nunrecognized: true\n")
    expect(bundle.project?.files).toEqual([])
    expect(bundle).toHaveProperty("unrecognized", true)
  })
  it.each([
    'format: a\nformat: b',
    'format: &x a',
    'format: &x a\nproject: *x',
    'format: a\n---\nformat: b',
  ])("rejects ambiguous YAML: %s", text => { expect(() => parsePageBundle(text)).toThrow() })
})

 it("only allows same-origin bootstrap with an explicit development setting", () => {
 const artifact = { format: "crewship-page-preview/v1" as const, javascript: "void 0", css: "", toolchain: "test" }
 const studio = "https://studio.example.com"
 expect(() => validatePreview(artifact, studio + "/api/v1/pages/runtime/bootstrap", studio, true)).not.toThrow()
 for (const runtime of ["http://studio.example.com/api/v1/pages/runtime/bootstrap", "https://studio.example.com:9443/api/v1/pages/runtime/bootstrap", studio + "/other", studio + "/api/v1/pages/runtime/bootstrap?x=1"]) {
 expect(() => validatePreview(artifact, runtime, studio, true)).toThrow()
 }
 })
