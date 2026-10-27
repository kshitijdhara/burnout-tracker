package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/schedule"
)

const configUsage = `usage: burnout config COMMAND
  get [KEY]              show all settings, or one
  set KEY VALUE...       change a setting (lists take any number of values)
  add KEY VALUE...       add to a list setting
  remove KEY VALUE...    remove from a list setting
  unset KEY              restore a setting's default
  validate [FILE]        check the config file (or another file)
  path                   print where the config file is
Keys: `

// configPath returns where the config file lives.
func configPath() (string, error) {
	dir, err := paths.ConfigDir()
	if err != nil {
		return "", err
	}
	return config.Path(dir), nil
}

// loadConfig reads the config file; a missing file means the defaults.
func loadConfig() (config.Config, string, error) {
	path, err := configPath()
	if err != nil {
		return config.Config{}, "", err
	}
	c, err := config.Load(path)
	return c, path, err
}

// settingsFrom is what the daemon's loop applies from the config file.
func settingsFrom(c config.Config) app.Settings {
	return app.Settings{
		PrivateHours:  c.PrivateHours(),
		Exclusions:    c.Privacy.ExcludeApps,
		AggregateOnly: c.Privacy.AggregateOnly,
	}
}

// scoreOptions scores with the user's role and core hours.
func scoreOptions(c config.Config) app.ScoreOptions {
	o := app.DefaultScoreOptions()
	o.Features, o.Role = c.FeatureParams(), c.Role()
	return o
}

// profileConfig is the config for read-only commands: an invalid file
// gets a warning and the defaults, so status and review still work.
func profileConfig() config.Config {
	c, _, err := loadConfig()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "burnout: the config file is invalid, so the default profile is used (see `burnout config validate`)\n")
		return config.Default()
	}
	return c
}

// retentionFrom is the store's retention for c.
func retentionFrom(c config.Config) sqlite.Retention {
	var r sqlite.Retention
	r.RawEvents, r.Rollups, r.Intervals, r.DailyFeatures, r.Checkins = c.RetentionPeriods()
	return r
}

// privateNow returns the private-hours window now falls in, if any.
func privateNow(c config.Config, now time.Time) (string, bool) {
	for _, spec := range c.Privacy.PrivateHours {
		if w, err := schedule.Parse(spec); err == nil && w.Contains(now) {
			return w.String(), true
		}
	}
	return "", false
}

func runConfig(_ context.Context, args []string) error {
	usage := errors.New(configUsage + strings.Join(config.Keys(), ", "))
	if len(args) == 0 {
		return usage
	}
	path, err := configPath()
	if err != nil {
		return err
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "path":
		fmt.Println(path)
		return nil
	case "validate":
		if len(rest) > 1 {
			return usage
		}
		file := path
		if len(rest) == 1 {
			file = rest[0]
		} else if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			fmt.Println("No config file; every setting has its default.")
			return nil
		}
		if _, err := config.Load(file); err != nil {
			return fmt.Errorf("%s is not valid:\n%w", printable(file), err)
		}
		fmt.Printf("%s is valid.\n", printable(file))
		return nil
	case "get":
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		switch len(rest) {
		case 0:
			_, _ = os.Stdout.Write(config.Render(c))
			return nil
		case 1:
			vals, err := c.Get(rest[0])
			if err != nil {
				return err
			}
			for _, v := range vals {
				fmt.Println(printable(v))
			}
			return nil
		}
		return usage
	case "set", "add", "remove", "unset":
		if len(rest) == 0 || (cmd == "unset" && len(rest) != 1) || ((cmd == "add" || cmd == "remove") && len(rest) < 2) {
			return usage
		}
		return changeConfig(path, cmd, rest[0], rest[1:])
	}
	return usage
}

// changeConfig applies one change to the file and saves it, validated.
func changeConfig(path, cmd, key string, values []string) error {
	c, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("%w\nFix the file first (`burnout config validate`)", err)
	}
	switch cmd {
	case "set":
		err = c.Set(key, values)
	case "add":
		err = c.Add(key, values)
	case "remove":
		var n int
		if n, err = c.Remove(key, values); err == nil && n == 0 {
			fmt.Printf("None of those are in %s.\n", key)
			return nil
		}
	case "unset":
		err = c.Unset(key)
	}
	if err != nil {
		return err
	}
	if err := config.Save(path, c); err != nil {
		return err
	}
	if saved, err := config.Load(path); err == nil {
		c = saved // as written: duplicates dropped, private hours tidied
	}
	vals, _ := c.Get(key)
	if config.IsList(key) {
		quoted := make([]string, len(vals))
		for i, v := range vals {
			quoted[i] = strconv.Quote(v)
		}
		fmt.Printf("%s = [%s]\n", key, strings.Join(quoted, ", "))
	} else {
		fmt.Printf("%s = %s\n", key, strings.Join(vals, ""))
	}
	if strings.HasPrefix(key, "retention.") {
		fmt.Println("Shorter retention applies at the daemon's next nightly clean-up.")
	} else if daemonAnswers(context.Background()) {
		fmt.Println("The daemon picks this up within a few seconds.")
	}
	return nil
}
