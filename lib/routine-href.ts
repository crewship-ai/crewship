// Canonical deep-link to a routine's detail view on the /routines page.
// Centralized so the route + query-param shape lives in exactly one place —
// several surfaces (activity rail rows, trace side panel, routine preview
// card, overview nodes) link here, and the /routines page reads the slug back
// via useSearchParams (see routines-layout). If the route ever moves to a
// path segment (/routines/[slug]), only this function and that reader change.
export function routineHref(slug: string): string {
  return `/routines?slug=${encodeURIComponent(slug)}`
}
