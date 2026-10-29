package mooring

// NormalizeSessionIngress validates original immutable membership using the same
// canonicalization as durable commits and returns owned copies. It is a pure
// check: it establishes no author, current permission, owner or acknowledgement.
// The transport must separately bound the complete original raw wrapper before
// parsing or sealing; normalized size cannot stand in for that check.
func NormalizeSessionIngress(key SessionKey, changes []BatchChange) ([]BatchChange, error) {
	return normalizeBatch(BatchRequest{Lease: OwnerLease{Key: key}, ExpectedRevision: "0", Changes: changes})
}
