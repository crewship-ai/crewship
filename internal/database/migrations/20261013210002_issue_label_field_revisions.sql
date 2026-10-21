-- Label membership is editable issue state. Advance the CAS field revision,
-- not only the aggregate revision, so simultaneous label editors conflict.
DROP TRIGGER mission_labels_aggregate_insert;
DROP TRIGGER mission_labels_aggregate_update;
DROP TRIGGER mission_labels_aggregate_delete;
CREATE TRIGGER mission_labels_aggregate_insert AFTER INSERT ON mission_labels
BEGIN
 UPDATE missions SET revision=revision+1,aggregate_revision=aggregate_revision+1 WHERE id=NEW.mission_id;
END;
CREATE TRIGGER mission_labels_aggregate_update AFTER UPDATE ON mission_labels
WHEN NEW.mission_id IS NOT OLD.mission_id OR NEW.label_id IS NOT OLD.label_id
BEGIN
 UPDATE missions SET revision=revision+1,aggregate_revision=aggregate_revision+1 WHERE id IN (NEW.mission_id,OLD.mission_id);
END;
CREATE TRIGGER mission_labels_aggregate_delete AFTER DELETE ON mission_labels
BEGIN
 UPDATE missions SET revision=revision+1,aggregate_revision=aggregate_revision+1 WHERE id=OLD.mission_id;
END;
