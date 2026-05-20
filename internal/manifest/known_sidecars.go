package manifest

import "strings"

// known_sidecars.go centralises the "if the operator wrote
// `image: postgres:16-alpine` and nothing else, what does Crewship
// auto-provision?" question. Each entry maps an image registry
// prefix to a small bundle of (a) literal env vars the image needs
// to come up sanely (POSTGRES_USER, MONGO_INITDB_ROOT_USERNAME, …)
// and (b) a list of AutoCredential entries Crewship should generate
// on the operator's behalf.
//
// Goals of this layer:
//
//   - End users don't see PENDING credentials for crew-private
//     sidecar passwords. A `services: [{ name: postgres, image:
//     postgres:16-alpine }]` block applies clean.
//   - Operators who want full control over the env can still
//     declare every field by hand — sugar is additive and explicit
//     declarations win on conflict (see Service.ResolveAuto...
//     helpers).
//   - The catalog is intentionally small and curated. New entries
//     are PR additions, not "any image we recognise" magic, so the
//     behaviour stays auditable from the codebase.
//
// Threat model: every credential here protects a sidecar that lives
// on the crew-private bridge network. Sidecars with ports published
// to the host are gated out in validate.go and AutoCredentials there
// must be operator-managed (T2 in the credentials tier ladder).

// sidecarDefaults is one row of the catalog. Both Env and
// AutoCredentials are additive — they merge with whatever the
// operator wrote into the manifest, with explicit values winning.
type sidecarDefaults struct {
	// Env is added to Service.Env (operator entries shadow these).
	Env map[string]string

	// AutoCredentials is appended to Service.AutoCredentials
	// (operator entries with the same Name shadow these).
	AutoCredentials []AutoCredential
}

// imagePrefixCatalog maps a leading image-name segment to the
// sidecar defaults. Match is done with strings.HasPrefix against
// the image's repository portion, so `postgres:16-alpine`,
// `postgres:latest`, `library/postgres:16`, and a private mirror
// like `harbor.acme.io/library/postgres:16` all hit the postgres
// entry (the prefix check strips any registry / namespace before
// the colon).
//
// Maintenance: keep the keys lowercase, no tags, no digests. The
// runtime match in lookupSidecarDefaults handles normalisation.
var imagePrefixCatalog = map[string]sidecarDefaults{
	"postgres": {
		Env: map[string]string{
			"POSTGRES_USER": "postgres",
		},
		AutoCredentials: []AutoCredential{
			{
				Name:        "POSTGRES_PASSWORD",
				Description: "Auto-generated superuser password for the crew-private Postgres sidecar.",
			},
		},
	},
	"mariadb": {
		AutoCredentials: []AutoCredential{
			{
				Name:        "MARIADB_ROOT_PASSWORD",
				Description: "Auto-generated root password for the crew-private MariaDB sidecar.",
			},
		},
	},
	"mysql": {
		AutoCredentials: []AutoCredential{
			{
				Name:        "MYSQL_ROOT_PASSWORD",
				Description: "Auto-generated root password for the crew-private MySQL sidecar.",
			},
		},
	},
	"mongo": {
		Env: map[string]string{
			"MONGO_INITDB_ROOT_USERNAME": "root",
		},
		AutoCredentials: []AutoCredential{
			{
				Name:        "MONGO_INITDB_ROOT_PASSWORD",
				Description: "Auto-generated root password for the crew-private MongoDB sidecar.",
			},
		},
	},
	"rabbitmq": {
		Env: map[string]string{
			"RABBITMQ_DEFAULT_USER": "admin",
		},
		AutoCredentials: []AutoCredential{
			{
				Name:        "RABBITMQ_DEFAULT_PASS",
				Description: "Auto-generated default-user password for the crew-private RabbitMQ sidecar.",
			},
		},
	},
	"elasticsearch": {
		Env: map[string]string{
			"discovery.type": "single-node",
		},
		AutoCredentials: []AutoCredential{
			{
				Name:        "ELASTIC_PASSWORD",
				Description: "Auto-generated elastic-user password for the crew-private Elasticsearch sidecar.",
			},
		},
	},
}

