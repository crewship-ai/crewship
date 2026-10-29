import type { ArtifactFile } from "@/hooks/use-artifact-project"

export interface DashboardManifest { schema_version: 1; collections: string[] }
export interface DashboardDocument {
  id: string; version: number; value: Record<string, unknown>; updated_at: string
  source_label: string; source_kind: "human" | "agent" | "unknown"
}
export interface DashboardCollection { collection: string; code_version: number; documents: DashboardDocument[] }
export interface DashboardPreviewOptions { hostOrigin: string; codeVersion: number; collections: string[] }
export const DASHBOARD_DATA_MESSAGE = "crewship-dashboard-data"
const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value)
export const dashboardName = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value)
/** Business keys are literal own-property names, never JavaScript paths. */
export const dashboardField = (value: unknown): value is string => typeof value === "string" && value.length > 0 && value.length <= 128 && !/[\u0000-\u001f\u007f-\u009f]/.test(value)
const version = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) > 0

/** A closed, bounded declaration in the authorized code manifest, never a URL or script. */
export function parseDashboardManifest(files: ArtifactFile[]): DashboardManifest | null {
  const candidates = files.filter(file => file.path === "dashboard.json")
  if (candidates.length !== 1 || candidates[0].content.length > 16384) return null
  try {
    const source = candidates[0].content, value: unknown = JSON.parse(source)
    if (!object(value) || value.schema_version !== 1 || Object.keys(value).length !== 2 || !Array.isArray(value.collections) || !value.collections.length || value.collections.length > 32 || !value.collections.every(dashboardName) || new Set(value.collections).size !== value.collections.length) return null
    // Valid declarations contain no nested objects; count even escaped/duplicate keys.
    const keys = [...source.matchAll(/"(?:\\.|[^"\\])*"\s*:/g)].map(match => JSON.parse(match[0].slice(0, -1).trim()) as string)
    if (keys.length !== 2 || new Set(keys).size !== 2 || !keys.includes("schema_version") || !keys.includes("collections")) return null
    return { schema_version: 1, collections: [...value.collections] }
  } catch { return null }
}

/** Project only the reader-safe wire fields. No raw actor, chat or run identity crosses the frame. */
export function parseDashboardCollection(raw: unknown, collection: string, codeVersion: number): DashboardCollection | null {
  if (!object(raw) || raw.collection !== collection || raw.code_version !== codeVersion || !version(codeVersion) || !Array.isArray(raw.documents) || raw.documents.length > 4096) return null
  const documents: DashboardDocument[] = [], ids = new Set<string>()
  for (const item of raw.documents) {
    if (!object(item) || !dashboardName(item.id) || ids.has(item.id) || !version(item.version) || !object(item.value) || typeof item.updated_at !== "string" || item.updated_at.length > 64 || !Number.isFinite(Date.parse(item.updated_at))) return null
    ids.add(item.id)
    const kind = item.source_kind === "human" || item.source_kind === "agent" ? item.source_kind : "unknown"
    documents.push({ id: item.id, version: item.version, value: item.value, updated_at: item.updated_at,
      source_kind: kind, source_label: typeof item.source_label === "string" && item.source_label.length <= 80 ? item.source_label : kind === "agent" ? "Agent" : kind === "human" ? "Person" : "Unknown source" })
  }
  return { collection, code_version: codeVersion, documents: documents.sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0) }
}

