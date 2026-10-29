import DOMPurify from "dompurify"
import { afterEach, expect, it } from "vitest"
import { artifactProjectDocument } from "../artifact-project-preview"
const supported = DOMPurify.isSupported
afterEach(() => { DOMPurify.isSupported = supported })
it("fails closed when sanitizer support is unavailable", () => {
 DOMPurify.isSupported = false
 expect(artifactProjectDocument([{ path: "index.html", content: "<script>bad()</script>", media_type: "text/html" }], "index.html", "token", true)).not.toContain("bad()")
})
