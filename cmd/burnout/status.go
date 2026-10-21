package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

func runStatus(ctx context.Context, _ []string) error {
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	store, err := sqlite.OpenReadOnly(ctx, dataDir)
	if errors.Is(err, sqlite.ErrNoDatabase) {
		fmt.Println("Nothing recorded yet. Run `burnout setup`, then start `burnout daemon`.")
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	day := model.LogicalDay(time.Now(), app.DefaultConfig().Rollover)
	s, err := store.DaySummary(ctx, day)
	if err != nil {
		return err
	}
	printSummary(s)
	return nil
}

func printSummary(s sqlite.DaySummary) {
	total := 0.0
	for _, v := range s.CategorySeconds {
		total += v
	}
	fmt.Printf("Today (%s): %s attended\n", s.LogicalDay, hm(total))
	if total == 0 && s.ShellCommands == 0 && s.AgentSessions == 0 {
		fmt.Println("  Nothing recorded yet. Is the daemon running? Try `burnout doctor`.")
		return
	}
	for _, c := range slices.Sorted(maps.Keys(s.CategorySeconds)) {
		fmt.Printf("  %-16s %s\n", c, hm(s.CategorySeconds[c]))
	}
	if s.UnattributedSeconds > 0 {
		fmt.Printf("  %-16s %s\n", "unattributed", hm(s.UnattributedSeconds))
	}
	if len(s.TopApps) > 0 {
		fmt.Println("Top apps:")
		for _, a := range s.TopApps {
			fmt.Printf("  %-32s %s\n", a.Name, hm(a.Seconds))
		}
	}
	if s.ShellCommands > 0 {
		fmt.Printf("Shell: %d commands, %d failed\n", s.ShellCommands, s.ShellFailures)
		for _, c := range s.TopCommands {
			name := c.Name
			if name == "" {
				name = "(unrecognised)"
			}
			fmt.Printf("  %-16s %d\n", name, c.Count)
		}
	}
	if s.AgentSessions > 0 {
		fmt.Printf("Agents: %d sessions\n", s.AgentSessions)
		for _, k := range slices.Sorted(maps.Keys(s.AgentStateSeconds)) {
			fmt.Printf("  agent %-18s %s\n", k, hm(s.AgentStateSeconds[k]))
		}
		for _, k := range slices.Sorted(maps.Keys(s.HumanStateSeconds)) {
			fmt.Printf("  you   %-18s %s\n", k, hm(s.HumanStateSeconds[k]))
		}
	}
}

func hm(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
