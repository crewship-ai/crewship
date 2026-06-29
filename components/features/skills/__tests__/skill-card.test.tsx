import { describe, expect, it, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { SkillCard, type SkillCardData } from "@/components/features/skills/skill-card"

// Minimal factory that fills only the fields the card actually reads.
// The card type widens with optional fields per Sprint 1; existing rows
// without those should still render — covered by the "legacy row"
// fixture below.
function makeSkill(overrides: Partial<SkillCardData> = {}): SkillCardData {
  return {
    id: "sk_1",
    name: "Pdf Extract",
    slug: "pdf-extract",
    display_name: "Pdf Extract",
    description: "Use when the user asks to extract structured data from PDF files.",
    version: "1.0.0",
    author: "anthropic",
    category: "CODING",
    source: "BUNDLED",
    icon: null,
    vendor: "anthropic",
    maturity: "OFFICIAL",
    runtime: "INSTRUCTIONS",
    scan_status: "CLEAN",
    description_quality: null,
    downloads: 23400,
    featured: false,
    updated_at: new Date(Date.now() - 3 * 86_400_000).toISOString(),
    ...overrides,
  }
}

describe("SkillCard", () => {
  it("renders the seven canonical fields when all metadata is present", () => {
    render(<SkillCard skill={makeSkill()} />)
    expect(screen.getByText("anthropic/")).toBeDefined()
    expect(screen.getByText("Pdf Extract")).toBeDefined()
    expect(screen.getByText(/extract structured data/i)).toBeDefined()
    expect(screen.getByText("Coding")).toBeDefined()
    expect(screen.getByText("Official")).toBeDefined()
    // 23.4k installs (formatted)
    // 23400 lands in the >=10k branch, which collapses the decimal
    // to keep the chip compact at large install counts.
    expect(screen.getByText(/23k installs/i)).toBeDefined()
    // updated relative — 3d ago
    expect(screen.getByText(/3d ago/i)).toBeDefined()
  })

  it("hides the maturity badge for OFFICIAL skills (silent default)", () => {
    render(<SkillCard skill={makeSkill({ maturity: "OFFICIAL" })} />)
    expect(screen.queryByText("Beta")).toBeNull()
    expect(screen.queryByText("Experimental")).toBeNull()
    expect(screen.queryByText("Curated")).toBeNull()
  })

  it("shows the maturity badge for non-OFFICIAL skills", () => {
    render(<SkillCard skill={makeSkill({ maturity: "EXPERIMENTAL" })} />)
    expect(screen.getByText("Experimental")).toBeDefined()
  })

  it("shows the Generated source badge for agent-authored / memory-promoted skills", () => {
    // An approved agent-authored skill carries source=GENERATED so the catalog
    // visibly marks it as machine-originated (and the Generated tab surfaces it)
    // rather than letting it look like a hand-made import.
    render(<SkillCard skill={makeSkill({ source: "GENERATED" })} />)
    expect(screen.getByText("Generated")).toBeDefined()
  })

  it("shows the FLAGGED chip when scan_status is FLAGGED", () => {
    render(<SkillCard skill={makeSkill({ scan_status: "FLAGGED" })} />)
    expect(screen.getByText("Flagged")).toBeDefined()
  })

  it("hides install count when zero", () => {
    render(<SkillCard skill={makeSkill({ downloads: 0 })} />)
    expect(screen.getByText(/0 installs/i)).toBeDefined()
  })

  it("calls onSelect with the skill when clicked", () => {
    const onSelect = vi.fn()
    render(<SkillCard skill={makeSkill()} onSelect={onSelect} />)
    fireEvent.click(screen.getByRole("button"))
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect.mock.calls[0][0]?.id).toBe("sk_1")
  })

  it("falls back gracefully for legacy rows without v65 metadata", () => {
    // Row written before migration v65: no vendor/maturity/runtime/scan_status.
    // Card must still render and substitute "community" for the missing
    // vendor (the visible default we ship for user-imported skills).
    render(
      <SkillCard
        skill={makeSkill({
          vendor: null,
          maturity: null,
          runtime: null,
          scan_status: null,
          description_quality: null,
        })}
      />,
    )
    expect(screen.getByText("community/")).toBeDefined()
    expect(screen.queryByText("Flagged")).toBeNull()
  })
})
