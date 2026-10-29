import { expect, it } from "vitest"
import { artifactCommentMessage } from "../agent-dispatch"

const comment = { id: "cm1", version: 2, path: "index.html", selector: "#plan-team", quote: "Team", text: "Make it stand out", task_id: "t1" }

it("starts the agent through the artifact project intent bound to the comment and its version", () => {
  const { text, metadata } = artifactCommentMessage("page-1", comment)
  expect(metadata).toEqual({ artifact_project_intent: { submission_id: "artifact-comment-cm1", page_id: "page-1", comment_id: "cm1", base_version: 2 } })
  for (const part of ["@page-1", "Base version: 2", "index.html", "#plan-team", "Make it stand out", "base_version 2", "comment cm1"]) expect(text).toContain(part)
})

it("uses the same submission for a retry, so the server can recognise it", () => {
  expect(artifactCommentMessage("page-1", comment).metadata).toEqual(artifactCommentMessage("page-1", comment).metadata)
})
