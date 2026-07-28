package seeddata

// Demo credentials — one of every shape the vault can hold, so a fresh
// workspace shows what the credential surface actually does instead of a
// single Anthropic key and three empty facets.
//
// Every value here is INERT AND OBVIOUSLY SO. They read
// "dummy-not-a-real-secret-…" rather than looking like plausible tokens, for
// two reasons: nobody wastes a minute finding out whether a demo key works,
// and a screenshot of this page cannot be mistaken for a leak. The PEM blobs
// are shaped enough to pass the server's type validation (SSH_KEY and
// CERTIFICATE both require a real PEM envelope) and contain nothing else.
//
// Nothing here is assigned to an agent by default. These exist to populate the
// list, the type filter, the classification badges and the readiness column —
// not to change what any agent can do. The one exception is deliberate and
// documented at DemoBindings.

// DemoCredential is a seeded demo credential plus the extra parts and the
// classification the vault should show for it.
type DemoCredential struct {
	Def CredentialDef
	// Fields are the additional parts (credential_fields). Secret ones are
	// encrypted server-side and never echoed on read; plain ones are stored
	// cleartext, the same way `username` always has been, because an identifier
	// is not a secret.
	Fields []DemoField
	// Sensitivity is the reveal classification: "", "RESTRICTED" or "SEALED".
	// Empty leaves the server default (STANDARD).
	Sensitivity string
	// Tags feed the tag facet.
	Tags []string
	// Username is the cleartext identifier half a USERPASS credential
	// requires. Empty for every other type.
	Username string
}

// DemoField is one part of a multi-part demo credential.
type DemoField struct {
	Key    string
	Value  string
	Secret bool
}

// DemoBinding pairs a demo credential to a slot in one crew, so the seeded
// workspace demonstrates the thing the binding model exists for: two accounts
// of the same provider, each reaching a different crew under the same variable.
type DemoBinding struct {
	CredentialName string
	Slot           string
	CrewSlug       string
}

const dummyPrefix = "dummy-not-a-real-secret-"

// A PEM envelope with a body that is plainly filler. Enough to satisfy the
// server's shape check without resembling key material.
const demoPrivateKeyPEM = `-----BEGIN OPENSSH PRIVATE KEY-----
ZHVtbXktbm90LWEtcmVhbC1zZWNyZXQtdGhpcy1pcy1kZW1vLWZpbGxlci1vbmx5
ZHVtbXktbm90LWEtcmVhbC1zZWNyZXQtbm90LWEtdXNhYmxlLXByaXZhdGUta2V5
-----END OPENSSH PRIVATE KEY-----`

const demoCertificatePEM = `-----BEGIN CERTIFICATE-----
ZHVtbXktbm90LWEtcmVhbC1zZWNyZXQtZGVtby1jZXJ0aWZpY2F0ZS1maWxsZXI=
ZHVtbXktbm90LWEtcmVhbC1zZWNyZXQtbm90LWEtdXNhYmxlLWNlcnRpZmljYXRl
-----END CERTIFICATE-----`

