package access

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// LegacyPublicationAuthorityFingerprint identifies existing trusted versions.
// A recognized legacy child is still exact content to compile inside the
// publisher ceiling; this sentinel transfers no execution authority.
const LegacyPublicationAuthorityFingerprint = "legacy-trusted:v1"

// PublicationAuthoritySchema identifies the private canonical protocol.
// Tuple changes require an explicit new schema, never silent reinterpretation.
const PublicationAuthoritySchema = 2

// PublicationAuthority is host-only immutable input to publication identity.
// This typed manifest is not an authorization, admission or public request DTO.
type PublicationAuthority struct {
	Schema                int                        `json:"-"`
	WorkspaceID           string                     `json:"-"`
	PipelineID            string                     `json:"-"`
	PublisherID           string                     `json:"-"`
	PublisherMembershipID string                     `json:"-"`
	RootCrewID            string                     `json:"-"`
	AuthorAgentID         string                     `json:"-"`
	ActingAgentID         string                     `json:"-"`
	RootOriginID          string                     `json:"-"`
	Authoring             []PublicationGrantBinding  `json:"-"`
	Execution             []PublicationGrantBinding  `json:"-"`
	Recipes               []PublicationRecipeBinding `json:"-"`
	Agents                []PublicationAgentBinding  `json:"-"`
}

// ParentVersionID is empty only for the unpublished root candidate.
// Every nested callsite is keyed by its exact parent version and step ID.
type PublicationRecipeBinding struct {
	ParentVersionID      string `json:"-"`
	StepID               string `json:"-"`
	PipelineID           string `json:"-"`
	VersionID            string `json:"-"`
	Version              int    `json:"-"`
	ContentHash          string `json:"-"`
	AuthorityFingerprint string `json:"-"`
	VersionOriginID      string `json:"-"`
}

type PublicationAgentBinding struct {
	ParentVersionID    string `json:"-"`
	StepID             string `json:"-"`
	AgentID            string `json:"-"`
	CrewID             string `json:"-"`
	Profile            string `json:"-"`
	PolicyHash         string `json:"-"`
	CredentialID       string `json:"-"`
	GrantID            string `json:"-"`
	CredentialRevision string `json:"-"`
	Model              string `json:"-"`
	GrantExpiry        string `json:"-"`
	KeyExpiry          string `json:"-"`
	MaxOutputTokens    int64  `json:"-"`
}

// Positional tuples deliberately enumerate every hidden DTO field. Changing
// tuple layout is a new schema, never an incidental JSON-tag refactor.
type publicationCanonical struct {
	Schema      int          `json:"schema"`
	Workspace   string       `json:"workspace"`
	Pipeline    string       `json:"pipeline"`
	Publisher   string       `json:"publisher"`
	Membership  string       `json:"membership"`
	Crew        string       `json:"crew"`
	AuthorAgent string       `json:"author_agent"`
	ActingAgent string       `json:"acting_agent"`
	RootOrigin  string       `json:"root_origin"`
	Authoring   [][9]string  `json:"authoring"`
	Execution   [][9]string  `json:"execution"`
	Recipes     [][8]string  `json:"recipes"`
	Agents      [][13]string `json:"agents"`
}

