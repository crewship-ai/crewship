/**
 * The sentences folder sharing is said with (#2533). Every surface reads
 * from here, so the words are pinned once: the spec's exact sentence above
 * the manager's table, the marker a non-manager gets instead, and the two
 * shapes of "After the move".
 */
import { describe, expect, it } from "vitest"

import {
  FOLDER_ACL_SENTENCE,
  MOVE_IMPACT_UNFILED,
  describeOwnPath,
  folderSharingSentence,
  joinNames,
  moveImpactFromAcl,
  moveImpactFromShared,
  ownPathsSentence,
  toFolderShared,
  type FolderAclEntry,
} from "@/lib/pages/folder-sharing"

const entry = (over: Partial<FolderAclEntry>): FolderAclEntry => ({
  subjectType: "crew",
  subjectId: "support",
  label: "Support",
  canWrite: false,
  setBy: null,
  setAt: null,
  ...over,
})

describe("the marker", () => {
  it("reads the wire's three words and nothing else", () => {
    expect(toFolderShared("crew")).toBe("crew")
    expect(toFolderShared("workspace")).toBe("workspace")
    expect(toFolderShared("none")).toBe("none")
    expect(toFolderShared(undefined)).toBe("none")
    expect(toFolderShared("everyone")).toBe("none")
  })

  it("is one of three sentences", () => {
    expect(folderSharingSentence("none")).toBe("Only the owning crew")
    expect(folderSharingSentence("crew")).toBe("Shared with a crew")
    expect(folderSharingSentence("workspace")).toBe("Shared with everyone in this workspace")
  })

  it("keeps the spec's sentence above the table verbatim", () => {
    expect(FOLDER_ACL_SENTENCE).toBe(
      "Folder permissions apply to every page that is in the folder right now, including pages added later. Panels owned by another crew stay sealed. Anyone who can edit can also remove pages from the folder.",
    )
  })
})

describe("After the move, with names", () => {
  it("lists viewers, then editors, and never repeats an editor among the viewers", () => {
    expect(
      moveImpactFromAcl([
        entry({ subjectType: "crew", subjectId: "support", label: "Support" }),
        entry({ subjectType: "workspace", subjectId: "", label: "Everyone in this workspace" }),
        entry({ subjectType: "crew", subjectId: "ops", label: "Ops", canWrite: true }),
      ]),
    ).toBe("Visible to crew Support and everyone in this workspace; crew Ops can edit.")
  })

  it("has a shape for viewers only, editors only, and nobody", () => {
    expect(moveImpactFromAcl([entry({ subjectType: "user", subjectId: "u1", label: "ada@example.com" })])).toBe(
      "Visible to ada@example.com.",
    )
    expect(moveImpactFromAcl([entry({ canWrite: true }), entry({ subjectType: "user", subjectId: "u1", label: "ada@example.com", canWrite: true })])).toBe(
      "Crew Support and ada@example.com can view and edit.",
    )
    expect(moveImpactFromAcl([])).toBe("Visible to the owning crew only.")
  })

  it("joins three names with commas and an and", () => {
    expect(joinNames(["a", "b", "c"])).toBe("a, b and c")
    expect(joinNames(["a"])).toBe("a")
    expect(joinNames([])).toBe("")
  })
})

describe("After the move, without names", () => {
  it("is built from the marker alone", () => {
    expect(moveImpactFromShared("workspace")).toBe("Visible to everyone in this workspace.")
    expect(moveImpactFromShared("crew")).toBe("Visible to members of the shared crews.")
    expect(moveImpactFromShared("none")).toBe("Visible to the owning crew only.")
    expect(MOVE_IMPACT_UNFILED).toBe("No folder permissions apply; the page's own access stays as it is.")
  })
})

describe("the caller's own paths", () => {
  it("turns the server's words into phrases and keeps an unknown word as it came", () => {
    expect(describeOwnPath("owner")).toBe("you own it")
    expect(describeOwnPath("role")).toBe("your workspace role")
    expect(describeOwnPath("crew:ops")).toBe("your crew ops")
    expect(describeOwnPath("panel_crew:ops")).toBe("a panel owned by your crew ops")
    expect(describeOwnPath("panel_crew:withheld")).toBe("a panel owned by a crew you are in")
    expect(describeOwnPath("grant")).toBe("a grant on the page")
    expect(describeOwnPath("grant:page:write")).toBe("a write grant on the page")
    expect(describeOwnPath("folder:ops")).toBe("the folder ops")
    expect(describeOwnPath("telepathy")).toBe("telepathy")
  })

  it("is one sentence, or says there is none", () => {
    expect(ownPathsSentence(["owner", "folder:ops"])).toBe("You reach it through you own it and the folder ops.")
    expect(ownPathsSentence([])).toBe("You have no path of your own to this page.")
  })
})
