//go:build tools

// Package tools pins dependencies used by adapters under construction so
// go.mod stays stable while several packages are developed in parallel.
// Remove entries once real imports exist.
package tools

import (
	_ "github.com/BurntSushi/toml"
	_ "github.com/Microsoft/go-winio"
	_ "github.com/zalando/go-keyring"
	_ "golang.org/x/sys/unix"
	_ "golang.org/x/sys/windows"
	_ "modernc.org/sqlite"
)
