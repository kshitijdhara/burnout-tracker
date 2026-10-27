package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/checkin"
)

// errQuit means the user stopped a check-in; nothing is saved.
var errQuit = errors.New("check-in stopped")

// runCheckin asks the weekly check-in (both CBI subscales), or with
// --daily the two-question micro check-in, and stores the answers.
func runCheckin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("checkin", flag.ContinueOnError)
	daily := fs.Bool("daily", false, "the two-question daily check-in instead of the weekly one")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return errors.New("check-ins must be answered interactively in a terminal")
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	store, err := sqlite.OpenExisting(ctx, dataDir)
	if errors.Is(err, sqlite.ErrNoDatabase) {
		return errors.New("nothing recorded yet: run `burnout setup` and start the daemon first")
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	instruments := checkin.Weekly()
	if *daily {
		instruments = []checkin.Instrument{checkin.Daily()}
	}
	var previous []checkin.Record
	if !*daily {
		if previous, err = store.Checkins(ctx, time.Now().AddDate(0, -3, 0)); err != nil {
			return err
		}
	}
	records, err := askCheckin(bufio.NewReader(os.Stdin), os.Stdout, instruments, time.Now())
	if errors.Is(err, errQuit) {
		fmt.Println("Stopped. Nothing was saved.")
		return nil
	}
	if err != nil {
		return err
	}
	if err := store.SaveCheckins(ctx, records); err != nil {
		return err
	}
	fmt.Print(checkinResult(records, previous, regionFromEnv()))
	return nil
}

// askCheckin asks every item of every instrument. Each answer is a number
// from 1 to 5; q stops without saving.
func askCheckin(in *bufio.Reader, out io.Writer, instruments []checkin.Instrument, now time.Time) ([]checkin.Record, error) {
	total := 0
	for _, inst := range instruments {
		total += len(inst.Items)
	}
	if len(instruments) > 1 {
		_, _ = fmt.Fprintf(out, "Weekly check-in: %d questions, about a minute. Answer with a number; q stops without saving.\n", total)
	} else {
		_, _ = fmt.Fprintln(out, "Answer with a number; q stops without saving.")
	}
	var records []checkin.Record
	n := 0
	for _, inst := range instruments {
		_, _ = fmt.Fprintf(out, "\n%s\n", inst.Intro)
		answers := checkin.Answers{}
		for _, item := range inst.Items {
			n++
			v, err := askItem(in, out, item, n, total)
			if err != nil {
				return nil, err
			}
			answers[item.ID] = v
		}
		score, err := inst.Score(answers)
		if err != nil {
			return nil, err
		}
		records = append(records, checkin.Record{At: now, Instrument: inst.ID, Answers: answers, Score: score})
	}
	return records, nil
}

func askItem(in *bufio.Reader, out io.Writer, item checkin.Item, n, total int) (int, error) {
	_, _ = fmt.Fprintf(out, "\n%d/%d  %s\n", n, total, item.Text)
	for i, c := range item.Choices {
		_, _ = fmt.Fprintf(out, "  %d %s\n", i+1, c.Label)
	}
	for {
		_, _ = fmt.Fprint(out, "> ")
		line, err := in.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer == "q" || (err != nil && answer == "") {
			return 0, errQuit
		}
		if i, convErr := strconv.Atoi(answer); convErr == nil && i >= 1 && i <= len(item.Choices) {
			return item.Choices[i-1].Value, nil
		}
		if err != nil {
			return 0, errQuit
		}
		_, _ = fmt.Fprintf(out, "Please type a number from 1 to %d, or q to stop.\n", len(item.Choices))
	}
}

// checkinResult describes the scores, against the previous weekly
// check-in, and shows the support message when a subscale is high.
func checkinResult(records, previous []checkin.Record, region string) string {
	var b strings.Builder
	support := false
	b.WriteString("\n")
	for _, r := range records {
		inst, _ := checkin.ByID(r.Instrument)
		if r.Instrument == checkin.DailyID {
			b.WriteString("Saved. Thanks.\n")
			continue
		}
		fmt.Fprintf(&b, "%s: %.0f/100 (%s)", inst.Name, r.Score, checkin.BandOf(r.Score))
		if p, ok := lastOf(previous, r.Instrument); ok {
			fmt.Fprintf(&b, ", last time %.0f", p.Score)
		}
		b.WriteString("\n")
		support = support || checkin.NeedsSupport(r.Instrument, r.Score)
	}
	if support {
		b.WriteString("\n" + checkin.SupportMessage + "\n")
		for _, res := range checkin.Resources(region) {
			fmt.Fprintf(&b, "      %s: %s\n", res.Name, res.Contact)
		}
	}
	if len(records) > 1 {
		b.WriteString("\nSaved. `burnout review` shows how your week compares.\n")
	}
	return b.String()
}

func lastOf(records []checkin.Record, instrument string) (checkin.Record, bool) {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Instrument == instrument {
			return records[i], true
		}
	}
	return checkin.Record{}, false
}

// regionFromEnv reads the user's region from the locale, in the order the
// C library uses it.
func regionFromEnv() string {
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if r := checkin.RegionFromLocale(os.Getenv(v)); r != "" {
			return r
		}
	}
	return ""
}
