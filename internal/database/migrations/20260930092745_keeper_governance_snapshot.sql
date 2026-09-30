-- Keeper watchdog defaults become a template (docs/api-reference/admin.mdx,
-- "Defaults for new workspaces"). Until now a workspace with no
-- keeper_governance_settings row read the instance defaults
-- (app_settings 'keeper.governance_defaults') live; from now on a workspace
-- gets its own copy when it is created, and a workspace with no row reads the
-- built-in opt-out. So that no existing workspace changes under that switch,
-- every live workspace without a row gets one now, holding what it runs on
-- today: the saved defaults, or the built-in opt-out where none were saved.
-- The security contact and the judge's vault credential are never defaults.
INSERT INTO keeper_governance_settings
    (workspace_id, enabled, deny_notify_min_risk, watch_spec, watch_presets, require_second_approver,
     gov_model_provider, gov_model_id, auto_lease_seconds, behavior_sample_every)
SELECT
    w.id,
    CASE WHEN json_extract(d.value, '$.enabled') THEN 1 ELSE 0 END,
    COALESCE(json_extract(d.value, '$.deny_notify_min_risk'), 7),
    COALESCE(json_extract(d.value, '$.watch_spec'), ''),
    CASE WHEN json_array_length(json_extract(d.value, '$.watch_presets')) > 0
         THEN json_extract(d.value, '$.watch_presets') ELSE '' END,
    CASE WHEN json_extract(d.value, '$.require_second_approver') THEN 1 ELSE 0 END,
    COALESCE(json_extract(d.value, '$.gov_model_provider'), ''),
    COALESCE(json_extract(d.value, '$.gov_model_id'), ''),
    COALESCE(json_extract(d.value, '$.auto_lease_seconds'), 0),
    COALESCE(json_extract(d.value, '$.behavior_sample_every'), 0)
FROM workspaces w
LEFT JOIN app_settings d ON d.key = 'keeper.governance_defaults'
WHERE w.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM keeper_governance_settings g WHERE g.workspace_id = w.id);
