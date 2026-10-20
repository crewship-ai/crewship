import { test } from "@playwright/test"

// Specs that write (send messages, create conversations) must run against a
// disposable instance. scripts/throwaway-server.sh `run` passes
// CREWSHIP_THROWAWAY_MANIFEST to its command; without it the spec is skipped
// instead of touching whatever server PLAYWRIGHT_BASE_URL points at.
export function requireThrowawayServer(): void {
  test.skip(
    !process.env.CREWSHIP_THROWAWAY_MANIFEST,
    "writes data: run under scripts/throwaway-server.sh (CREWSHIP_THROWAWAY_MANIFEST unset)",
  )
}
