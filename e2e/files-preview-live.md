# Dev2 Files preview acceptance

The samples are synthetic QA fixtures uploaded through the supported owner CLI. They are **not** agent-generated work products. The test signs in normally as the seeded Emma VIEWER and performs read-only file access through the UI.

```sh
# Optional: create a new uniquely named demo folder and upload the fixtures.
node e2e/prepare-files-preview-demo.mjs

# After the preview implementation has been deployed to Dev2:
FILES_PREVIEW_MANIFEST=/private/run/files-preview-demo-manifest.json \
TEAM_CHAT_STATE=/private/path/accounts.json node e2e/files-preview-live.mjs
```

The uploader targets only `--profile dev2 --server http://localhost:8082`, agent `ma-ena` and crew `copy-site`. It writes a unique `preview-demo-<date>-<random>` folder, preserving existing files. It prints `FILES_PREVIEW_MANIFEST=<path>` for the mode-0600 manifest in its newly created private directory; pass that exact path to the browser test. Keep the local fixture files until the exact-byte download comparison finishes.

The browser scenario checks PDF page rendering, page navigation, zoom, a download checksum, PNG dimensions, code preview, and an output PDF through the crew-scoped route. It never sends an agent prompt. Credentials stay in the private seed state and are not printed or captured in reports. The script respects authentication rate limits.

Results are written to `files-preview-live-report.json` inside the private run directory printed as `Artifacts: …`; screenshots go to `Artifacts: …` run directory. The mobile scenario opens Files at 390px, renders the second PDF page, and checks document overflow.

Known existing limitation: Crew root lists the output tree, while the actual crew shared volume requires `crew files list copy-site --path shared`. The dedicated shared-volume fixture was uploaded successfully but is not discoverable from that UI root; the crew-route browser check therefore uses the agent output PDF via the Crew tree.

Each run creates a unique mode-0700 artifact directory. Reports and screenshots stay there; inspect and sanitize them before copying selected evidence into tracked documentation. Credential state must be a regular mode-0600 file owned by the current user; symlinks are rejected.
