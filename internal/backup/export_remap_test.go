package backup

// NonRemappablePKTablesForTest is the production nonRemappablePKTables, exposed
// so the external backup_test package asserts against the real policy rather
// than a hand copy that goes stale the moment the map gains an entry.
var NonRemappablePKTablesForTest = nonRemappablePKTables
