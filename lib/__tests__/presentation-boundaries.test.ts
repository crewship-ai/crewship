import { expect, it } from "vitest"
import { ADAPTER_BRAND, getAdapterBrand } from "../cli-adapter-brand"
import { formatStatus } from "../format-status"
import { isComposedRun, runProvenance } from "../run-provenance"
import { navSections, phoneTabs, isHiddenForRole } from "../nav-sections"

it.each(["new-adapter", "constructor", "toString", "__proto__"])("uses a neutral usable brand for unknown adapter %s", key => {
 expect(getAdapterBrand(key)).toEqual({ fg: "#A1A1AA", bg: "rgba(161, 161, 170, 0.12)", border: "rgba(161, 161, 170, 0.40)" })
 expect(getAdapterBrand("CLAUDE_CODE")).toBe(ADAPTER_BRAND.CLAUDE_CODE)
})
it("presents blank statuses as unknown and unknown single words as readable labels", () => {
 expect(formatStatus("  -__ ")).toEqual({ label: "Unknown", tone: "muted" })
 expect(formatStatus("future")).toEqual({ label: "Future", tone: "muted" })
})
it("distinguishes direct starts from composed runs without inventing provenance", () => {
 expect(isComposedRun({})).toBe(false)
 expect(isComposedRun({ chain_depth: 0 })).toBe(false)
 expect(isComposedRun({ chain_depth: -1 })).toBe(false)
 expect(isComposedRun({ chain_depth: 2 })).toBe(true)
 expect(runProvenance({ triggered_via: "schedule", automation_name: "Triage", triggered_by_id: "schedule-id", chain_depth: 2 })).toEqual({ label: "automation", source: "Triage", chainDepth: 2 })
 expect(runProvenance({ triggered_via: "", triggered_by_id: "" })).toEqual({ label: "manual", source: undefined, chainDepth: undefined })
})
it("rejects a phone-tab configuration whose destination was removed from shared navigation", () => {
 const section = navSections.find(section => section.items.some(item => item.href === "/chat"))!
 const index = section.items.findIndex(item => item.href === "/chat")
 const [removed] = section.items.splice(index, 1)
 try { expect(() => phoneTabs()).toThrow("PHONE_TAB_HREFS names /chat") }
 finally { section.items.splice(index, 0, removed) }
 expect(phoneTabs().map(item => item.href)).toEqual(["/", "/inbox", "/chat"])
})

it.each(["OWNER", "ADMIN", "MEMBER"])("does not treat workspace role %s as instance-admin authority", role => {
 const admin = navSections.flatMap(section => section.items).find(item => item.href === "/admin")!
 const inbox = navSections.flatMap(section => section.items).find(item => item.href === "/inbox")!
 expect(isHiddenForRole(admin, role)).toBe(true)
 expect(isHiddenForRole(admin, role, false)).toBe(true)
 expect(isHiddenForRole(admin, role, true)).toBe(false)
 expect(isHiddenForRole(inbox, role, false)).toBe(false)
})
