# shellcheck shell=bash
# keeper-ledger.sh: which keeper requests each ledger guarantee applies to.
#
# An access or execute request is raised PENDING and then decided, so its
# history starts at PENDING. A Phase-2 verdict and a DUPLICATE_SUPPRESSED row are
# written already decided (#1369) and hold one terminal transition. Reading
# PENDING off those is a false failure, and skipping them loses a real check.
# Both functions take the JSON of `crewship keeper requests` and print one id.

keeper_pick_raised_request() {
  printf '%s' "$1" | jq -r 'first(.[]
    | select(.request_type == "access" or .request_type == "execute")
    | select((.decision // "") as $d | $d != "" and $d != "PENDING" and $d != "DUPLICATE_SUPPRESSED"))
    | .id // empty'
}

keeper_pick_one_shot_request() {
  printf '%s' "$1" | jq -r 'first(.[]
    | select((.decision // "") as $d | $d != "" and $d != "PENDING")
    | select(.decision == "DUPLICATE_SUPPRESSED"
        or (.request_type != "access" and .request_type != "execute")))
    | .id // empty'
}
