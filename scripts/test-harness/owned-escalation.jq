# Public API returns metadata as serialized JSON. Never infer ownership from
# generic words in a reason, a credential type, or the first pending request.
[.[] | select(.type == "CREDENTIAL" and .status == "PENDING" and .from_slug == $agent)
 | select((try (.metadata | fromjson | .name) catch null) == $name)
 | .id | select(type == "string" and length > 0)]
| if length == 1 then .[0] else error("expected exactly one owned pending escalation") end
