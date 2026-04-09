import { IssuePageClient } from "./issue-page-client"

export function generateStaticParams() {
  return [{ identifier: "_" }]
}

export default function IssueDetailPage() {
  return <IssuePageClient />
}
