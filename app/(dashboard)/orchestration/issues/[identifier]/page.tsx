import { IssueDetailClient } from "./issue-detail-client"

export function generateStaticParams() {
  return [{ identifier: "_" }]
}

export default function IssueDetailPage() {
  return <IssueDetailClient />
}
