-- Restricted context has explicit durable attempt provenance. Legacy shared
-- conversation/episodic/persona data is deliberately not imported.
CREATE TABLE access_context (
 id TEXT PRIMARY KEY,
 attempt_id TEXT NOT NULL REFERENCES access_attempts(id),
 scope TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('history','memory','summary')),
 role TEXT NOT NULL CHECK(role IN ('user','assistant','derived')),
 content TEXT NOT NULL CHECK(length(content)<=32768),
 sources TEXT NOT NULL DEFAULT '[]',
 created_at TEXT NOT NULL,
 CHECK((kind='history' AND role IN ('user','assistant') AND sources='[]') OR (kind IN ('memory','summary') AND role='derived' AND sources<>'[]'))
);
CREATE INDEX access_context_scope ON access_context(scope,agent_id,created_at,id);
CREATE TRIGGER access_context_immutable BEFORE UPDATE ON access_context BEGIN
 SELECT RAISE(ABORT,'restricted context is immutable');
END;
