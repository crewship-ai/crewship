package notify

import (
	"fmt"

	"github.com/nicholas-fedor/shoutrrr/pkg/router"
)

// ValidateServiceURL checks that raw parses as a real delivery target for its
// scheme, by handing it to the delivery library's own parser.
//
// This is what keeps the hand-written composers in providers_catalog.go
// honest. They take webhook URLs apart by hand — "the last two path segments
// are the id and the token" — which is correct until a provider changes its
// URL shape. Without this check a wrong composer produces a channel that
// saves cleanly, passes every shape check we could write ourselves, and fails
// only on someone's first real notification. With it, the failure lands at
// compose time, in a test.
//
// It does not perform any network call: the parse is local.
func ValidateServiceURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("empty service url")
	}
	var r router.ServiceRouter
	if _, err := r.Locate(raw); err != nil {
		return err
	}
	return nil
}
