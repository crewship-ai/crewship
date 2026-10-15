-- Internal origin bookkeeping and SQL no-ops do not change issue fields.
-- Explicit revision/aggregate maintenance never recursively bumps counters.
DROP TRIGGER missions_revision;
CREATE TRIGGER missions_revision AFTER UPDATE ON missions
WHEN NEW.revision = OLD.revision AND NEW.aggregate_revision = OLD.aggregate_revision
AND (NEW.workspace_id IS NOT OLD.workspace_id OR NEW.crew_id IS NOT OLD.crew_id
 OR NEW.lead_agent_id IS NOT OLD.lead_agent_id OR NEW.title IS NOT OLD.title
 OR NEW.description IS NOT OLD.description OR NEW.status IS NOT OLD.status
 OR NEW.plan IS NOT OLD.plan OR NEW.workflow_template IS NOT OLD.workflow_template
 OR NEW.total_token_count IS NOT OLD.total_token_count OR NEW.total_estimated_cost IS NOT OLD.total_estimated_cost
 OR NEW.completed_at IS NOT OLD.completed_at OR NEW.scope IS NOT OLD.scope
 OR NEW.proposal_id IS NOT OLD.proposal_id OR NEW.total_token_budget IS NOT OLD.total_token_budget
 OR NEW.complexity IS NOT OLD.complexity OR NEW.pattern IS NOT OLD.pattern
 OR NEW.number IS NOT OLD.number OR NEW.identifier IS NOT OLD.identifier
 OR NEW.priority IS NOT OLD.priority OR NEW.assignee_type IS NOT OLD.assignee_type
 OR NEW.assignee_id IS NOT OLD.assignee_id OR NEW.due_date IS NOT OLD.due_date
 OR NEW.sort_order IS NOT OLD.sort_order OR NEW.mission_type IS NOT OLD.mission_type
 OR NEW.project_id IS NOT OLD.project_id OR NEW.milestone_id IS NOT OLD.milestone_id
 OR NEW.estimate IS NOT OLD.estimate OR NEW.parent_issue_id IS NOT OLD.parent_issue_id
 OR NEW.routine_id IS NOT OLD.routine_id OR NEW.routine_inputs_json IS NOT OLD.routine_inputs_json
 OR NEW.owner_user_id IS NOT OLD.owner_user_id OR NEW.delegate_agent_id IS NOT OLD.delegate_agent_id
 OR NEW.created_at IS NOT OLD.created_at OR NEW.author_agent_id IS NOT OLD.author_agent_id
 OR NEW.created_by_user_id IS NOT OLD.created_by_user_id OR NEW.authored_via IS NOT OLD.authored_via)
BEGIN
 UPDATE missions SET revision=revision+1,aggregate_revision=aggregate_revision+1 WHERE id=NEW.id;
END;

-- Work policy is part of issueResponse, not merely execution internals.
CREATE TRIGGER issue_work_issue_revision_insert AFTER INSERT ON issue_work
BEGIN
 UPDATE missions SET revision=revision+1,aggregate_revision=aggregate_revision+1 WHERE id=NEW.mission_id;
END;
CREATE TRIGGER issue_work_issue_revision_update AFTER UPDATE ON issue_work
WHEN NEW.mode IS NOT OLD.mode OR NEW.worker_user_id IS NOT OLD.worker_user_id
 OR NEW.revision IS NOT OLD.revision OR NEW.note IS NOT OLD.note
 OR NEW.brief_revision IS NOT OLD.brief_revision
 OR NEW.client_review_required IS NOT OLD.client_review_required
 OR NEW.submitted_brief_revision IS NOT OLD.submitted_brief_revision
BEGIN
 UPDATE missions SET revision=revision+1,aggregate_revision=aggregate_revision+1
 WHERE id IN (NEW.mission_id,OLD.mission_id);
END;
CREATE TRIGGER issue_work_issue_revision_delete AFTER DELETE ON issue_work
BEGIN
 UPDATE missions SET revision=revision+1,aggregate_revision=aggregate_revision+1 WHERE id=OLD.mission_id;
END;
