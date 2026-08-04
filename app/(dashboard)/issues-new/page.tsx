"use client"

// /issues-new — unlisted design preview for the issue and project detail
// redesign. Not linked from the sidebar; reachable only by typing the URL,
// which is what we want while the layout is still an argument.
//
// Unlike /routines-new it reads the live workspace rather than fixtures:
// the argument here is that one card vocabulary fits all three nouns, and
// that only holds if it survives the issues we actually have. Nothing on
// the page writes.

import { IssuesNewPreview } from "@/components/features/issues-new/issues-new-preview"

export default function IssuesNewPage() {
  return <IssuesNewPreview />
}
