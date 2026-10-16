/** Shared frame keeps the source operation distinct from current issue data. */
export function EntityCardFrame({ children }: { children: React.ReactNode }) {
  return <section className="my-2 min-w-0 space-y-3 rounded-xl border bg-card p-3 text-sm shadow-sm">{children}</section>
}