// lookupSidecarDefaults returns the catalog entry for an image
// reference, or (zero, false) when nothing matches. Normalisation
// strips any registry/namespace prefix and the tag/digest suffix
// so `harbor.example.com/library/postgres:16-alpine@sha256:abc...`
// still hits the `postgres` row.
func lookupSidecarDefaults(image string) (sidecarDefaults, bool) {
	name := normalizeImageName(image)
	if name == "" {
		return sidecarDefaults{}, false
	}
	d, ok := imagePrefixCatalog[name]
	return d, ok
}

// normalizeImageName trims registry/namespace + tag/digest and
// lower-cases. Returns the bare image name (e.g. "postgres") or
// empty string if the input doesn't look like an image reference.
//
// Algorithm, intentionally simple:
//   - Drop everything after the FIRST '@' (digest).
//   - Drop everything after the LAST ':' if that segment isn't a
//     port (digits-only of <=5 chars in the registry portion).
//     We do this after the '/' check, so `localhost:5000/foo:tag`
//     trims `:tag`, not `:5000`.
//   - Take the substring after the LAST '/' as the repository
//     basename.
//   - Lower-case the result.
//
// We don't pull in github.com/distribution/reference because the
// catalog match doesn't need the full grammar — a curated set of
// short names is all we resolve here.
func normalizeImageName(image string) string {
	if image == "" {
		return ""
	}
	if at := strings.IndexByte(image, '@'); at >= 0 {
		image = image[:at]
	}
	// Trim tag: take last ':' position AFTER the last '/' (so a
	// registry host with a port stays intact).
	lastSlash := strings.LastIndexByte(image, '/')
	tagSearch := image
	tagSearchStart := 0
	if lastSlash >= 0 {
		tagSearch = image[lastSlash+1:]
		tagSearchStart = lastSlash + 1
	}
	if colon := strings.IndexByte(tagSearch, ':'); colon >= 0 {
		image = image[:tagSearchStart+colon]
	}
	if lastSlash := strings.LastIndexByte(image, '/'); lastSlash >= 0 {
		image = image[lastSlash+1:]
	}
	return strings.ToLower(image)
}

// ResolveAutoCredentials returns the effective list of auto-managed
// credentials for a service, merging the operator's explicit
// AutoCredentials list with the sugar defaults from the image
// catalog. Operator entries with the same Name override sugar
// entries entirely (no field-level merging — if the operator wrote
// a Name=POSTGRES_PASSWORD entry, theirs wins, full stop).
//
// Returns nil when neither source contributes anything.
func (s *Service) ResolveAutoCredentials() []AutoCredential {
	if s == nil {
		return nil
	}
	defaults, hasDefaults := lookupSidecarDefaults(s.Image)
	if !hasDefaults && len(s.AutoCredentials) == 0 {
		return nil
	}

	byName := make(map[string]struct{}, len(s.AutoCredentials))
	out := make([]AutoCredential, 0, len(s.AutoCredentials)+len(defaults.AutoCredentials))
	for _, ac := range s.AutoCredentials {
		out = append(out, ac)
		byName[ac.Name] = struct{}{}
	}
	if hasDefaults {
		for _, ac := range defaults.AutoCredentials {
			if _, shadowed := byName[ac.Name]; shadowed {
				continue
			}
			out = append(out, ac)
			byName[ac.Name] = struct{}{}
		}
	}
	return out
}

// ResolveEnv returns the effective sidecar env map: operator's
// explicit Service.Env overlaid on top of the catalog's defaults
// for this image. Operator values always win on key collision.
//
// Returns nil when neither source contributes anything (keeps the
// downstream JSON minimal — no `"env": {}` noise on plain services).
func (s *Service) ResolveEnv() map[string]string {
	if s == nil {
		return nil
	}
	defaults, hasDefaults := lookupSidecarDefaults(s.Image)
	if !hasDefaults && len(s.Env) == 0 {
		return nil
	}
	out := make(map[string]string, len(defaults.Env)+len(s.Env))
	if hasDefaults {
		for k, v := range defaults.Env {
			out[k] = v
		}
	}
	for k, v := range s.Env {
		out[k] = v
	}
	return out
}
