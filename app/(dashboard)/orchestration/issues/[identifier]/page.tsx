import { OrchestrationIssueRedirect } from "./redirect-client"

export function generateStaticParams() {
  return [{ identifier: "_" }]
}

// /orchestration/issues/[identifier] — client-side redirect stub kept
// one release for bookmark compat. Canonical route is /issues/[id].
export default function Page() {
  return <OrchestrationIssueRedirect />
}
