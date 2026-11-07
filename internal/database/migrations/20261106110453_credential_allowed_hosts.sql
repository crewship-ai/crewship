-- Human-approved hosts for a credential (#5045). A JSON array of normalised
-- hostnames or "*.host" wildcards; '[]' means unbound, which is how every
-- existing credential behaves. Only an operator endpoint writes this column:
-- an agent's proposed hosts stay on the escalation as a suggestion.
ALTER TABLE credentials ADD COLUMN allowed_hosts TEXT NOT NULL DEFAULT '[]';
