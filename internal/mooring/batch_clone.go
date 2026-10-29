package mooring

// Preserve the exact canonical payload bytes while isolating mutable slices
// and read-counter pointers. JSON roundtrips would re-escape HTML/U+2028 inside
// RawMessage and thus cease to pass the exact JCS bytes to the adapter.
func cloneBatchOperation(op BatchOperation) BatchOperation {
	clone := op
	clone.Dependencies = make([]string, len(op.Dependencies))
	copy(clone.Dependencies, op.Dependencies)
	clone.Reads = make([]BatchReadPrecondition, len(op.Reads))
	copy(clone.Reads, op.Reads)
	for i := range clone.Reads {
		if counter := clone.Reads[i].StructureRevision; counter != nil {
			value := *counter
			clone.Reads[i].StructureRevision = &value
		}
	}
	clone.Payload = append([]byte(nil), op.Payload...)
	return clone
}
func cloneBatchChange(change BatchChange) BatchChange {
	clone := change
	clone.Operations = make([]BatchOperation, len(change.Operations))
	for i, op := range change.Operations {
		clone.Operations[i] = cloneBatchOperation(op)
	}
	return clone
}
