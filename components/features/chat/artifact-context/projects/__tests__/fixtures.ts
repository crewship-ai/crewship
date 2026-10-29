import type { ArtifactProjectSnapshot } from "../types"

export const snapshot = (patch: Partial<ArtifactProjectSnapshot> = {}): ArtifactProjectSnapshot => ({
  page_id: "pg-1",
  name: "Onboarding flow",
  slug: "onboarding",
  can_write: true, can_share: true, can_restore: true, can_send_to_agent: true,
  latest_version: 2,
  shared_version: 0,
  manifest: {
    version: 2,
    entrypoint: "index.html",
    files: [
      { path: "index.html", content: "<h1>a</h1>", media_type: "text/html" },
      { path: "signup.html", content: "<h1>b</h1>", media_type: "text/html" },
      { path: "done.html", content: "<h1>c</h1>", media_type: "text/html" },
      { path: "style.css", content: "", media_type: "text/css" },
    ],
  },
  versions: [],
  comments: [],
  ...patch,
})

/** Minimal UseQueryResult shape the cards read. */
export const query = (patch: Record<string, unknown> = {}) => ({ data: undefined, error: null, isPending: false, refetch: () => Promise.resolve(), ...patch })
