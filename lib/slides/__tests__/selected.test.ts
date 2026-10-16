import { webcrypto } from "node:crypto"
import { beforeEach, expect, it, vi } from "vitest"
import fixture from "@/testdata/slides-schema-v1.json"
import type { SlideDeck } from "../model"
import type { SlidesState } from "../mutations"
import { captureSlideTextRange } from "../selected"
const saved = () => { const deck = structuredClone(fixture.deck) as SlideDeck; const slide = deck.slides[0], element = slide.elements.find(item => item.type === "text")!; if (element.type !== "text") throw new Error("text fixture"); element.text.value = "Před 🐎 český\ntext"; return { state: { schema_version: 1, revision: 1, order_version: 1, content_hash: "a".repeat(64), deck } as SlidesState, range: { slide_id: slide.id, element_id: element.id, from: 5, to: 7, quote: "🐎" } } }
beforeEach(() => vi.stubGlobal("crypto", webcrypto))
it("captures actual saved UTF16 range and stable element hash without changing original", async () => { const { state, range } = saved(); const original = structuredClone(state); const captured = await captureSlideTextRange(state, range, true); expect(captured.base_rev).toBe(1); expect(captured.selection.element_hash).toMatch(/^[a-f0-9]{64}$/); expect(captured.selection.quote).toBe("🐎"); expect(state).toEqual(original) })
it("rejects dirty base, empty range, image target and surrogate split", async () => { const { state, range } = saved(); await expect(captureSlideTextRange(state, range, false)).rejects.toThrow("Save or discard"); await expect(captureSlideTextRange(state, { ...range, to: 5, quote: "" }, true)).rejects.toThrow("nonempty"); await expect(captureSlideTextRange(state, { ...range, to: 6, quote: "\ud83d" }, true)).rejects.toThrow("nonempty"); await expect(captureSlideTextRange(state, { ...range, element_id: "image_target" }, true)).rejects.toThrow("nonempty") })
