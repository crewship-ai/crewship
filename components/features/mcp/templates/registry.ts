import {
  GitBranch, Mail, Hash, Folder, Database, Bug,
  Search, ListChecks, BookOpen, CreditCard, Activity, Cloud,
} from "lucide-react"
import type { MCPTemplate } from "../types"

// --- Provider imports (add new providers here) ---
import { github } from "./providers/github"
import { googleWorkspace } from "./providers/google-workspace"
import { slack } from "./providers/slack"
import { filesystem } from "./providers/filesystem"
import { postgres } from "./providers/postgres"
import { sentry } from "./providers/sentry"
import { braveSearch } from "./providers/brave-search"
import { supabase } from "./providers/supabase"
import { linear } from "./providers/linear"
import { notion } from "./providers/notion"
import { stripe } from "./providers/stripe"
import { datadog } from "./providers/datadog"
import { cloudflare } from "./providers/cloudflare"
import { gitlab } from "./providers/gitlab"

// ---------------------------------------------------------------------------
// Template registry — order determines display order in the UI
// ---------------------------------------------------------------------------

export const MCP_TEMPLATES: MCPTemplate[] = [
  github,
  googleWorkspace,
  slack,
  braveSearch,
  linear,
  notion,
  stripe,
  supabase,
  postgres,
  datadog,
  filesystem,
  sentry,
  cloudflare,
  gitlab,
]

// ---------------------------------------------------------------------------
// Icon map — maps template icon strings to lucide-react components
// ---------------------------------------------------------------------------

export const TEMPLATE_ICONS: Record<string, React.ComponentType<{ className?: string }>> = {
  github: GitBranch,
  "git-branch": GitBranch,
  mail: Mail,
  hash: Hash,
  folder: Folder,
  database: Database,
  bug: Bug,
  search: Search,
  "list-checks": ListChecks,
  "book-open": BookOpen,
  "credit-card": CreditCard,
  activity: Activity,
  cloud: Cloud,
}
