-- credentials.crew_delivery — who a crew link delivers to.
--
-- 'all' (the default, every existing row): a credential_crews link delivers
-- the credential to every member of the crew under credentials.name. This is
-- the pre-existing behaviour, unchanged.
--
-- 'granted': the crew link only makes the credential belong to, and be
-- visible in, the crew. It is delivered to an agent only through an explicit
-- agent_credentials grant or an AGENT-scoped binding, and a routine step can
-- read it only by name (`{{ secrets.name.<credential-name> }}`), never by type.
-- This is what lets a crew own a specialist's key without its lead receiving it.
ALTER TABLE credentials ADD COLUMN crew_delivery TEXT NOT NULL DEFAULT 'all'
    CHECK (crew_delivery IN ('all', 'granted'));
