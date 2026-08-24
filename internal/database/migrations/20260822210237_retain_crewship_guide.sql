-- The onboarding Setup Guide is now the workspace's permanent Crewship Guide.
-- Older builds soft-deleted the reserved crew and agent on completion. Revive
-- those rows in place so their stable chat id and conversation history survive
-- the upgrade; ensureOnboardingSetupCrew refreshes the full authored prompt and
-- selected model on the next status/start call.
UPDATE crews
SET name = 'Crewship Guide',
    description = 'Crewship''s built-in workspace guide. It helps configure crews, routines, Pages, integrations, and declarative manifests during onboarding and afterwards.',
    deleted_at = NULL,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE kind = 'setup' AND slug = '_crewship-setup';

UPDATE agents
SET name = 'Crewship Guide',
    role_title = 'Crewship Specialist',
    memory_enabled = 1,
    suggested_prompts = 'Design a crew for my workflow
Create a routine from this recurring task
Design a Crewship Page for these metrics
Explain or review my Crewship YAML manifest',
    deleted_at = NULL,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE slug = '_crewship-setup-guide';

UPDATE chats
SET title = 'Crewship Guide',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE agent_id IN (SELECT id FROM agents WHERE slug = '_crewship-setup-guide');