// DemoCredentials returns the demo vault contents. Ordered so the list reads
// top-to-bottom as a tour: the everyday shapes first, the multi-part and the
// locked-down ones after.
func DemoCredentials() []DemoCredential {
	return []DemoCredential{
		// Two accounts of ONE provider — the case a workspace could not express
		// before bindings, because both would have had to be called GH_TOKEN.
		// DemoBindings points each at its own crew.
		{
			Def: CredentialDef{
				Name:        "github-acme",
				Description: "GitHub bot for the Acme org — demo account, inert",
				Type:        "CLI_TOKEN",
				Provider:    "GITHUB",
				Value:       dummyPrefix + "github-acme",
			},
			Tags: []string{"demo", "source-control"},
		},
		{
			Def: CredentialDef{
				Name:        "github-globex",
				Description: "GitHub bot for the Globex org — demo account, inert",
				Type:        "CLI_TOKEN",
				Provider:    "GITHUB",
				Value:       dummyPrefix + "github-globex",
			},
			Tags: []string{"demo", "source-control"},
		},

		// Multi-part: one credential, three parts, two of them not secret.
		// This is the shape a single encrypted_value column could not hold.
		{
			Def: CredentialDef{
				Name:        "aws-sandbox",
				Description: "AWS sandbox access key — demo, inert",
				Type:        "SECRET",
				Provider:    "AWS",
				Value:       dummyPrefix + "aws-secret-access-key",
			},
			Fields: []DemoField{
				{Key: "access_key_id", Value: "AKIADEMO0000000DEMO"},
				{Key: "region", Value: "eu-central-1"},
				{Key: "session_token", Value: dummyPrefix + "aws-session-token", Secret: true},
			},
			Tags: []string{"demo", "cloud"},
		},

		// Login shape: the cleartext half is an identifier, not a secret.
		{
			Def: CredentialDef{
				Name:        "smtp-relay",
				Description: "SMTP relay login for outbound demo mail — inert",
				Type:        "USERPASS",
				Provider:    "NONE",
				Value:       dummyPrefix + "smtp-password",
			},
			Username: "demo-mailer",
			Fields: []DemoField{
				{Key: "host", Value: "smtp.example.invalid"},
				{Key: "port", Value: "587"},
			},
			Tags: []string{"demo", "comms"},
		},

		{
			Def: CredentialDef{
				Name:        "deploy-ssh-key",
				Description: "Deploy key for git-over-SSH — demo PEM, not usable",
				Type:        "SSH_KEY",
				Provider:    "GITHUB",
				Value:       demoPrivateKeyPEM,
			},
			Tags: []string{"demo", "source-control"},
		},

		{
			Def: CredentialDef{
				Name:        "mtls-client-cert",
				Description: "Client certificate for an mTLS demo endpoint — not usable",
				Type:        "CERTIFICATE",
				Provider:    "NONE",
				Value:       demoCertificatePEM,
			},
			Tags: []string{"demo", "infra"},
		},

		{
			Def: CredentialDef{
				Name:        "stripe-test",
				Description: "Stripe test-mode key — demo, inert",
				Type:        "API_KEY",
				Provider:    "STRIPE",
				Value:       dummyPrefix + "stripe",
			},
			Tags: []string{"demo", "payments"},
		},
		{
			Def: CredentialDef{
				Name:        "notion-workspace",
				Description: "Notion integration token — demo, inert",
				Type:        "API_KEY",
				Provider:    "NOTION",
				Value:       dummyPrefix + "notion",
			},
			Tags: []string{"demo", "docs"},
		},

		{
			Def: CredentialDef{
				Name:        "webhook-signing-secret",
				Description: "HMAC secret for verifying inbound webhooks — demo, inert",
				Type:        "GENERIC_SECRET",
				Provider:    "NONE",
				Value:       dummyPrefix + "webhook-hmac",
			},
			Tags: []string{"demo", "infra"},
		},

		// RESTRICTED and SEALED exist so the classification badges and the
		// reveal refusal are visible without anyone having to set them by hand.
		// SEALED is the one no role can reveal, including OWNER — the demo is a
		// safer place to discover that than production.
		{
			Def: CredentialDef{
				Name:        "prod-db-dsn",
				Description: "Production database DSN — SEALED: rotate, never reveal",
				Type:        "GENERIC_SECRET",
				Provider:    "NONE",
				Value:       "postgres://demo:" + dummyPrefix + "dsn@db.example.invalid:5432/demo",
			},
			Sensitivity: "SEALED",
			Tags:        []string{"demo", "data", "production"},
		},
		{
			Def: CredentialDef{
				Name:        "kubeconfig-staging",
				Description: "Staging cluster kubeconfig — RESTRICTED",
				Type:        "SECRET",
				Provider:    "KUBERNETES",
				Value:       dummyPrefix + "kubeconfig",
			},
			Sensitivity: "RESTRICTED",
			Tags:        []string{"demo", "infra"},
		},
	}
}

// DemoBindings pairs the two GitHub accounts to different crews under the same
// slot. This is the one place the demo data changes what an agent resolves, and
// it is the point: without it the multi-account model is a claim in a doc
// rather than something visible in `crewship credential resolve`.
//
// The values are inert, so a crew that picks one up gets a GH_TOKEN that fails
// to authenticate — which is the correct demo outcome. A seeded credential that
// silently worked would be a real secret in a public seed.
func DemoBindings() []DemoBinding {
	return []DemoBinding{
		{CredentialName: "github-acme", Slot: "GH_TOKEN", CrewSlug: "engineering"},
		{CredentialName: "github-globex", Slot: "GH_TOKEN", CrewSlug: "quality"},
	}
}
