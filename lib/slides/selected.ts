import { canonicalSlideElement, slideElementHashDomain } from "./model"
import type { SlidesState } from "./mutations"

export interface SlideTextRange { slide_id: string; element_id: string; from: number; to: number; quote: string }
export interface SelectedTextRange extends SlideTextRange { selection_schema_version: 1; kind: "text_range"; element_hash: string }
export interface SelectedAdmission { page_id: string; type_id: "slides"; schema_version: 1; base_rev: number; submission_id: string; chat_id: string; instruction: string; selection: SelectedTextRange }
export interface SelectedSummary { id: string; page_id: string; schema_version: 1; version: number; status: "queued" | "running" | "succeeded" | "failed" | "cancelled" | "interrupted"; base_revision: number; base_schema_version: 1 | 2; base_content_hash: string; selection: SelectedTextRange; instruction: string; human_actor: string; agent_actor: string; original_run_id: string; result_revision?: number; result_hash?: string; error: string; restored: boolean; created_at: string; updated_at: string }
export interface SelectedRequestPage { items: SelectedSummary[]; next_cursor: string; watermark: number; has_more: boolean; can_dispatch: boolean; can_cancel: boolean; eligible_chats: { chat_id: string; agent_name: string; title: string }[] }
const id = (value: unknown): value is string => typeof value === "string" && /^[a-zA-Z0-9_-]{1,128}$/.test(value)
const hash = (value: unknown) => typeof value === "string" && /^[a-f0-9]{64}$/.test(value)
const positive = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) > 0
const splitSurrogate = (value: string, index: number) => index > 0 && index < value.length && value.charCodeAt(index - 1) >= 0xd800 && value.charCodeAt(index - 1) <= 0xdbff && value.charCodeAt(index) >= 0xdc00 && value.charCodeAt(index) <= 0xdfff
export function validSelectedRange(value: unknown): value is SelectedTextRange {
 if (!value || typeof value !== "object") return false
 const v = value as SelectedTextRange
 return v.selection_schema_version === 1 && v.kind === "text_range" && id(v.slide_id) && id(v.element_id) && hash(v.element_hash) && Number.isSafeInteger(v.from) && v.from >= 0 && positive(v.to) && v.to > v.from && v.to - v.from <= 4096 && typeof v.quote === "string" && v.quote.length === v.to - v.from
}
export async function captureSlideTextRange(saved: SlidesState, range: SlideTextRange, ready: boolean): Promise<{ base_rev: number; page_id: string; selection: SelectedTextRange }> {
 if (!ready) throw new Error("Save or discard unsaved changes before selecting agent work.")
 const element = saved.deck.slides.find(slide => slide.id === range.slide_id)?.elements.find(item => item.id === range.element_id)
 if (!element || element.type !== "text" || !Number.isSafeInteger(range.from) || !Number.isSafeInteger(range.to) || range.from < 0 || range.to <= range.from || range.to > element.text.value.length || range.to - range.from > 4096 || splitSurrogate(element.text.value, range.from) || splitSurrogate(element.text.value, range.to) || element.text.value.slice(range.from, range.to) !== range.quote) throw new Error("Select a nonempty range of at most 4096 UTF16 units in the saved text field.")
 const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(slideElementHashDomain(element) + canonicalSlideElement(element)))
 const element_hash = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("")
 return { base_rev: saved.revision, page_id: saved.deck.deck_id, selection: { ...range, selection_schema_version: 1, kind: "text_range", element_hash } }
}
export function validSelectedSummary(value: unknown, page: string): value is SelectedSummary {
 if (!value || typeof value !== "object") return false
 const v = value as SelectedSummary
 return id(v.id) && v.page_id === page && v.schema_version === 1 && positive(v.version) && v.version <= 8 && ["queued", "running", "succeeded", "failed", "cancelled", "interrupted"].includes(v.status) && positive(v.base_revision) && [1, 2].includes(v.base_schema_version) && hash(v.base_content_hash) && validSelectedRange(v.selection) && typeof v.instruction === "string" && typeof v.error === "string" && typeof v.restored === "boolean" && typeof v.created_at === "string" && typeof v.updated_at === "string" && (v.result_revision === undefined ? v.result_hash === undefined : v.result_revision === v.base_revision + 1 && hash(v.result_hash))
}
export function validSelectedPage(value: unknown, page: string): value is SelectedRequestPage {
 if (!value || typeof value !== "object") return false
 const v = value as SelectedRequestPage
 return Array.isArray(v.items) && v.items.length <= 100 && v.items.every(item => validSelectedSummary(item, page)) && typeof v.next_cursor === "string" && v.next_cursor.length <= 1024 && Number.isSafeInteger(v.watermark) && v.watermark >= 0 && typeof v.has_more === "boolean" && (!v.has_more || !!v.next_cursor) && typeof v.can_cancel === "boolean" && typeof v.can_dispatch === "boolean" && Array.isArray(v.eligible_chats) && v.eligible_chats.length <= 50 && v.eligible_chats.every(chat => id(chat.chat_id) && typeof chat.agent_name === "string" && typeof chat.title === "string")
}
