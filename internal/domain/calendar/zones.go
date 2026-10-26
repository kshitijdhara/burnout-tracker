package calendar

// WindowsZone maps a Windows time zone name, as Outlook and Exchange feeds
// use in TZID, to its IANA name. Only the common ones are listed; others
// fall back to local time.
func WindowsZone(name string) (string, bool) {
	z, ok := windowsZones[name]
	return z, ok
}

var windowsZones = map[string]string{
	"UTC": "UTC", "Greenwich Standard Time": "Atlantic/Reykjavik", "GMT Standard Time": "Europe/London",
	"W. Europe Standard Time": "Europe/Berlin", "Central Europe Standard Time": "Europe/Budapest",
	"Romance Standard Time": "Europe/Paris", "Central European Standard Time": "Europe/Warsaw",
	"E. Europe Standard Time": "Europe/Chisinau", "FLE Standard Time": "Europe/Kiev", "GTB Standard Time": "Europe/Bucharest",
	"Russian Standard Time": "Europe/Moscow", "Turkey Standard Time": "Europe/Istanbul", "Israel Standard Time": "Asia/Jerusalem",
	"Arabian Standard Time": "Asia/Dubai", "Pakistan Standard Time": "Asia/Karachi", "India Standard Time": "Asia/Kolkata",
	"Bangladesh Standard Time": "Asia/Dhaka", "SE Asia Standard Time": "Asia/Bangkok", "China Standard Time": "Asia/Shanghai",
	"Singapore Standard Time": "Asia/Singapore", "Taipei Standard Time": "Asia/Taipei", "Tokyo Standard Time": "Asia/Tokyo",
	"Korea Standard Time": "Asia/Seoul", "AUS Eastern Standard Time": "Australia/Sydney", "E. Australia Standard Time": "Australia/Brisbane",
	"W. Australia Standard Time": "Australia/Perth", "New Zealand Standard Time": "Pacific/Auckland",
	"South Africa Standard Time": "Africa/Johannesburg", "Egypt Standard Time": "Africa/Cairo", "W. Central Africa Standard Time": "Africa/Lagos",
	"E. South America Standard Time": "America/Sao_Paulo", "Argentina Standard Time": "America/Buenos_Aires",
	"Atlantic Standard Time": "America/Halifax", "Eastern Standard Time": "America/New_York", "Central Standard Time": "America/Chicago",
	"Mountain Standard Time": "America/Denver", "US Mountain Standard Time": "America/Phoenix", "Pacific Standard Time": "America/Los_Angeles",
	"Alaskan Standard Time": "America/Anchorage", "Hawaiian Standard Time": "Pacific/Honolulu",
	"Central Standard Time (Mexico)": "America/Mexico_City", "SA Pacific Standard Time": "America/Bogota",
}
