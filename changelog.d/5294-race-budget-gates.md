Race-duration alarms now block pull requests, merge groups and manual qualification as well as main pushes, retaining the existing thresholds and complete coverage accounting.

API race workers now compact their selection against the complete inventoried test set, avoiding oversized Go test matcher allocations while retaining all parents, descendant subtests and exact execution checks. CLI and non-race partition placement is unchanged.
