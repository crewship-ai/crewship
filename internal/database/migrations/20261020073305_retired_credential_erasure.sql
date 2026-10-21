-- Retain credential identity and history, never retired vault material.
-- Native guards also scrub historical normal/fork imports after migration,
-- including fields and OAuth state inserted after their retired parent.
CREATE TEMP TABLE credential_erasure_expected(id TEXT PRIMARY KEY);
INSERT INTO credential_erasure_expected SELECT id FROM credentials WHERE deleted_at IS NOT NULL;
DELETE FROM credential_fields WHERE credential_id IN (SELECT id FROM credentials WHERE deleted_at IS NOT NULL);
DELETE FROM oauth_states WHERE credential_id IN (SELECT id FROM credentials WHERE deleted_at IS NOT NULL);
UPDATE credentials SET
        encrypted_value='',
        oauth_resource='',
        oauth_issuer='',
        encrypted_refresh_token=NULL,
        oauth_client_secret_enc=NULL,
        oauth_refresh_token_enc=NULL,
        username=NULL,
        account_email=NULL,
        account_label=NULL,
        description=NULL,
        last_error=NULL,
        last_used_ips=NULL,
        tags=NULL,
        oauth_client_id=NULL,
        oauth_auth_url=NULL,
        oauth_token_url=NULL,
        oauth_scopes=NULL,
        oauth_token_expires_at=NULL,
        token_expires_at=NULL WHERE deleted_at IS NOT NULL;

CREATE TRIGGER credential_retired_scrub_insert AFTER INSERT ON credentials
WHEN NEW.deleted_at IS NOT NULL AND (
    COALESCE(NEW.encrypted_value,'')!=''
    OR COALESCE(NEW.oauth_resource,'')!=''
    OR COALESCE(NEW.oauth_issuer,'')!=''
    OR COALESCE(NEW.encrypted_refresh_token,'')!=''
    OR COALESCE(NEW.oauth_client_secret_enc,'')!=''
    OR COALESCE(NEW.oauth_refresh_token_enc,'')!=''
    OR COALESCE(NEW.username,'')!=''
    OR COALESCE(NEW.account_email,'')!=''
    OR COALESCE(NEW.account_label,'')!=''
    OR COALESCE(NEW.description,'')!=''
    OR COALESCE(NEW.last_error,'')!=''
    OR COALESCE(NEW.last_used_ips,'')!=''
    OR COALESCE(NEW.tags,'')!=''
    OR COALESCE(NEW.oauth_client_id,'')!=''
    OR COALESCE(NEW.oauth_auth_url,'')!=''
    OR COALESCE(NEW.oauth_token_url,'')!=''
    OR COALESCE(NEW.oauth_scopes,'')!=''
    OR COALESCE(NEW.oauth_token_expires_at,'')!=''
    OR COALESCE(NEW.token_expires_at,'')!=''
    OR EXISTS(SELECT 1 FROM credential_fields WHERE credential_id=NEW.id)
    OR EXISTS(SELECT 1 FROM oauth_states WHERE credential_id=NEW.id))
BEGIN
    DELETE FROM credential_fields WHERE credential_id=NEW.id;
    DELETE FROM oauth_states WHERE credential_id=NEW.id;
    UPDATE credentials SET
        encrypted_value='',
        oauth_resource='',
        oauth_issuer='',
        encrypted_refresh_token=NULL,
        oauth_client_secret_enc=NULL,
        oauth_refresh_token_enc=NULL,
        username=NULL,
        account_email=NULL,
        account_label=NULL,
        description=NULL,
        last_error=NULL,
        last_used_ips=NULL,
        tags=NULL,
        oauth_client_id=NULL,
        oauth_auth_url=NULL,
        oauth_token_url=NULL,
        oauth_scopes=NULL,
        oauth_token_expires_at=NULL,
        token_expires_at=NULL WHERE id=NEW.id;
    SELECT RAISE(ABORT,'retired credential erasure failed')
    WHERE NOT EXISTS(SELECT 1 FROM credentials
        WHERE id=NEW.id AND workspace_id IS NEW.workspace_id AND deleted_at IS NOT NULL
        AND COALESCE(encrypted_value,'')=''
        AND COALESCE(oauth_resource,'')=''
        AND COALESCE(oauth_issuer,'')=''
        AND COALESCE(encrypted_refresh_token,'')=''
        AND COALESCE(oauth_client_secret_enc,'')=''
        AND COALESCE(oauth_refresh_token_enc,'')=''
        AND COALESCE(username,'')=''
        AND COALESCE(account_email,'')=''
        AND COALESCE(account_label,'')=''
        AND COALESCE(description,'')=''
        AND COALESCE(last_error,'')=''
        AND COALESCE(last_used_ips,'')=''
        AND COALESCE(tags,'')=''
        AND COALESCE(oauth_client_id,'')=''
        AND COALESCE(oauth_auth_url,'')=''
        AND COALESCE(oauth_token_url,'')=''
        AND COALESCE(oauth_scopes,'')=''
        AND COALESCE(oauth_token_expires_at,'')=''
        AND COALESCE(token_expires_at,'')='')
       OR EXISTS(SELECT 1 FROM credential_fields WHERE credential_id=NEW.id)
       OR EXISTS(SELECT 1 FROM oauth_states WHERE credential_id=NEW.id);
