"use client"

import { InboxList } from "@/components/features/inbox/inbox-list"

// /inbox — unified human-in-the-loop surface backed by inbox_items
// (migration v85). Renders a Linear-Triage style three-state list
// (unread → read → resolved) with kind-specific detail actions:
// approve waitpoints, resolve escalations, retry failed runs.
export default function InboxPage() {
  return <InboxList />
}
