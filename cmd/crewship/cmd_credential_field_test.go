package main

// CLI parity for the credential custom-field surface (project rule #3).
//
// These drive the cobra commands against a stub server, so what is asserted is
// the CONTRACT — which endpoint, which method, which body — rather than the
// server behaviour, which internal/api/credential_fields_test.go owns.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// credFieldCredID is CUID-shaped so resolveCredentialID short-circuits on the
// existence check instead of scanning the credential list.
const credFieldCredID = "ccred00000000000000flds"

func runCredField(t *testing.T, cmd *cobra.Command, args []string, flags map[string]string) (string, error) {
	t.Helper()
	covResetFlags(t, cmd)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	t.Cleanup(func() {
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})
	if flags != nil {
		covSetFlags(t, cmd, flags)
	}
	err := cmd.RunE(cmd, args)
	return stdout.String() + stderr.String(), err
}

// stubFieldCredential registers the existence check resolveCredentialID makes.
func stubFieldCredential(s *clitest.StubServer) {
	s.OnGet("/api/v1/credentials/"+credFieldCredID,
		clitest.JSONResponse(200, map[string]any{"id": credFieldCredID, "name": "aws-prod"}))
}

// A secret field must render as a marker, never as a value — and the CLI
// cannot invent one, because the server does not send it. The test pins that
// the rendering does not fabricate a placeholder that looks like data.
func TestCredFieldList_ShowsKeysAndHidesSecretValues(t *testing.T) {
	stub := covStub(t)
	stubFieldCredential(stub)
	stub.OnGet("/api/v1/credentials/"+credFieldCredID+"/fields", clitest.JSONResponse(200, []map[string]any{
		{"key": "access_key_id", "is_secret": false, "ordinal": 0, "value": "AKIAEXAMPLE"},
		{"key": "secret_access_key", "is_secret": true, "ordinal": 1, "value": nil},
	}))

	// newFormatter writes its table straight to os.Stdout, so the command's
	// own writers do not see it.
	var err error
	out := covCaptureStdoutCli3(t, func() {
		_, err = runCredField(t, credFieldListCmd, []string{credFieldCredID}, nil)
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"access_key_id", "secret_access_key", "AKIAEXAMPLE"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "(secret)") {
		t.Errorf("a secret field should render as (secret), got:\n%s", out)
	}
}

// `set` is an upsert over a create/update pair: POST first, and only if the
// server says the key is taken (409) does it PUT. That keeps the API honest —
// a duplicate is still a real conflict — while the CLI stays one verb.
func TestCredFieldSet_CreatesThenFallsBackToUpdateOnConflict(t *testing.T) {
	stub := covStub(t)
	stubFieldCredential(stub)
	stub.OnPost("/api/v1/credentials/"+credFieldCredID+"/fields",
		clitest.JSONResponse(409, map[string]any{"error": "a field named \"region\" already exists on this credential"}))
	stub.OnPut("/api/v1/credentials/"+credFieldCredID+"/fields/region",
		clitest.JSONResponse(200, map[string]any{"key": "region", "is_secret": false, "ordinal": 0, "value": "eu-central-1"}))

	if _, err := runCredField(t, credFieldSetCmd, []string{credFieldCredID, "region"},
		map[string]string{"value": "eu-central-1", "plain": "true"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	calls := stub.Calls()
	if len(calls) < 3 {
		t.Fatalf("expected resolve + POST + PUT, got %d calls: %+v", len(calls), calls)
	}
	post := calls[len(calls)-2]
	put := calls[len(calls)-1]
	if post.Method != "POST" || !strings.HasSuffix(post.Path, "/fields") {
		t.Errorf("expected a POST to /fields first, got %s %s", post.Method, post.Path)
	}
	if put.Method != "PUT" || !strings.HasSuffix(put.Path, "/fields/region") {
		t.Errorf("expected a PUT to /fields/region after the 409, got %s %s", put.Method, put.Path)
	}
}

// --plain is the only way to store a field in cleartext. Without it the field
// is secret, because a value whose secrecy the operator did not state must be
// encrypted — the same fail-safe default the server applies.
func TestCredFieldSet_DefaultsToSecret(t *testing.T) {
	stub := covStub(t)
	stubFieldCredential(stub)
	stub.OnPost("/api/v1/credentials/"+credFieldCredID+"/fields",
		clitest.JSONResponse(201, map[string]any{"key": "totp_seed", "is_secret": true, "ordinal": 0, "value": nil}))

	if _, err := runCredField(t, credFieldSetCmd, []string{credFieldCredID, "totp_seed"},
		map[string]string{"value": "JBSWY3DPEHPK3PXP"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	calls := stub.Calls()
	body := map[string]any{}
	if err := json.Unmarshal(calls[len(calls)-1].Body, &body); err != nil {
		t.Fatalf("decode POST body: %v", err)
	}
	if secret, ok := body["is_secret"].(bool); !ok || !secret {
		t.Errorf("POST body is_secret = %v, want true — an unflagged field must be encrypted", body["is_secret"])
	}
}

func TestCredFieldSet_RequiresAValue(t *testing.T) {
	stub := covStub(t)
	stubFieldCredential(stub)

	_, err := runCredField(t, credFieldSetCmd, []string{credFieldCredID, "region"}, nil)
	if err == nil || !strings.Contains(err.Error(), "--value") {
		t.Fatalf("err = %v, want the missing-value error", err)
	}
}

// The key shape is checked client-side before the request. The server remains
// the authority, but an operator who typed `Access-Key` learns why immediately
// instead of reading a 400 they have to map back to their input.
func TestCredFieldSet_RejectsBadKeyShapeBeforeCallingTheServer(t *testing.T) {
	stub := covStub(t)
	stubFieldCredential(stub)

	_, err := runCredField(t, credFieldSetCmd, []string{credFieldCredID, "Access-Key"},
		map[string]string{"value": "x"})
	if err == nil || !strings.Contains(err.Error(), "lower_snake_case") {
		t.Fatalf("err = %v, want the key-shape error", err)
	}
	for _, c := range stub.Calls() {
		if c.Method == "POST" {
			t.Errorf("a malformed key must not reach the server, but a POST was made: %+v", c)
		}
	}
}

func TestCredFieldRemove_DeletesTheKey(t *testing.T) {
	stub := covStub(t)
	stubFieldCredential(stub)
	stub.OnDelete("/api/v1/credentials/"+credFieldCredID+"/fields/region",
		clitest.JSONResponse(200, map[string]any{"success": true}))

	if _, err := runCredField(t, credFieldRemoveCmd, []string{credFieldCredID, "region"}, nil); err != nil {
		t.Fatalf("rm: %v", err)
	}
	last := stub.Calls()[len(stub.Calls())-1]
	if last.Method != http.MethodDelete || !strings.HasSuffix(last.Path, "/fields/region") {
		t.Errorf("expected DELETE /fields/region, got %s %s", last.Method, last.Path)
	}
}