END;

CREATE TRIGGER credential_retired_scrub_update AFTER UPDATE ON credentials
WHEN NEW.deleted_at IS NOT NULL AND (
    COALESCE(NEW.encrypted_value,'')!=''
    OR COALESCE(NEW.oauth_resource,'')!=''
    OR COALESCE(NEW.oauth_issuer,'')!=''
    OR COALESCE(NEW.encrypted_refresh_token,'')!=''
    OR COALESCE(NEW.oauth_client_secret_enc,'')!=''
    OR COALESCE(NEW.oauth_refresh_token_enc,'')!=''
    OR COALESCE(NEW.username,'')!=''
    OR COALESCE(NEW.account_email,'')!=''
    OR COALESCE(NEW.account_label,'')!=''
    OR COALESCE(NEW.description,'')!=''
    OR COALESCE(NEW.last_error,'')!=''
    OR COALESCE(NEW.last_used_ips,'')!=''
    OR COALESCE(NEW.tags,'')!=''
    OR COALESCE(NEW.oauth_client_id,'')!=''
    OR COALESCE(NEW.oauth_auth_url,'')!=''
    OR COALESCE(NEW.oauth_token_url,'')!=''
    OR COALESCE(NEW.oauth_scopes,'')!=''
    OR COALESCE(NEW.oauth_token_expires_at,'')!=''
    OR COALESCE(NEW.token_expires_at,'')!=''
    OR EXISTS(SELECT 1 FROM credential_fields WHERE credential_id=NEW.id)
    OR EXISTS(SELECT 1 FROM oauth_states WHERE credential_id=NEW.id))
BEGIN
    DELETE FROM credential_fields WHERE credential_id=NEW.id;
    DELETE FROM oauth_states WHERE credential_id=NEW.id;
    UPDATE credentials SET
        encrypted_value='',
        oauth_resource='',
        oauth_issuer='',
        encrypted_refresh_token=NULL,
        oauth_client_secret_enc=NULL,
        oauth_refresh_token_enc=NULL,
        username=NULL,
        account_email=NULL,
        account_label=NULL,
        description=NULL,
        last_error=NULL,
        last_used_ips=NULL,
        tags=NULL,
        oauth_client_id=NULL,
        oauth_auth_url=NULL,
        oauth_token_url=NULL,
        oauth_scopes=NULL,
        oauth_token_expires_at=NULL,
        token_expires_at=NULL WHERE id=NEW.id;
    SELECT RAISE(ABORT,'retired credential erasure failed')
    WHERE NOT EXISTS(SELECT 1 FROM credentials
        WHERE id=NEW.id AND workspace_id IS NEW.workspace_id AND deleted_at IS NOT NULL
        AND COALESCE(encrypted_value,'')=''
        AND COALESCE(oauth_resource,'')=''
        AND COALESCE(oauth_issuer,'')=''
        AND COALESCE(encrypted_refresh_token,'')=''
        AND COALESCE(oauth_client_secret_enc,'')=''
        AND COALESCE(oauth_refresh_token_enc,'')=''
        AND COALESCE(username,'')=''
        AND COALESCE(account_email,'')=''
        AND COALESCE(account_label,'')=''
        AND COALESCE(description,'')=''
        AND COALESCE(last_error,'')=''
        AND COALESCE(last_used_ips,'')=''
        AND COALESCE(tags,'')=''
        AND COALESCE(oauth_client_id,'')=''
        AND COALESCE(oauth_auth_url,'')=''
        AND COALESCE(oauth_token_url,'')=''
        AND COALESCE(oauth_scopes,'')=''
        AND COALESCE(oauth_token_expires_at,'')=''
        AND COALESCE(token_expires_at,'')='')
       OR EXISTS(SELECT 1 FROM credential_fields WHERE credential_id=NEW.id)
       OR EXISTS(SELECT 1 FROM oauth_states WHERE credential_id=NEW.id);
