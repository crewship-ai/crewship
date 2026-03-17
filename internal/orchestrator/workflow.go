package orchestrator

import (
	"encoding/json"
	"fmt"
)

// WorkflowTemplate defines a reusable execution pattern for missions.
// Templates generate task structures that the MissionEngine executes.
type WorkflowTemplate struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Steps       []TemplateStep `json:"steps"`
}

// TemplateStep is one step in a workflow template.
// Steps can reference agent roles (resolved at planning time) or specific slugs.
type TemplateStep struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Description   string   `json:"description,omitempty"`
	AgentRole     string   `json:"agent_role,omitempty"` // role-based: "developer", "tester", etc.
	AgentSlug     string   `json:"agent_slug,omitempty"` // slug-based: specific agent
	DependsOn     []string `json:"depends_on,omitempty"`
	MaxIterations int      `json:"max_iterations,omitempty"`
	LoopBackTo    string   `json:"loop_back_to,omitempty"` // on failure, restart from this step
}

// BuiltinTemplates are predefined workflow patterns available to all workspaces.
var BuiltinTemplates = map[string]WorkflowTemplate{
	"sequential": {
		Name:        "sequential",
		Description: "Tasks execute one after another in order",
		Steps: []TemplateStep{
			{ID: "step-1", Title: "Step 1", AgentRole: "agent"},
			{ID: "step-2", Title: "Step 2", AgentRole: "agent", DependsOn: []string{"step-1"}},
			{ID: "step-3", Title: "Step 3", AgentRole: "agent", DependsOn: []string{"step-2"}},
		},
	},
	"parallel": {
		Name:        "parallel",
		Description: "All tasks run simultaneously, then results are aggregated",
		Steps: []TemplateStep{
			{ID: "task-a", Title: "Parallel Task A", AgentRole: "agent"},
			{ID: "task-b", Title: "Parallel Task B", AgentRole: "agent"},
			{ID: "task-c", Title: "Parallel Task C", AgentRole: "agent"},
			{ID: "aggregate", Title: "Aggregate Results", AgentRole: "lead", DependsOn: []string{"task-a", "task-b", "task-c"}},
		},
	},
	"dev-test-loop": {
		Name:        "dev-test-loop",
		Description: "Developer writes code, tester reviews. On failure, loops back to developer (max 3 iterations)",
		Steps: []TemplateStep{
			{ID: "develop", Title: "Implement feature", AgentRole: "developer", MaxIterations: 3},
			{ID: "test", Title: "Test and review", AgentRole: "tester", DependsOn: []string{"develop"}, LoopBackTo: "develop", MaxIterations: 3},
		},
	},
	"pipeline": {
		Name:        "pipeline",
		Description: "Sequential stage followed by parallel tasks, then final aggregation",
		Steps: []TemplateStep{
			{ID: "prepare", Title: "Prepare", AgentRole: "agent"},
			{ID: "work-a", Title: "Work Stream A", AgentRole: "agent", DependsOn: []string{"prepare"}},
			{ID: "work-b", Title: "Work Stream B", AgentRole: "agent", DependsOn: []string{"prepare"}},
			{ID: "finalize", Title: "Finalize", AgentRole: "lead", DependsOn: []string{"work-a", "work-b"}},
		},
	},
}

// TaskPlan is a concrete set of mission tasks generated from a template.
type TaskPlan struct {
	Tasks []PlannedTask `json:"tasks"`
}

// PlannedTask is a task ready to be inserted into the mission_tasks table.
type PlannedTask struct {
	TempID        string   `json:"temp_id"`
	Title         string   `json:"title"`
	Description   string   `json:"description,omitempty"`
	AgentID       string   `json:"agent_id,omitempty"`
	TaskOrder     int      `json:"task_order"`
	DependsOn     []string `json:"depends_on,omitempty"`
	MaxIterations *int     `json:"max_iterations,omitempty"`
	Status        string   `json:"status"`
}

// AgentMapping maps template role names to actual agent IDs.
type AgentMapping map[string]string // role -> agent_id

// ExpandTemplate generates a concrete task plan from a workflow template
// and a mapping of roles to actual agents.
func ExpandTemplate(templateName string, mapping AgentMapping, taskOverrides map[string]string) (*TaskPlan, error) {
	tmpl, ok := BuiltinTemplates[templateName]
	if !ok {
		return nil, fmt.Errorf("unknown workflow template: %s", templateName)
	}

	return expandWorkflow(tmpl, mapping, taskOverrides)
}

// ExpandCustomTemplate generates a task plan from a custom (user-provided) template JSON.
func ExpandCustomTemplate(templateJSON string, mapping AgentMapping) (*TaskPlan, error) {
	var tmpl WorkflowTemplate
	if err := json.Unmarshal([]byte(templateJSON), &tmpl); err != nil {
		return nil, fmt.Errorf("invalid template JSON: %w", err)
	}
	return expandWorkflow(tmpl, mapping, nil)
}

func expandWorkflow(tmpl WorkflowTemplate, mapping AgentMapping, taskOverrides map[string]string) (*TaskPlan, error) {
	plan := &TaskPlan{}

	for i, step := range tmpl.Steps {
		task := PlannedTask{
			TempID:    step.ID,
			Title:     step.Title,
			TaskOrder: i + 1,
			Status:    "PENDING",
		}

		if step.Description != "" {
			task.Description = step.Description
		}

		// Override title if provided
		if taskOverrides != nil {
			if override, ok := taskOverrides[step.ID]; ok {
				task.Title = override
			}
		}

		// Resolve agent: prefer slug, fall back to role mapping
		if step.AgentSlug != "" {
			task.AgentID = step.AgentSlug // caller must resolve slug to ID
		} else if step.AgentRole != "" {
			if agentID, ok := mapping[step.AgentRole]; ok {
				task.AgentID = agentID
			}
		}

		// Set dependencies
		if len(step.DependsOn) > 0 {
			task.DependsOn = step.DependsOn
			task.Status = "BLOCKED"
		}

		if step.MaxIterations > 0 {
			mi := step.MaxIterations
			task.MaxIterations = &mi
		}

		plan.Tasks = append(plan.Tasks, task)
	}

	return plan, nil
}

// ListTemplates returns all available builtin template names and descriptions.
func ListTemplates() []map[string]string {
	var result []map[string]string
	for name, tmpl := range BuiltinTemplates {
		result = append(result, map[string]string{
			"name":        name,
			"description": tmpl.Description,
		})
	}
	return result
}
