/**
 * Human names for what a person may do with a shared resource. The server
 * stores bare operation ids (`discover`, `delete_issues`); the screen says
 * what they let someone do. Kind and operation together, because the same id
 * means different things ("manage" a crew, "manage" a project).
 */

export type ResourceKind = "crew" | "agent" | "project" | "routine"

export const RESOURCE_KIND_NAME: Record<ResourceKind, string> = {
  crew: "Crew", agent: "Agent", project: "Project", routine: "Routine",
}

const OPERATION_NAME: Record<ResourceKind, Record<string, string>> = {
  crew: { view: "See it", use: "Use it", manage: "Manage it" },
  agent: { discover: "Find it", chat: "Chat with it", run: "Run it", delegate: "Delegate to it" },
  project: {
    list: "See it in lists", read: "Read it", contribute: "Contribute to it", manage: "Manage it",
    delete_issues: "Delete its issues", write: "Change its files", delete: "Delete its files",
  },
  routine: { view: "See it", results: "View runs and outputs", run: "Run it", edit: "Edit its draft", publish: "Publish it" },
}

/** An operation id nobody has named yet still reads as words, not as an id. */
function humanize(id: string): string {
  const words = id.replace(/[_.:-]+/g, " ").trim()
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : id
}

export function operationName(kind: string, operation: string): string {
  return OPERATION_NAME[kind as ResourceKind]?.[operation] ?? humanize(operation)
}

/** "Agent · Scout · Chat with it" pieces, for a grant row. */
export function kindName(kind: string): string {
  return RESOURCE_KIND_NAME[kind as ResourceKind] ?? humanize(kind)
}
