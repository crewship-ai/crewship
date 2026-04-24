import { SettingsPageClient } from "./settings-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function SettingsPage() {
  return <SettingsPageClient />
}
