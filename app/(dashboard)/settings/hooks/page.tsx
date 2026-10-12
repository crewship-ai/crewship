import { HooksPaused } from "@/components/features/settings/hooks/hooks-paused"

// Lifecycle hooks are paused (internal/hooks/paused.go). The page for them,
// HooksPage, is kept as work in progress; this route says why it is off.
export default function SettingsHooksPage() {
  return <HooksPaused />
}
