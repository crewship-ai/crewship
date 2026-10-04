//go:build !linux

package stagedstart

import "errors"

func Run([]string) (int, error) { return 1, errors.New("staged start requires Linux") }
