package dashboard

// The dashboard's JSON views. They hold what the CLI's status, review and
// privacy commands show, shaped for the page.

// Overview is the strain trend.
type Overview struct {
	Today   string `json:"today"`
	Profile string `json:"profile"` // "developer, core hours 09:00-18:00"
	// Calibrating is set until the first score: usable days so far of Needed.
	Calibrating *Calibration `json:"calibrating,omitempty"`
	// Latest is the most recent completed day, scored or not.
	Latest *DayScore `json:"latest,omitempty"`
	// Days are the last 90 completed days, oldest first; missing dates are
	// absent.
	Days []DayScore `json:"days"`
	// Notices are things the user should know now: paused, private hours,
	// disk encryption off, an invalid config file.
	Notices []string `json:"notices,omitempty"`
}

// Calibration is the progress towards the first score.
type Calibration struct {
	UsableDays int `json:"usable_days"`
	Needed     int `json:"needed"`
}

// DayScore is one day's strain.
type DayScore struct {
	Date       string   `json:"date"`
	Status     string   `json:"status"`           // ok, missing or calibrating
	Strain     *float64 `json:"strain,omitempty"` // 0–100 when ok
	Band       string   `json:"band,omitempty"`   // low, elevated or high
	Reason     string   `json:"reason,omitempty"` // why a day is missing
	Drivers    []string `json:"drivers,omitempty"`
	Guardrails []string `json:"guardrails,omitempty"`
	Marked     string   `json:"marked,omitempty"` // an annotation covering the day
}

// Day is one day's activity.
type Day struct {
	Date        string      `json:"date"`
	AttendedS   float64     `json:"attended_s"`
	Categories  []Named     `json:"categories"` // attended seconds, largest first
	TopApps     []Named     `json:"top_apps"`
	Unattended  float64     `json:"unattributed_s"`
	Commands    int         `json:"commands"`
	Failures    int         `json:"failures"`
	TopCommands []Named     `json:"top_commands"`
	Agents      int         `json:"agent_sessions"`
	AgentStates []Named     `json:"agent_states"`
	HumanStates []Named     `json:"human_states"`
	Meetings    []Meeting   `json:"meetings"`
	Timeline    []SlotEntry `json:"timeline"` // 10-minute slots with attention, in order
}

// Named is a label with a value (seconds or a count).
type Named struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// Meeting is a calendar busy block, as local clock times.
type Meeting struct {
	Start     string `json:"start"` // HH:MM
	End       string `json:"end"`
	Tentative bool   `json:"tentative,omitempty"`
}

// SlotEntry is a 10-minute slot: the category that held most attention.
type SlotEntry struct {
	Start     string  `json:"start"` // HH:MM, local
	Category  string  `json:"category"`
	AttendedS float64 `json:"attended_s"`
}

// Week is the weekly review.
type Week struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	Calibrating bool     `json:"calibrating"`
	Scored      int      `json:"scored"`
	Average     *float64 `json:"average,omitempty"`
	PrevAverage *float64 `json:"prev_average,omitempty"`
	Peak        *Peak    `json:"peak,omitempty"`
	Drivers     []Count  `json:"drivers"`
	Limits      []Count  `json:"limits"`
	Checkins    []Check  `json:"checkins"`
	CheckinDue  bool     `json:"checkin_due"`
	Marked      []string `json:"marked"`
	Suggestions []string `json:"suggestions"`
}

// Peak is the week's highest-strain day.
type Peak struct {
	Date   string  `json:"date"`
	Strain float64 `json:"strain"`
	Band   string  `json:"band"`
}

// Count is something and the days it came up on.
type Count struct {
	Label string `json:"label"`
	Days  int    `json:"days"`
}

// Check is one check-in instrument's latest result.
type Check struct {
	Name     string   `json:"name"`
	Score    float64  `json:"score"`
	Band     string   `json:"band"`
	Previous *float64 `json:"previous,omitempty"`
}

// Privacy is what is collected and the user's settings.
type Privacy struct {
	Collected []string  `json:"collected"` // what is and isn't collected
	Settings  []Setting `json:"settings"`
}

// Setting is one row of the privacy view.
type Setting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
