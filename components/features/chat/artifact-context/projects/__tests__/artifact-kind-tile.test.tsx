import { cleanup, render } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import { ARTIFACT_KINDS, KIND_ACCENT } from "../../new-artifact/artifact-kinds"
import { ArtifactKindBadge, ArtifactKindTile } from "../artifact-kind-tile"

afterEach(cleanup)

it.each(ARTIFACT_KINDS.map(k => [k.key, k.label] as const))("draws the %s tile in its registry colour and names it %s", (key, label) => {
  const { container } = render(<><ArtifactKindTile kind={key} /><ArtifactKindBadge kind={key} /></>)
  const tile = container.querySelector(`[data-kind="${key}"]`)!
  expect(tile.getAttribute("aria-hidden")).toBe("true")
  for (const cls of `${KIND_ACCENT[key].chip} ${KIND_ACCENT[key].fg}`.split(" ")) expect(tile.className).toContain(cls)
  expect(tile.querySelector("svg")).not.toBeNull()
  expect(container.textContent).toBe(label)
})

it("an unknown kind gets a neutral tile and no colour that claims a kind", () => {
  const { container } = render(<ArtifactKindTile kind={null} />)
  const tile = container.querySelector("[data-kind]")!
  expect(tile.getAttribute("data-kind")).toBe("unknown")
  expect(tile.className).toContain("border-dashed")
  for (const k of ARTIFACT_KINDS) expect(tile.className).not.toContain(KIND_ACCENT[k.key].chip)
})
