import {
  GitBranch, Mail, Hash, Folder, Database, Bug,
} from "lucide-react"
import type { MCPTemplate } from "../types"

// --- Provider imports (add new providers here) ---
import { github } from "./providers/github"
import { googleWorkspace } from "./providers/google-workspace"
import { slack } from "./providers/slack"
import { filesystem } from "./providers/filesystem"
import { postgres } from "./providers/postgres"
import { sentry } from "./providers/sentry"

// ---------------------------------------------------------------------------
// Template registry — order determines display order in the UI
// ---------------------------------------------------------------------------

export const MCP_TEMPLATES: MCPTemplate[] = [
  github,
  googleWorkspace,
  slack,
  filesystem,
  postgres,
  sentry,
]

// ---------------------------------------------------------------------------
// Icon map — maps template icon strings to lucide-react components
// ---------------------------------------------------------------------------

export const TEMPLATE_ICONS: Record<string, React.ComponentType<{ className?: string }>> = {
  github: GitBranch,
  mail: Mail,
  hash: Hash,
  folder: Folder,
  database: Database,
  bug: Bug,
}
