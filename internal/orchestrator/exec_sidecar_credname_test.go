package orchestrator

import (
	"context"
	"strings"
	"testing"
)

// #1657 — one badly-named credential must not cost the agent its other
// credentials or its run.
//
// buildCredFileScript used to return a hard error for a file-mounted credential
// whose env var name failed the uppercase charset. The caller
// (writeCredentialFiles → preparePreflightDirs) treats an error on a run that
// carries file-mounted credentials as fatal, so a user who named a credential
// `github-token` in the UI lost every run of that crew — including the SSH key
// and the CLI token that were named perfectly well.

// TestBuildCredFileScript_BadlyNamedCredentialDoesNotCostTheOthers is the
// headline: the badly-named one is skipped, everything else still lands.
func TestBuildCredFileScript_BadlyNamedCredentialDoesNotCostTheOthers(t *testing.T) {
	t.Parallel()
	creds := []Credential{
		{ID: "c1", EnvVarName: "github-token", PlainValue: "ghp_x", Type: "CLI_TOKEN"},
		{ID: "c2", EnvVarName: "DEPLOY_KEY", PlainValue: "-----BEGIN KEY-----", Type: "SSH_KEY"},
	}
	script, fileCount, skipped, err := buildCredFileScript(creds, "/secrets/agent-a", false)
	if err != nil {
		t.Fatalf("buildCredFileScript returned an error for a badly-named credential: %v — "+
			"one bad name must cost that credential only, not the batch", err)
	}
	if !strings.Contains(script, "/secrets/agent-a/ssh/DEPLOY_KEY") {
		t.Errorf("the well-named SSH key is missing from the script:\n%s", script)
	}
	if strings.Contains(script, "github-token") {
		t.Errorf("the badly-named credential reached the script:\n%s", script)
	}
	if fileCount != 1 {
		t.Errorf("fileCount = %d, want 1 (the SSH key only)", fileCount)
	}
	if len(skipped) != 1 || skipped[0].EnvVar != "github-token" || skipped[0].CredentialID != "c1" {
		t.Fatalf("skipped = %+v, want exactly the github-token credential — a credential "+
			"that vanishes with no report is a quieter version of the same defect", skipped)
	}
}

// TestBuildCredFileScript_SkippedNameNeverReachesTheScript keeps the security
// half of the old hard error. The name is a path component and is interpolated
// into `sh -c`; skipping must mean the bytes never appear, not that they appear
// somewhere harmless.
func TestBuildCredFileScript_SkippedNameNeverReachesTheScript(t *testing.T) {
	t.Parallel()
	creds := []Credential{
		{ID: "evil", EnvVarName: "GH;rm -rf /", PlainValue: "x", Type: "SECRET"},
		{ID: "ok", EnvVarName: "GOOD_TOKEN", PlainValue: "y", Type: "SECRET"},
	}
	script, _, skipped, err := buildCredFileScript(creds, "/secrets/agent-a", false)
	if err != nil {
		t.Fatalf("buildCredFileScript: %v", err)
	}
	if strings.Contains(script, "rm -rf /") {
		t.Fatalf("shell metacharacters from a credential name reached the script:\n%s", script)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %+v, want the one malicious name", skipped)
	}
}

// TestBuildCredFileScript_BadFieldNameCostsTheFieldNotTheCredential is the same
// rule one level down. A part refused a name used to abort the batch too, which
// meant a multi-part credential the API tier had already sanitised could still
// take the run down if anything ever handed the orchestrator a raw name.
func TestBuildCredFileScript_BadFieldNameCostsTheFieldNotTheCredential(t *testing.T) {
	t.Parallel()
	creds := []Credential{
		{ID: "c1", EnvVarName: "AWS", PlainValue: "akid", Type: "GENERIC_SECRET", Fields: []CredentialField{
			{EnvVar: "aws-region", Value: "eu-central-1"},
			{EnvVar: "AWS_SECRET", Value: "s3cr3t"},
		}},
	}
	script, fileCount, skipped, err := buildCredFileScript(creds, "/secrets/agent-a", false)
	if err != nil {
		t.Fatalf("a badly-named field aborted the batch: %v", err)
	}
	if !strings.Contains(script, "/secrets/agent-a/AWS_SECRET") {
		t.Errorf("the well-named field is missing from the script:\n%s", script)
	}
	if !strings.Contains(script, "/secrets/agent-a/AWS") {
		t.Errorf("the credential's own file is missing from the script:\n%s", script)
	}
	if strings.Contains(script, "aws-region") {
		t.Errorf("the badly-named field reached the script:\n%s", script)
	}
	if fileCount != 2 {
		t.Errorf("fileCount = %d, want 2 (the credential and its one legal field)", fileCount)
	}
	if len(skipped) != 1 || skipped[0].EnvVar != "aws-region" {
		t.Fatalf("skipped = %+v, want exactly the aws-region field", skipped)
	}
}

// TestPreparePreflightDirs_BadlyNamedCredentialDoesNotAbortTheRun is the defect
// as a user meets it: the run simply does not start, and the error blames the
// Docker daemon's version because that advice is appended to the same string.
func TestPreparePreflightDirs_BadlyNamedCredentialDoesNotAbortTheRun(t *testing.T) {
	o, _, req := preflightFixture(t)
	req.Credentials = []Credential{
		{ID: "c1", EnvVarName: "github-token", PlainValue: "ghp_x", Type: "CLI_TOKEN"},
		{ID: "c2", EnvVarName: "DEPLOY_KEY", PlainValue: "-----BEGIN KEY-----", Type: "SSH_KEY"},
	}
	// fileCreds=true: the run genuinely carries file-mounted credentials, which
	// is the branch that turns a credential-write failure into a dead run.
	if _, _, err := o.preparePreflightDirs(context.Background(), req, nil, true, false, "run-1"); err != nil {
		t.Fatalf("preparePreflightDirs: %v — a credential named github-token killed a run "+
			"that also carried a perfectly good SSH key", err)
	}
}
