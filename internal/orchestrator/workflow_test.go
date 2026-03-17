package orchestrator

import (
	"testing"
)

func TestExpandTemplate_Sequential(t *testing.T) {
	mapping := AgentMapping{"agent": "agent-1"}
	plan, err := ExpandTemplate("sequential", mapping, nil)
	if err != nil {
		t.Fatalf("ExpandTemplate: %v", err)
	}
	if len(plan.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(plan.Tasks))
	}

	// First task should be PENDING (no deps)
	if plan.Tasks[0].Status != "PENDING" {
		t.Errorf("task 0 status = %s, want PENDING", plan.Tasks[0].Status)
	}
	// Second task should be BLOCKED (depends on first)
	if plan.Tasks[1].Status != "BLOCKED" {
		t.Errorf("task 1 status = %s, want BLOCKED", plan.Tasks[1].Status)
	}
	// All tasks should have agent-1
	for i, task := range plan.Tasks {
		if task.AgentID != "agent-1" {
			t.Errorf("task %d agentID = %s, want agent-1", i, task.AgentID)
		}
	}
}

func TestExpandTemplate_Parallel(t *testing.T) {
	mapping := AgentMapping{"agent": "agent-1", "lead": "agent-lead"}
	plan, err := ExpandTemplate("parallel", mapping, nil)
	if err != nil {
		t.Fatalf("ExpandTemplate: %v", err)
	}
	if len(plan.Tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d", len(plan.Tasks))
	}

	// First 3 tasks should be PENDING (parallel)
	for i := 0; i < 3; i++ {
		if plan.Tasks[i].Status != "PENDING" {
			t.Errorf("task %d status = %s, want PENDING", i, plan.Tasks[i].Status)
		}
	}
	// Aggregate task should be BLOCKED
	if plan.Tasks[3].Status != "BLOCKED" {
		t.Errorf("aggregate task status = %s, want BLOCKED", plan.Tasks[3].Status)
	}
	if plan.Tasks[3].AgentID != "agent-lead" {
		t.Errorf("aggregate task agent = %s, want agent-lead", plan.Tasks[3].AgentID)
	}
}

func TestExpandTemplate_DevTestLoop(t *testing.T) {
	mapping := AgentMapping{"developer": "dev-1", "tester": "test-1"}
	plan, err := ExpandTemplate("dev-test-loop", mapping, nil)
	if err != nil {
		t.Fatalf("ExpandTemplate: %v", err)
	}
	if len(plan.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(plan.Tasks))
	}

	// Develop task
	if plan.Tasks[0].AgentID != "dev-1" {
		t.Errorf("develop agent = %s, want dev-1", plan.Tasks[0].AgentID)
	}
	if plan.Tasks[0].MaxIterations == nil || *plan.Tasks[0].MaxIterations != 3 {
		t.Errorf("develop max_iterations = %v, want 3", plan.Tasks[0].MaxIterations)
	}

	// Test task
	if plan.Tasks[1].AgentID != "test-1" {
		t.Errorf("test agent = %s, want test-1", plan.Tasks[1].AgentID)
	}
	if plan.Tasks[1].Status != "BLOCKED" {
		t.Errorf("test status = %s, want BLOCKED", plan.Tasks[1].Status)
	}
}

func TestExpandTemplate_Unknown(t *testing.T) {
	_, err := ExpandTemplate("nonexistent", nil, nil)
	if err == nil {
		t.Error("expected error for unknown template")
	}
}

func TestExpandTemplate_WithOverrides(t *testing.T) {
	mapping := AgentMapping{"agent": "agent-1"}
	overrides := map[string]string{
		"step-1": "Fetch user data from API",
		"step-2": "Transform and clean data",
	}
	plan, err := ExpandTemplate("sequential", mapping, overrides)
	if err != nil {
		t.Fatalf("ExpandTemplate: %v", err)
	}
	if plan.Tasks[0].Title != "Fetch user data from API" {
		t.Errorf("task 0 title = %q, want override", plan.Tasks[0].Title)
	}
	if plan.Tasks[1].Title != "Transform and clean data" {
		t.Errorf("task 1 title = %q, want override", plan.Tasks[1].Title)
	}
}

func TestListTemplates(t *testing.T) {
	templates := ListTemplates()
	if len(templates) < 3 {
		t.Errorf("expected at least 3 templates, got %d", len(templates))
	}
	found := false
	for _, tmpl := range templates {
		if tmpl["name"] == "dev-test-loop" {
			found = true
		}
	}
	if !found {
		t.Error("dev-test-loop template not found in list")
	}
}
