# Dev2 Files preview acceptance — 2026-09-08

Passed six browser scenarios as the normally authenticated seeded Emma VIEWER. No agent prompt or model execution was used. Samples were deliberately synthetic QA documents, uploaded with supported owner CLI commands into a unique demo directory.

- Two-page PDF rendered with a real PDF.js canvas; pages produced different raster output, navigation bounds worked, and zoom reached 125%.
- Download retained the original PDF filename and exact SHA-256 checksum.
- PNG decoded at its actual 900×500 dimensions.
- TypeScript remained available in the existing code editor.
- The same PDF rendered through the Crew tree and crew-scoped download API.
- At 390px viewport width, the mobile Files tab opened the PDF, displayed page two, and caused no document overflow.

No browser runtime errors or failed Files/PDF asset responses occurred. The worker loaded from Dev2 itself. The corrected agent header stays compact beside the wide preview; the initial 330px vertical header expansion is gone.

Screenshots: [PDF beside chat](assets/files-preview-dev2/agent-pdf-page-two.png), [PNG beside chat](assets/files-preview-dev2/agent-image.png), [mobile PDF](assets/files-preview-dev2/mobile-preview.png). Machine-readable evidence: [report](files-preview-dev2-live.json). The desktop PDF screenshot was recaptured after the final cosmetic footer removal; the image and mobile screenshots retain the earlier footer. The full acceptance result is unchanged.

The agent samples remain under `preview-demo-2026-09-08-fc52510a` in Mařena's Files. A separate PDF was also uploaded to Copy site's `shared/preview-demo-2026-09-08-fc52510a/crew-demo-preview.pdf`.

Existing limitation: the Crew UI root exposes the output tree, while the actual shared volume is accessible with CLI `crew files list copy-site --path shared`. Consequently, this acceptance verifies the crew-scoped renderer using the output PDF; it does not claim that the separate shared-volume sample is discoverable from the current UI root.

Reproduction: [browser acceptance instructions](../../../e2e/files-preview-live.md). Private account state and local fixture manifests remain outside git.