END;

CREATE TRIGGER credential_retired_field_insert AFTER INSERT ON credential_fields
WHEN EXISTS(SELECT 1 FROM credentials WHERE id=NEW.credential_id AND deleted_at IS NOT NULL)
BEGIN
    DELETE FROM credential_fields WHERE credential_id=NEW.credential_id;
    SELECT RAISE(ABORT,'retired credential field erasure failed')
    WHERE EXISTS(SELECT 1 FROM credential_fields WHERE credential_id=NEW.credential_id);
END;

CREATE TRIGGER credential_retired_field_update AFTER UPDATE ON credential_fields
WHEN EXISTS(SELECT 1 FROM credentials WHERE id=NEW.credential_id AND deleted_at IS NOT NULL)
BEGIN
    DELETE FROM credential_fields WHERE credential_id=NEW.credential_id;
    SELECT RAISE(ABORT,'retired credential field erasure failed')
    WHERE EXISTS(SELECT 1 FROM credential_fields WHERE credential_id=NEW.credential_id);
END;

CREATE TRIGGER credential_retired_oauth_insert AFTER INSERT ON oauth_states
WHEN EXISTS(SELECT 1 FROM credentials WHERE id=NEW.credential_id AND deleted_at IS NOT NULL)
BEGIN
    DELETE FROM oauth_states WHERE credential_id=NEW.credential_id;
    SELECT RAISE(ABORT,'retired credential oauth erasure failed')
    WHERE EXISTS(SELECT 1 FROM oauth_states WHERE credential_id=NEW.credential_id);
END;

CREATE TRIGGER credential_retired_oauth_update AFTER UPDATE ON oauth_states
WHEN EXISTS(SELECT 1 FROM credentials WHERE id=NEW.credential_id AND deleted_at IS NOT NULL)
BEGIN
    DELETE FROM oauth_states WHERE credential_id=NEW.credential_id;
    SELECT RAISE(ABORT,'retired credential oauth erasure failed')
    WHERE EXISTS(SELECT 1 FROM oauth_states WHERE credential_id=NEW.credential_id);
END;

-- A pre-existing IGNORE trigger must not let old material survive while the
-- migration ledger claims success. The temporary CHECK aborts the same TX.
CREATE TEMP TABLE credential_erasure_guard(ok INTEGER CHECK(ok=1));
INSERT INTO credential_erasure_guard SELECT NOT EXISTS(
    SELECT 1 FROM credential_erasure_expected e LEFT JOIN credentials c ON c.id=e.id
    WHERE c.id IS NULL OR c.deleted_at IS NULL OR (
    COALESCE(c.encrypted_value,'')!=''
    OR COALESCE(c.oauth_resource,'')!=''
    OR COALESCE(c.oauth_issuer,'')!=''
    OR COALESCE(c.encrypted_refresh_token,'')!=''
    OR COALESCE(c.oauth_client_secret_enc,'')!=''
    OR COALESCE(c.oauth_refresh_token_enc,'')!=''
    OR COALESCE(c.username,'')!=''
    OR COALESCE(c.account_email,'')!=''
    OR COALESCE(c.account_label,'')!=''
    OR COALESCE(c.description,'')!=''
    OR COALESCE(c.last_error,'')!=''
    OR COALESCE(c.last_used_ips,'')!=''
    OR COALESCE(c.tags,'')!=''
    OR COALESCE(c.oauth_client_id,'')!=''
    OR COALESCE(c.oauth_auth_url,'')!=''
    OR COALESCE(c.oauth_token_url,'')!=''
    OR COALESCE(c.oauth_scopes,'')!=''
    OR COALESCE(c.oauth_token_expires_at,'')!=''
    OR COALESCE(c.token_expires_at,'')!='')
    OR EXISTS(SELECT 1 FROM credential_fields WHERE credential_id=e.id)
    OR EXISTS(SELECT 1 FROM oauth_states WHERE credential_id=e.id));
DROP TABLE credential_erasure_guard;
DROP TABLE credential_erasure_expected;
