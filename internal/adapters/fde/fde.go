// Package fde reports whether the disk holding the app's data is protected
// by OS full-disk encryption. That is the app's at-rest protection
// (ADR-0007), so onboarding and doctor warn when it is off.
//
// Detection is best-effort and never needs admin rights. When the answer is
// unclear the result is Unknown, never a guess.
package fde

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// State is the encryption state of the data volume.
type State int

// States.
const (
	Unknown State = iota
	Enabled
	Disabled
)

func (s State) String() string {
	switch s {
	case Enabled:
		return "enabled"
	case Disabled:
		return "disabled"
	default:
		return "unknown"
	}
}

const commandTimeout = 3 * time.Second

// Status reports the encryption state of the volume holding path (usually
// the data directory) and a short human-readable detail.
func Status(ctx context.Context, path string) (State, string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return status(ctx, path)
}

// parseFdesetup interprets `fdesetup status` on macOS.
func parseFdesetup(out string) (State, string) {
	switch {
	case strings.Contains(out, "FileVault is On"):
		return Enabled, "FileVault is on"
	case strings.Contains(out, "Encryption in progress"):
		return Enabled, "FileVault encryption in progress"
	case strings.Contains(out, "Decryption in progress"):
		return Disabled, "FileVault decryption in progress"
	case strings.Contains(out, "FileVault is Off"):
		return Disabled, "FileVault is off"
	default:
		return Unknown, "could not read FileVault status"
	}
}

// parseBitLockerProtection interprets the shell property
// System.Volume.BitLockerProtection, which a standard user can read.
//
// Values, as observed in Explorer and documented by community sources:
// 0 not encryptable, 1 on, 2 off, 3 encrypting, 4 decrypting, 5 suspended,
// 6 on and locked, 8 waiting for activation. "Suspended" and "waiting for
// activation" leave the key in the clear on disk, so the data is not
// protected at rest and we report Disabled. Anything else is Unknown.
func parseBitLockerProtection(out string) (State, string) {
	v, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return Unknown, "could not read BitLocker status"
	}
	switch v {
	case 1, 6:
		return Enabled, "BitLocker is on"
	case 3:
		return Enabled, "BitLocker encryption in progress"
	case 0:
		return Disabled, "volume does not support BitLocker"
	case 2:
		return Disabled, "BitLocker is off"
	case 4:
		return Disabled, "BitLocker decryption in progress"
	case 5:
		return Disabled, "BitLocker protection is suspended"
	case 8:
		return Disabled, "device encryption is waiting for activation"
	default:
		return Unknown, "unrecognised BitLocker status " + strconv.Itoa(v)
	}
}