/** Closure-free, host-owned code serialized into the opaque frame after sanitization. */
export function installDashboardBindings(config: DashboardPreviewOptions & { token: string }): void {
  const validName = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value)
  const validField = (value: unknown): value is string => typeof value === "string" && value.length > 0 && value.length <= 128 && !/[\u0000-\u001f\u007f-\u009f]/.test(value)
  let last = 0, failed = false
  const meterStatuses = new WeakMap<HTMLMeterElement, HTMLSpanElement>()
  function meter(element: HTMLMeterElement, value?: number) {
    let status = meterStatuses.get(element)
    if (!status) { status = document.createElement("span"); status.setAttribute("data-row", ""); element.after(status); meterStatuses.set(element, status) }
    const available = value !== undefined && element.max > element.min
    element.removeAttribute("value"); element.hidden = !available; status.hidden = available
    if (available) { element.style.removeProperty("display"); element.value = Math.max(element.min, Math.min(element.max, value)) }
    else element.style.setProperty("display", "none", "important")
    status.textContent = available ? "" : "Unavailable"
  }
  const repeats: Array<{ template: Element; marker: Comment; collection: string; rows: Element[] }> = []
  for (const element of document.querySelectorAll('[data-row="*"]')) {
    if (!element.isConnected) continue
    const collection = element.closest("[data-source]")?.getAttribute("data-source")
    if (!collection || !config.collections.includes(collection) || element.querySelector('[data-row="*"]')) { element.remove(); continue }
    const marker = document.createComment("dashboard rows")
    repeats.push({ template: element, marker, collection, rows: [] }); element.replaceWith(marker)
  }
  for (const element of document.querySelectorAll("[data-field]")) {
    if (element instanceof HTMLMeterElement) meter(element)
    else element.textContent = "Unavailable"
  }
  // Every rendered value carries its source on hover, keyboard focus and in its accessible description.
  // The tooltip and source list are host-owned; data-row="" keeps them out of comment selection.
  const prefix = `crewship-provenance-${String(config.token).replace(/[^0-9a-f]/g, "").slice(0, 12)}`
  const style = document.createElement("style")
  style.textContent = `[data-crewship-provenance]{text-decoration:underline dotted;text-underline-offset:2px;cursor:help}[data-crewship-provenance=missing]{text-decoration-style:dashed;opacity:.75}[data-crewship-provenance]:focus-visible{outline:2px solid #2563eb;outline-offset:2px}#${prefix}-tip{position:fixed;z-index:2147483647;max-width:min(32rem,90vw);padding:4px 8px;border-radius:4px;background:#111827;color:#fff;font:12px/1.4 system-ui,sans-serif;pointer-events:none;overflow-wrap:anywhere}`
  document.head.append(style)
  const tip = document.createElement("div"); tip.id = `${prefix}-tip`; tip.setAttribute("role", "tooltip"); tip.setAttribute("data-row", ""); document.body.append(tip)
  const tipNotes = new WeakMap<Element, string>()
  function hideTip() { tip.style.setProperty("display", "none", "important") }
  function showTip(event: Event) {
    const target = event.target instanceof Element ? event.target.closest("[data-crewship-provenance]") : null
    const note = target ? tipNotes.get(target) : undefined
    if (!target || !note) return
    tip.textContent = note; tip.style.setProperty("display", "block", "important")
    const box = target.getBoundingClientRect(), width = tip.offsetWidth, height = tip.offsetHeight
    const top = box.bottom + 4 + height <= window.innerHeight ? box.bottom + 4 : Math.max(4, box.top - 4 - height)
    tip.style.left = `${Math.max(4, Math.min(box.left, window.innerWidth - width - 4))}px`; tip.style.top = `${top}px`
  }
  hideTip()
  document.addEventListener("mouseover", showTip); document.addEventListener("focusin", showTip)
  document.addEventListener("mouseout", hideTip); document.addEventListener("focusout", hideTip)
  document.addEventListener("keydown", event => { if (event.key === "Escape") hideTip() })
  const provenance = document.createElement("aside"); provenance.setAttribute("aria-label", "Data sources"); provenance.setAttribute("data-row", ""); document.body.append(provenance)
  // Static count is captured without previous rows/notes; each refresh replaces them.
  // Two extra elements per field conservatively cover meter statuses and provenance lines.
  const staticCost = document.querySelectorAll("*").length + 2 * document.querySelectorAll("[data-field]").length
  const repeatCosts = repeats.map(repeat => 1 + repeat.template.querySelectorAll("*").length + 2 * (repeat.template.matches("[data-field]") ? 1 : 0) + 2 * repeat.template.querySelectorAll("[data-field]").length)
  const staticFields = Array.from(document.querySelectorAll("[data-field]"))
  const repeatFields = repeats.map(repeat => [...(repeat.template.matches("[data-field]") ? [repeat.template] : []), ...repeat.template.querySelectorAll("[data-field]")])
  // Count UTF-8 without allocating an encoded copy of a potentially repeated value.
  function bytes(text: string): number {
    let count = 0
    for (let index = 0; index < text.length; index++) {
      const code = text.charCodeAt(index)
      if (code < 128) count++
      else if (code < 2048) count += 2
      else if (code >= 0xd800 && code <= 0xdbff && index + 1 < text.length && text.charCodeAt(index + 1) >= 0xdc00 && text.charCodeAt(index + 1) <= 0xdfff) { count += 4; index++ }
      else count += 3
    }
    return count
  }
  function markupBytes(root: Element): number {
    let count = 0
    for (const element of [root, ...root.querySelectorAll("*")]) for (const attr of element.attributes) count += bytes(attr.name) + bytes(attr.value)
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
    while (walker.nextNode()) count += bytes(walker.currentNode.nodeValue ?? "")
    return count
  }
  const staticBytes = markupBytes(document.documentElement)
  const repeatBytes = repeats.map(repeat => markupBytes(repeat.template))
  function binding(element: Element, feeds: Map<string, DashboardCollection>, repeat?: typeof repeats[number], rowId?: string) {
    const source = element.closest("[data-source]"), rowElement = element.closest("[data-row]")
    // A clone receives the repeat root's source/row; authored inner boundaries still win.
    const collection = repeat && (!source || source === repeat.template) ? repeat.collection : source?.getAttribute("data-source")
    const row = repeat && (!rowElement || rowElement === repeat.template) ? rowId : rowElement?.getAttribute("data-row")
    const field = element.getAttribute("data-field")
    const doc = collection && validName(row) ? feeds.get(collection)?.documents.find(item => item.id === row) : undefined
    const value = doc && validField(field) && doc.value && Object.hasOwn(doc.value, field) ? doc.value[field] : undefined
    const numeric = typeof value === "number" && Number.isFinite(value) && Math.abs(value) <= Number.MAX_SAFE_INTEGER && (!Number.isInteger(value) || Number.isSafeInteger(value))
    const text = typeof value === "string" ? value : numeric || typeof value === "boolean" ? String(value) : value === null ? "—" : "Unavailable"
    // A value is attributed only when the server named its producer. Otherwise it must not look authoritative.
    const known = Boolean(doc && (doc.source_kind === "human" || doc.source_kind === "agent") && typeof doc.source_label === "string" && doc.source_label)
    const stamp = doc && typeof doc.updated_at === "string" && Number.isFinite(Date.parse(doc.updated_at)) ? `updated ${new Date(doc.updated_at).toISOString().slice(0, 16).replace("T", " ")} UTC` : "not loaded"
    const location = `${collection || "?"}/${row || "?"}.${field || "?"}`
    const note = known ? `Source: ${location} · ${doc!.source_label} · ${stamp}` : `zdroj neuveden · ${location} · ${stamp}`
    return { value, numeric, text, note, known }
  }
  function withinTextBudget(feeds: Map<string, DashboardCollection>): boolean {
    let remaining = 16 * 1024 * 1024 - staticBytes
    // Per-refresh only: never retain text from an earlier authorized dataset.
    const sizes = new Map<string, number>()
    const size = (text: string) => { let result = sizes.get(text); if (result === undefined) { result = bytes(text); sizes.set(text, result) } return result }
    const charge = (amount: number) => (remaining -= amount) >= 0
    const fieldCost = (element: Element, repeat?: typeof repeats[number], rowId?: string) => {
      const { value, numeric, text, note } = binding(element, feeds, repeat, rowId)
      // Value text (or meter aria-label), both note attributes and one provenance line.
      // Count provenance per field even when the actual renderer deduplicates it.
      // Fixed overhead also covers generated provenance attributes, the description id and meter status/style attributes.
      return size(text) + 3 * size(note) + 256 + (element instanceof HTMLMeterElement ? size(numeric ? String(value) : "Unavailable") + 11 : 0)
    }
    if (remaining < 0) return false
    for (const element of staticFields) if (!charge(fieldCost(element))) return false
    for (let index = 0; index < repeats.length; index++) {
      const repeat = repeats[index], docs = feeds.get(repeat.collection)!.documents
      if (!charge(repeatBytes[index] * docs.length)) return false
      for (const doc of docs) {
        if (!validName(doc.id)) continue
        // Over-count the clone's replacement source/row attribute values and names.
        if (!charge(size(doc.id) + size(repeat.collection) + 23)) return false
        for (const element of repeatFields[index]) if (!charge(fieldCost(element, repeat, doc.id))) return false
      }
    }
    return true
  }
  window.addEventListener("message", event => {
    const message = event.data
    if (failed || event.source !== parent || event.origin !== config.hostOrigin || !message || message.type !== "crewship-dashboard-data" || message.token !== config.token || message.code_version !== config.codeVersion || !Number.isSafeInteger(message.seq) || message.seq <= last || !Array.isArray(message.collections) || message.collections.length !== config.collections.length) return
    const feeds = new Map<string, DashboardCollection>()
    for (const feed of message.collections) {
      if (!feed || !config.collections.includes(feed.collection) || feeds.has(feed.collection) || feed.code_version !== config.codeVersion || !Array.isArray(feed.documents) || feed.documents.length > 4096) return
      feeds.set(feed.collection, feed)
    }
    const renderCost = repeats.reduce((total, repeat, index) => total + repeatCosts[index] * feeds.get(repeat.collection)!.documents.length, staticCost)
    if (renderCost > 100000 || !withinTextBudget(feeds)) {
      failed = true
      const alert = document.createElement("p"); alert.setAttribute("role", "alert")
      alert.textContent = "Dashboard exceeds the preview rendering limit"
      document.body.replaceChildren(alert)
      return
    }
    last = message.seq
    for (const repeat of repeats) {
      repeat.rows.forEach(row => row.remove()); repeat.rows = []
      for (const doc of feeds.get(repeat.collection)!.documents) {
        if (!validName(doc.id)) continue
        const row = repeat.template.cloneNode(true) as Element
        row.removeAttribute("id"); row.querySelectorAll("[id]").forEach(node => node.removeAttribute("id"))
        row.setAttribute("data-row", doc.id); row.setAttribute("data-source", repeat.collection)
        repeat.marker.parentNode?.insertBefore(row, repeat.marker); repeat.rows.push(row)
      }
    }
    provenance.replaceChildren(); hideTip()
    const notes = new Map<string, string>()
    for (const element of document.querySelectorAll("[data-field]")) {
      const { value, numeric, text, note, known } = binding(element, feeds)
      if (element instanceof HTMLMeterElement) {
        meter(element, numeric ? value as number : undefined)
        element.setAttribute("aria-label", text)
      } else element.textContent = text
      let id = notes.get(note)
      if (!id) { id = `${prefix}-${notes.size + 1}`; const line = document.createElement("p"); line.id = id; line.textContent = note; provenance.append(line); notes.set(note, id) }
      element.setAttribute("data-crewship-provenance", known ? "known" : "missing"); element.setAttribute("tabindex", "0"); element.setAttribute("aria-describedby", id)
      tipNotes.set(element, note)
    }
  })
}
