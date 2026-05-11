import { OrchestrationProjectRedirect } from "./redirect-client"

export function generateStaticParams() {
  return [{ projectId: "_" }]
}

// Projects subsumed into /issues during IA refactor. Stub kept one
// release for bookmark compat.
export default function Page() {
  return <OrchestrationProjectRedirect />
}
