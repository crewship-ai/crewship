// Next.js static export requires dynamic routes to enumerate their param
// slots at build time. We export a single placeholder; real navigation
// happens client-side via `useParams` so any crewId resolves.
export function generateStaticParams() {
  return [{ crewId: "_" }]
}

export default function CrowsNestCrewLayout({
  children,
}: {
  children: React.ReactNode
}) {
  return <>{children}</>
}
