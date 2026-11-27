package craftsession

import (
	"os"
	"strings"
)

// savedValuesEngines are the gridcraft engine modules (SHA-256 of engine.wasm) that take
// mcp --saved-values: gridcraft 0.12.1+e68d19228249 with the saved-values patch (catalog
// c0d27f55…). Measured on a customer workbook of 234 203 cells: open 25.0 s → 4.0 s, RSS
// 712 → 405 MiB, every value equal to a recalculation.
var savedValuesEngines = map[string]bool{
	"c2959f753baa75f69a70b2d074ba285e58466bb849fcb98c057819ae284c1000": true,
	// + convert --saved-values (catalog fa3ecaad…)
	"52acf14ac2a4c7386a7333f9d18e548ac24b089c02afd47c71ace7e1540d0bc3": true,
	// + an Excel function the engine lacks keeps the document's value (catalog 57f7620f…)
	"90e7108286d7ef409604ddbfd062add72389dd70a1364417470684d8fa9af270": true,
}

// SavedValuesFor is the default of Config.OpenSavedValues for the engine module engineSHA: on
// only for an engine known to have the option, and off for all with CREWSHIP_CRAFT_SAVED_VALUES=0.
func SavedValuesFor(engineSHA string) bool {
	if os.Getenv("CREWSHIP_CRAFT_SAVED_VALUES") == "0" {
		return false
	}
	return savedValuesEngines[strings.ToLower(engineSHA)]
}

// savedValuesConvertEngines are the engine modules whose convert also takes --saved-values
// (validation without recalculating). Measured on the customer workbook copy: save 29.3 s → 7.5 s,
// initialize 28.6 s → 5.3 s, every check of the acceptance run passing.
var savedValuesConvertEngines = map[string]bool{
	"52acf14ac2a4c7386a7333f9d18e548ac24b089c02afd47c71ace7e1540d0bc3": true,
	"90e7108286d7ef409604ddbfd062add72389dd70a1364417470684d8fa9af270": true,
}

// SavedValidationFor says whether the validator may convert this server's own computed
// documents without recalculating with the engine module engineSHA.
func SavedValidationFor(engineSHA string) bool {
	return SavedValuesFor(engineSHA) && savedValuesConvertEngines[strings.ToLower(engineSHA)]
}
