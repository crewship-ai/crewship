# Mirrored reference artifacts

Each folder here reproduces, 1:1, one of the owner's reference artifacts
so the demo can be compared side by side with the original. A folder holds
the converted content files and a `SOURCE.md` naming the original artifact,
the Crewship kind it maps to, and anything the conversion could not carry
(a capability Crewship does not have yet is listed, never faked).

Each item registers itself from its own `seeddata/mirror_<slug>.go` through
`init()`, appending to the showcase catalogues, so items never edit a
shared list.
