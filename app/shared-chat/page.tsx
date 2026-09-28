import type { Metadata } from "next"

import { SharedChatViewer } from "./shared-chat-viewer"

export const metadata: Metadata = {
  title: "Read shared chat · Crewship",
  robots: { index: false, follow: false, nocache: true },
  referrer: "no-referrer",
}

// A top-level route keeps the transcript reader outside the authenticated
// dashboard layout. Both credentials are entered on the page, never in its URL.
export default function SharedChatPage() {
  return <SharedChatViewer />
}
