package seeddata

import (
	"bytes"
	"crypto/sha256"
	"image/png"
	"strings"
	"testing"
)

func TestTeamChatCatalogHasDistinctLocalPortraitsAndRealRoles(t *testing.T) {
	keys, emails, portraits, roles := map[string]bool{}, map[string]bool{}, map[[32]byte]bool{}, map[string]bool{}
	valid := map[string]bool{"ADMIN": true, "MANAGER": true, "MEMBER": true, "VIEWER": true}
	for _, person := range TeamChatPeople {
		if keys[person.Key] || emails[person.Email] || !strings.HasSuffix(person.Email, "@crewship.invalid") || !strings.Contains(person.FullName, "demo") || !valid[person.Role] || person.JobTitle == "" || person.Message == "" {
			t.Fatalf("invalid demo identity %s", person.Key)
		}
		keys[person.Key], emails[person.Email], roles[person.Role] = true, true, true
		data, err := TeamChatAvatar(person.Key)
		if err != nil {
			t.Fatal(err)
		}
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if config.Width != 128 || config.Height != 128 || len(data) > 100000 || portraits[hash] {
			t.Fatalf("invalid or duplicated avatar for %s", person.Key)
		}
		portraits[hash] = true
	}
	if len(keys) != 6 || len(roles) != 4 {
		t.Fatalf("expected six colleagues and four non-owner RBAC roles, got %d and %d", len(keys), len(roles))
	}
}
