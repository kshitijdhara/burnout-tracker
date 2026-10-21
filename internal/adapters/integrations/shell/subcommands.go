package shell

import (
	"maps"
	"slices"
	"strings"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
)

// ToolSubcommands is the allowlist of subcommands recorded per tool, owned
// by the domain classifier. It is rendered into the hook script, so a word
// not listed here never leaves the shell, not even in the emitter's
// environment.
var ToolSubcommands = classify.Subcommands()

// zshSubcommandTable renders ToolSubcommands as the body of a zsh
// associative array: tool ' sub1 sub2 ' pairs, sorted for stable output.
func zshSubcommandTable() string {
	var b strings.Builder
	for _, tool := range slices.Sorted(maps.Keys(ToolSubcommands)) {
		b.WriteString("  ")
		b.WriteString(tool)
		b.WriteString(" ' ")
		b.WriteString(strings.Join(ToolSubcommands[tool], " "))
		b.WriteString(" '\n")
	}
	return b.String()
}
