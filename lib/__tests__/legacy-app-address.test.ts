import { describe, expect, it } from "vitest"
import { legacyAppAddress } from "../legacy-app-address"

describe("legacyAppAddress (#G04)", () => {
  it.each([
    ["/pages/brief/editor", "", "/pages/brief?mode=edit"],
    ["/pages/brief/editor/", "?workspace_id=ws1", "/pages/brief?workspace_id=ws1&mode=edit"],
    ["/pages/%40page-1/editor", "?workspace_id=ws1&version=2", "/pages/%40page-1?workspace_id=ws1&version=2&mode=edit"],
    ["/pages/brief/editor", "?mode=view", "/pages/brief?mode=edit"],
  ])("%s%s moves to the Pages editor address %s", (path, search, want) => {
    expect(legacyAppAddress(path, search)).toBe(want)
  })

  it.each([
    ["/pages/craft/editor", ""],
    ["/pages/brief", ""],
    ["/pages/a/b/editor", ""],
    ["/pages//editor", ""],
    ["/pages/brief/editors", ""],
    ["/routines/x/editor", ""],
    ["/pages/%E0%A4%A/editor", ""],
  ])("%s is not an old address", (path, search) => {
    expect(legacyAppAddress(path, search)).toBeNull()
  })
})
