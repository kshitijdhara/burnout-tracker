package shell

import (
	"maps"
	"slices"
	"strings"

	"github.com/kshitijdhara/burntout/internal/domain/classify"
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

// bashSubcommandFuncs renders ToolSubcommands as two bash functions that
// work on bash 3.2, which has no associative arrays:
// __burntout_has_subcmds TOOL and __burntout_allowed TOOL WORD.
func bashSubcommandFuncs() string {
	tools := slices.Sorted(maps.Keys(ToolSubcommands))
	var b strings.Builder
	b.WriteString("__burntout_has_subcmds() {\n  case $1 in\n    " + strings.Join(tools, "|") + ") return 0 ;;\n  esac\n  return 1\n}\n")
	b.WriteString("__burntout_allowed() {\n  case $1 in\n")
	for _, tool := range tools {
		b.WriteString("    " + tool + ") case $2 in " + strings.Join(ToolSubcommands[tool], "|") + ") return 0 ;; esac ;;\n")
	}
	b.WriteString("  esac\n  return 1\n}")
	return b.String()
}

// fishSubcommandFuncs renders ToolSubcommands as the same two functions
// in fish.
func fishSubcommandFuncs() string {
	tools := slices.Sorted(maps.Keys(ToolSubcommands))
	var b strings.Builder
	b.WriteString("    function __burntout_has_subcmds\n        switch $argv[1]\n            case " + strings.Join(tools, " ") +
		"\n                return 0\n        end\n        return 1\n    end\n")
	b.WriteString("    function __burntout_allowed\n        switch $argv[1]\n")
	for _, tool := range tools {
		b.WriteString("            case " + tool + "\n                contains -- $argv[2] " + strings.Join(ToolSubcommands[tool], " ") + "; and return 0\n")
	}
	b.WriteString("        end\n        return 1\n    end")
	return b.String()
}
