-- Retire reciprocal workbook triggers on other content domains.
-- Content admission is enforced by the workbook adapter and its own heads.
DROP TRIGGER IF EXISTS documents_exclude_gridcraft_insert;
DROP TRIGGER IF EXISTS documents_exclude_gridcraft_update;
DROP TRIGGER IF EXISTS slides_heads_exclude_gridcraft_insert;
DROP TRIGGER IF EXISTS slides_heads_exclude_gridcraft_update;
DROP TRIGGER IF EXISTS page_panels_exclude_gridcraft_insert;
DROP TRIGGER IF EXISTS page_panels_exclude_gridcraft_update;
DROP TRIGGER IF EXISTS page_project_drafts_exclude_gridcraft_insert;
DROP TRIGGER IF EXISTS page_project_drafts_exclude_gridcraft_update;
DROP TRIGGER IF EXISTS page_project_live_exclude_gridcraft_insert;
DROP TRIGGER IF EXISTS page_project_live_exclude_gridcraft_update;
