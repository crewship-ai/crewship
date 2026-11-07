INSERT INTO feature_flags(id,key,description,enabled,created_at,updated_at)
 VALUES('ff_artifact_interactive','artifact_interactive','Interactive artifacts: Project scripts in the isolated runtime with brokered data access',0,datetime('now'),datetime('now'))
 ON CONFLICT(key) DO NOTHING;
