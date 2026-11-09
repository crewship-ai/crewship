import { describe, expect, it } from "vitest"
import { designPresenceColors } from "../use-design"

describe("designPresenceColors", () => {
 it("gives everyone present a different colour, the same on every viewer", () => {
  // Two IDs whose character sums are equal collided under the old sum-modulo rule.
  const owner = "user-ab", editor = "user-ba"
  const onOwner = designPresenceColors([owner, editor])
  const onEditor = designPresenceColors([editor, owner])
  expect(onOwner.get(owner)).not.toBe(onOwner.get(editor))
  expect([...onOwner]).toEqual([...onEditor])
 })

 it("stays distinct up to the palette size and ignores a person's second tab", () => {
  const ids = Array.from({ length: 8 }, (_, i) => `u${i}`)
  const colors = designPresenceColors([...ids, "u3"])
  expect(colors.size).toBe(8)
  expect(new Set(colors.values()).size).toBe(8)
 })
})
