import { expect, it } from "vitest"
import { supportsPageApplications } from "../runtime-support"
it("enables desktop Chromium and keeps unverified engines and mobile browsers out", () => {
 for (const agent of ["Chrome/140 Safari/537.36", "Chrome/140 Safari/537.36 Edg/140", "HeadlessChrome/140"]) expect(supportsPageApplications(agent)).toBe(true)
 for (const agent of ["Firefox/140", "Version/18 Safari/605", "CriOS/140 Mobile/15 Safari/605", "Android Chrome/140", ""]) expect(supportsPageApplications(agent)).toBe(false)
})
