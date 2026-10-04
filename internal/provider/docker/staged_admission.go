package docker

import "errors"

var errStagedDenied = errors.New("staged start: runtime/configuration or boot evidence unavailable")
