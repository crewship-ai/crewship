package credpolicy

import "testing"

func TestIsKeeperGated_OnlySecretToday(t *testing.T) {
	if !IsKeeperGated("SECRET") {
		t.Error("SECRET must be Keeper-gated")
	}
	for _, ty := range []string{
		"GENERIC_SECRET", "CLI_TOKEN", "USERPASS", "SSH_KEY", "CERTIFICATE",
		"API_KEY", "AI_CLI_TOKEN", "OAUTH2", "ENDPOINT_URL",
	} {
		if IsKeeperGated(ty) {
			t.Errorf("%s must NOT be Keeper-gated (delivery unchanged)", ty)
		}
	}
}

// The whole point of the table: an unclassified type fails safe — withheld and
// not delivered — so a new credential type leaks nothing until someone adds an
// explicit row.
func TestFor_UnknownTypeFailsSafe(t *testing.T) {
	p := For("SOME_FUTURE_TYPE")
	if !p.KeeperGated {
		t.Error("unknown type must be Keeper-gated (withheld)")
	}
	if p.Delivery != DeliveryNone {
		t.Errorf("unknown type must not be delivered, got %q", p.Delivery)
	}
	if Known("SOME_FUTURE_TYPE") {
		t.Error("Known must be false for an unclassified type")
	}
}

func TestFileMounted_MatchesSecretMaterialTypes(t *testing.T) {
	fileTypes := map[string]bool{
		"SECRET": true, "GENERIC_SECRET": true, "CLI_TOKEN": true,
		"USERPASS": true, "SSH_KEY": true, "CERTIFICATE": true,
		"API_KEY": false, "AI_CLI_TOKEN": false, "OAUTH2": false, "ENDPOINT_URL": false,
	}
	for ty, want := range fileTypes {
		if got := For(ty).FileMounted(); got != want {
			t.Errorf("%s FileMounted() = %v, want %v", ty, got, want)
		}
	}
}
