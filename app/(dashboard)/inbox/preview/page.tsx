import { InboxPreview } from "@/components/features/inbox/preview/inbox-preview"

// /inbox/preview — the 1.0 inbox design, rendered with the production kit and
// a fixture set copied from the Go producers.
//
// Deliberately NOT in the nav: it is a review surface, not a feature. It sits
// behind the dashboard's auth like every other page, renders no live data, and
// issues no requests. Delete this route (and components/features/inbox/preview)
// once the design lands in the real /inbox.
export default function InboxPreviewPage() {
  return <InboxPreview />
}
