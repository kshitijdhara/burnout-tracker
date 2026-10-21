//go:build !linux && !darwin && !windows

package procinfo

import "errors"

var errUnsupported = errors.New("procinfo: unsupported OS")

func parentOf(int) (int, error)  { return 0, errUnsupported }
func nameOf(int) (string, error) { return "", errUnsupported }
