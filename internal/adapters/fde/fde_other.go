//go:build !darwin && !windows && !linux

package fde

import "context"

func status(context.Context, string) (State, string, error) {
	return Unknown, "encryption detection is not supported on this OS", nil
}
