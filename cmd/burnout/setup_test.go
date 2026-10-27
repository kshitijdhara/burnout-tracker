package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/kshitijdhara/burnout-tracker/internal/config"
)

func TestAskProfile(t *testing.T) {
	for _, tc := range []struct {
		name, input, role, hours, says string
	}{
		{"defaults on enter", "\n\n", "developer", "09:00-18:00", "[1]"},
		{"pick and type", "4\n8:30-17:00\n", "pm", "08:30-17:00", "project or product manager"},
		{"re-asks bad answers", "9\nmanager\n2\n18:00-09:00\n10:00-19:00\n", "student", "10:00-19:00", "past midnight"},
		{"end of input keeps defaults", "", "developer", "09:00-18:00", "Core hours"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			c := askProfile(bufio.NewReader(strings.NewReader(tc.input)), &out, config.Default())
			if c.Profile.Role != tc.role || c.Profile.CoreHours != tc.hours {
				t.Errorf("got %+v\n%s", c.Profile, out.String())
			}
			if !strings.Contains(out.String(), tc.says) {
				t.Errorf("output lacks %q:\n%s", tc.says, out.String())
			}
			if err := c.Validate(); err != nil {
				t.Errorf("invalid result: %v", err)
			}
		})
	}
	// The current role is the default.
	c := config.Default()
	c.Profile.Role = "researcher"
	if got := askProfile(bufio.NewReader(strings.NewReader("\n\n")), &bytes.Buffer{}, c); got.Profile.Role != "researcher" {
		t.Errorf("current role not kept: %+v", got.Profile)
	}
}
