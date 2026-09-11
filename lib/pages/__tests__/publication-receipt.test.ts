import { expect, it } from "vitest"
import { publicationReceiptMessage } from "../publication-receipt"
import type { PagePublication } from "@/hooks/use-page-application"
it("distinguishes the historical operation from current publication and withdrawal", () => {
 const receipt = { version: 2, live_version: 3, is_current: false, replayed: true, published: true } as PagePublication
 expect(publicationReceiptMessage(receipt)).toBe("Version 2 was published earlier. The current live version is 3.")
 expect(publicationReceiptMessage({ ...receipt, published: false })).toContain("now withdrawn")
 expect(publicationReceiptMessage({ ...receipt, version: 3, is_current: true })).toBe("Published version 3.")
})
