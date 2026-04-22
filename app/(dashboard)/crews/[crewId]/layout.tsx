export function generateStaticParams() {
  return [{ crewId: "_" }]
}

export default function CrewDetailLayout({
  children,
}: {
  children: React.ReactNode
}) {
  return <>{children}</>
}
