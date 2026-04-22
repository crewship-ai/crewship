import { CredentialsPageClient } from "./credentials-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function CredentialsPage() {
  return <CredentialsPageClient />
}
