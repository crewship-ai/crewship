package backupplan

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func destinationFixture() Destination {
	return Destination{Endpoint: "https://storage.example.invalid", Bucket: "backups", AccessKeyID: "fixture-access", Region: "auto"}
}

func TestDestinationNormalizationPreservesExplicitNetworkPolicy(t *testing.T) {
	d := destinationFixture()
	d.Endpoint = " " + d.Endpoint + " "
	d.Bucket = " backups "
	d.Prefix = " /crewship/prod/ "
	d.AccessKeyID = " fixture-access "
	if err := NormalizeDestination(&d, "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	if d.Name != "backups/crewship/prod" || d.Prefix != "crewship/prod" || d.Kind != "s3" {
		t.Fatalf("normalization: %#v", d)
	}
	config := d.S3Config("fixture-secret")
	if config.SecretAccessKey != "fixture-secret" || config.AllowPrivateNetwork || config.Bucket != "backups" {
		t.Fatalf("configuration changed: %#v", config)
	}
	for name, change := range map[string]func(*Destination){
		"unsupported kind": func(d *Destination) { d.Kind = "drive" },
		"long name":        func(d *Destination) { d.Name = strings.Repeat("a", 121) },
		"private endpoint": func(d *Destination) { d.Endpoint = "http://127.0.0.1:9000" },
		"missing key":      func(d *Destination) { d.AccessKeyID = "" },
		"unsafe prefix":    func(d *Destination) { d.Prefix = "../escape" },
	} {
		t.Run(name, func(t *testing.T) {
			d := destinationFixture()
			change(&d)
			if err := NormalizeDestination(&d, "fixture-secret"); !IsValidation(err) {
				t.Fatalf("unsafe destination accepted: %v", err)
			}
		})
	}
	d = destinationFixture()
	d.Endpoint = "http://127.0.0.1:9000"
	d.AllowPrivateNetwork = true
	d.PathStyle = true
	if err := NormalizeDestination(&d, "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	if d.Name != "backups" || !d.S3Config("fixture-secret").PathStyle {
		t.Fatal("explicit private storage settings lost")
	}
}

func TestDestinationVaultAndPlanReferences(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	ctx := t.Context()
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("a7", 32))
	t.Setenv(encryption.KeyVersionEnvVar, "")
	sealed, err := encryption.Encrypt("fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	d := destinationFixture()
	d.Name = "Offsite"
	if err := InsertDestination(ctx, h.db, &d, sealed, "", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	opened, stored, err := DBDestinations(h.db)(ctx, d.ID)
	if err != nil || opened == nil || stored.ID != d.ID {
		t.Fatalf("open stored destination: %#v %v", stored, err)
	}
	encoded, err := json.Marshal(stored)
	if err != nil || strings.Contains(string(encoded), "fixture-secret") || strings.Contains(string(encoded), sealed) {
		t.Fatalf("destination exposed vault value: %s %v", encoded, err)
	}
	p := &Plan{Destinations: []string{" ", d.ID, "local", d.ID}}
	if err := ValidatePlanDestinations(ctx, h.db, p); err != nil || len(p.Destinations) != 2 || p.Destinations[0] != "local" || p.Destinations[1] != d.ID {
		t.Fatalf("plan destinations: %v %v", p.Destinations, err)
	}
	if err := ValidatePlanDestinations(ctx, h.db, &Plan{Destinations: []string{"missing"}}); !IsValidation(err) {
		t.Fatalf("unknown destination admitted: %v", err)
	}
	if opened, stored, err := DBDestinations(h.db)(ctx, "missing"); !errors.Is(err, ErrNotFound) || opened != nil || stored != nil {
		t.Fatalf("missing destination: %#v %#v %v", opened, stored, err)
	}
	if _, err := h.db.Exec(`UPDATE backup_offsite_destinations SET secret_enc='broken' WHERE id=?`, d.ID); err != nil {
		t.Fatal(err)
	}
	if opened, stored, err := DBDestinations(h.db)(ctx, d.ID); err == nil || opened != nil || stored == nil {
		t.Fatalf("corrupt vault value accepted: %#v %#v %v", opened, stored, err)
	}
	if _, err := h.db.Exec(`UPDATE backup_offsite_destinations SET secret_enc=?,endpoint='http://127.0.0.1' WHERE id=?`, sealed, d.ID); err != nil {
		t.Fatal(err)
	}
	if opened, stored, err := DBDestinations(h.db)(ctx, d.ID); err == nil || opened != nil || stored == nil {
		t.Fatalf("invalid stored endpoint accepted: %#v %#v %v", opened, stored, err)
	}
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePlanDestinations(ctx, h.db, &Plan{Destinations: []string{d.ID}}); err == nil || IsValidation(err) {
		t.Fatalf("database failure misclassified: %v", err)
	}
}
