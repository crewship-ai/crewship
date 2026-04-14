package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// imageValidateTimeout caps registry HEAD manifest lookups so a slow or
// unreachable registry does not stall a PATCH request.
const imageValidateTimeout = 5 * time.Second

// ValidateImageExists issues a HEAD manifest request to the registry to
// verify that ref exists and is pullable. Catches typos like "debian:bogus"
// before the user hits provisioning time.
//
// Behavior:
//   - Invalid reference syntax → error.
//   - Registry unreachable or DNS failure → error (can't confirm pullable).
//   - Auth required (UNAUTHORIZED / 401) → no error, fall-open for private
//     images; blocking would break private registry workflows.
//   - Manifest HEAD succeeds → no error.
//
// Callers are expected to wrap this as fail-fast validation before persisting
// runtime_image. A 5s timeout is applied internally.
func ValidateImageExists(ctx context.Context, ref string) error {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("parse image ref: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, imageValidateTimeout)
	defer cancel()

	_, err = remote.Head(parsed,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)
	if err != nil {
		if isAuthError(err) {
			return nil
		}
		return err
	}
	return nil
}

// isAuthError heuristically detects registry auth failures. Rough but OK for
// MVP — avoids blocking valid private images when Crewship has no creds.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	switch {
	case strings.Contains(msg, "UNAUTHORIZED"):
		return true
	case strings.Contains(msg, "401"):
		return true
	case strings.Contains(msg, "DENIED"):
		return true
	case strings.Contains(msg, "FORBIDDEN"):
		return true
	case strings.Contains(msg, "403"):
		return true
	}
	// transport.Error surfaces status codes via its Errors field; the
	// stringification already contains the code so the substring check above
	// is sufficient. Explicit errors.Is checks are left for callers that want
	// finer-grained handling.
	var te interface{ StatusCode() int }
	if errors.As(err, &te) {
		code := te.StatusCode()
		return code == 401 || code == 403
	}
	return false
}