var publicationModel = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,95}$`)

func publicationID(id string) bool {
	return id != "" && utf8.ValidString(id) && len(id) <= 256 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\x00\r\n\t")
}
func publicationDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func publicationExpiry(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return "", ErrDenied
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

// CanonicalPublicationAuthority validates and hashes a private projection.
// It authenticates neither inputs nor live authority. Callers must obtain
// bindings through host checks and separately prove recipe completeness.
func CanonicalPublicationAuthority(a PublicationAuthority) ([]byte, string, error) {
	if a.Schema != PublicationAuthoritySchema || !publicationID(a.WorkspaceID) || !publicationID(a.PipelineID) || !publicationID(a.PublisherID) || !publicationID(a.PublisherMembershipID) || !publicationID(a.ActingAgentID) || (a.RootCrewID != "" && !publicationID(a.RootCrewID)) || (a.AuthorAgentID != "" && !publicationID(a.AuthorAgentID)) || (a.RootOriginID != "" && !publicationID(a.RootOriginID)) || len(a.Authoring) > 128 || len(a.Execution) > 128 || len(a.Recipes) > 64 || len(a.Agents) == 0 || len(a.Agents) > 16 {
		return nil, "", ErrDenied
	}
	c := publicationCanonical{Schema: a.Schema, Workspace: a.WorkspaceID, Pipeline: a.PipelineID, Publisher: a.PublisherID, Membership: a.PublisherMembershipID, Crew: a.RootCrewID, AuthorAgent: a.AuthorAgentID, ActingAgent: a.ActingAgentID, RootOrigin: a.RootOriginID}
	grantIdentities := map[string][9]string{}
	grants := func(in []PublicationGrantBinding) ([][9]string, error) {
		out := make([][9]string, 0, len(in))
		seen := map[Right]bool{}
		for _, b := range in {
			if b.WorkspaceID != a.WorkspaceID || b.PublisherID != a.PublisherID || b.PublisherMembershipID != a.PublisherMembershipID || !validRight(b.Right) || !publicationID(b.Right.ID) || !publicationID(b.GrantID) || !publicationID(b.IssuerID) || !publicationID(b.IssuerMembershipID) || seen[b.Right] {
				return nil, ErrDenied
			}
			seen[b.Right] = true
			tuple := [9]string{b.WorkspaceID, b.PublisherID, b.PublisherMembershipID, b.Right.Kind, b.Right.ID, b.Right.Operation, b.GrantID, b.IssuerID, b.IssuerMembershipID}
			if previous, ok := grantIdentities[b.GrantID]; ok && previous != tuple {
				return nil, ErrDenied
			}
			grantIdentities[b.GrantID] = tuple
			out = append(out, tuple)
		}
		slices.SortFunc(out, func(a, b [9]string) int { return slices.Compare(a[:], b[:]) })
		return out, nil
	}
	var err error
	if c.Authoring, err = grants(a.Authoring); err != nil {
		return nil, "", err
	}
	if c.Execution, err = grants(a.Execution); err != nil {
		return nil, "", err
	}
	// Use non-nil empty slices for a single canonical empty representation.
	c.Recipes = make([][8]string, 0, len(a.Recipes))
	c.Agents = make([][13]string, 0, len(a.Agents))
	calls := map[[2]string]bool{}
	versions := map[string][5]string{}
	versionNumbers := map[[2]string]string{}
	for _, r := range a.Recipes {
		key := [2]string{r.ParentVersionID, r.StepID}
		if (r.ParentVersionID != "" && !publicationID(r.ParentVersionID)) || !publicationID(r.StepID) || !publicationID(r.PipelineID) || !publicationID(r.VersionID) || r.Version < 1 || !publicationDigest(r.ContentHash) || !(publicationDigest(r.AuthorityFingerprint) || r.AuthorityFingerprint == LegacyPublicationAuthorityFingerprint) || (r.VersionOriginID != "" && !publicationID(r.VersionOriginID)) || calls[key] {
			return nil, "", ErrDenied
		}
		calls[key] = true
		identity := [5]string{r.PipelineID, strconv.Itoa(r.Version), r.ContentHash, r.AuthorityFingerprint, r.VersionOriginID}
		if old, ok := versions[r.VersionID]; ok && old != identity {
			return nil, "", ErrDenied
		}
		numberKey := [2]string{r.PipelineID, strconv.Itoa(r.Version)}
		if old, ok := versionNumbers[numberKey]; ok && old != r.VersionID {
			return nil, "", ErrDenied
		}
		versionNumbers[numberKey] = r.VersionID
		versions[r.VersionID] = identity
		c.Recipes = append(c.Recipes, [8]string{r.ParentVersionID, r.StepID, r.PipelineID, r.VersionID, strconv.Itoa(r.Version), r.ContentHash, r.AuthorityFingerprint, r.VersionOriginID})
	}
	actingFound := false
	for _, b := range a.Agents {
		key := [2]string{b.ParentVersionID, b.StepID}
		if (b.ParentVersionID != "" && !publicationID(b.ParentVersionID)) || !publicationID(b.StepID) || !publicationID(b.AgentID) || !publicationID(b.CrewID) || (b.Profile != "responses_text" && b.Profile != "native_api_key") || !publicationDigest(b.PolicyHash) || !publicationID(b.CredentialID) || !publicationID(b.GrantID) || !publicationDigest(b.CredentialRevision) || !publicationModel.MatchString(b.Model) || b.MaxOutputTokens != 4096 || calls[key] {
			return nil, "", ErrDenied
		}
		calls[key] = true
		grantExpiry, e := publicationExpiry(b.GrantExpiry)
		if e != nil {
			return nil, "", e
		}
		keyExpiry, e := publicationExpiry(b.KeyExpiry)
		if e != nil {
			return nil, "", e
		}
		actingFound = actingFound || b.AgentID == a.ActingAgentID
		c.Agents = append(c.Agents, [13]string{b.ParentVersionID, b.StepID, b.AgentID, b.CrewID, b.Profile, b.PolicyHash, b.CredentialID, b.GrantID, b.CredentialRevision, b.Model, grantExpiry, keyExpiry, strconv.FormatInt(b.MaxOutputTokens, 10)})
	}
	if !actingFound {
		return nil, "", ErrDenied
	}
	// Every non-root callsite must refer to a represented exact child version.
	for key := range calls {
		if key[0] != "" {
			if _, ok := versions[key[0]]; !ok {
				return nil, "", ErrDenied
			}
		}
	}
	slices.SortFunc(c.Recipes, func(a, b [8]string) int { return slices.Compare(a[:], b[:]) })
	slices.SortFunc(c.Agents, func(a, b [13]string) int { return slices.Compare(a[:], b[:]) })
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > 256<<10 {
		return nil, "", ErrDenied
	}
	sum := sha256.Sum256(append([]byte("crewship.publication.authority.v2\x00"), raw...))
	return raw, hex.EncodeToString(sum[:]), nil
}
