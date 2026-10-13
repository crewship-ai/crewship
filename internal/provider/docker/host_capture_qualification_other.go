//go:build !linux

package docker

import "context"

func (*Provider) QualifyHostCapture(context.Context, string) (bool, error) { return false, nil }
