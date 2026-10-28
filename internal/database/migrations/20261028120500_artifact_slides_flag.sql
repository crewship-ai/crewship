INSERT INTO feature_flags(id,key,description,enabled,created_at,updated_at)
 VALUES('ff_artifact_slides','artifact_slides','Read-only authored Slides projections and bounded vector PDF export',0,datetime('now'),datetime('now'))
 ON CONFLICT(key) DO NOTHING;
