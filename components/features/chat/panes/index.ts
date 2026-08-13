// The three folder surfaces that hang off an agent in the chat tree.
//
// Selecting Files / Asks / Memory replaces the centre column with one of
// these. Each is self-contained: it fetches its own data from endpoints that
// already exist, and owns its loading, empty and failure states. The tree
// hands it an agent and nothing else.

export { AgentFilesPane, type AgentFilesPaneProps } from "./agent-files-pane"
export { AgentAsksPane, type AgentAsksPaneProps } from "./agent-asks-pane"
export { AgentMemoryPane, type AgentMemoryPaneProps } from "./agent-memory-pane"
