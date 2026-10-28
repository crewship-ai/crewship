INSERT INTO feature_flags(id,key,description,enabled,created_at,updated_at)
 VALUES('ff_artifact_dashboard_data','artifact_dashboard_data','Dashboard datasets with bounded document CAS and reader-scoped projections',0,datetime('now'),datetime('now'))
 ON CONFLICT(key) DO NOTHING;
