/** A single step in a workflow template, optionally bound to an agent role with dependency tracking. */
export interface TemplateStep {
  id: string
  title: string
  description?: string
  agent_role?: string
  agent_slug?: string
  depends_on?: string[]
  max_iterations?: number
  loop_back_to?: string
}

/** The JSON-serializable definition of a workflow template (name, description, steps). */
export interface TemplateDefinition {
  name: string
  description: string
  steps: TemplateStep[]
}

/** A reusable workflow template stored at workspace level, defining multi-step agent workflows. */
export interface WorkflowTemplate {
  id: string
  workspace_id: string
  name: string
  description: string | null
  template_json: TemplateDefinition
  icon: string | null
  color: string | null
  is_builtin: boolean
  created_at: string
  updated_at: string
}
