package wayland

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	repoRoot     = "../../../.."
	extensionDir = repoRoot + "/extensions/gnome-shell"
)

func readRepo(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, path)) //nolint:gosec // G304: repository files
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type schemaNode struct {
	Ref        string                `json:"$ref"`
	Minimum    *int                  `json:"minimum"`
	Maximum    *int                  `json:"maximum"`
	MaxLength  int                   `json:"maxLength"`
	MaxItems   int                   `json:"maxItems"`
	Pattern    string                `json:"pattern"`
	Required   []string              `json:"required"`
	Enum       []string              `json:"enum"`
	Properties map[string]schemaNode `json:"properties"`
}

// TestSchemaMatchesCode keeps schemas/v1/gnome-bridge.schema.json in step
// with the decoder's limits, so the published contract never drifts from
// what the agent enforces. Schema string lengths count characters; the Go
// limits are bytes, at most four per character.
func TestSchemaMatchesCode(t *testing.T) {
	var schema struct {
		Defs map[string]schemaNode `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(readRepo(t, "schemas/v1/gnome-bridge.schema.json")), &schema); err != nil {
		t.Fatal(err)
	}
	d := schema.Defs
	checks := []struct {
		name      string
		got, want int
	}{
		{"version maximum", *d["version"].Maximum, Version},
		{"coord maximum", *d["coord"].Maximum, MaxCoord},
		{"coord minimum", *d["coord"].Minimum, -MaxCoord},
		{"length maximum", *d["length"].Maximum, MaxCoord},
		{"windows maxItems", d["snapshot"].Properties["windows"].MaxItems, MaxWindows},
		{"monitors maxItems", d["snapshot"].Properties["monitors"].MaxItems, MaxMonitors},
		{"wm_class maxLength ×4", 4 * d["window"].Properties["wm_class"].MaxLength, MaxClassBytes},
		{"app maxLength ×4", 4 * d["window"].Properties["app"].MaxLength, MaxClassBytes},
		{"title maxLength ×4", 4 * d["window"].Properties["title"].MaxLength, MaxTitleBytes},
		{"pid maximum", *d["window"].Properties["pid"].Maximum, MaxPID},
		{"error maxLength", d["error"].Properties["error"].MaxLength, MaxErrorBytes},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: schema %d, code %d", c.name, c.got, c.want)
		}
	}
	if got := d["window"].Properties["type"].Enum; !slices.Equal(got, windowTypes) {
		t.Errorf("window types: schema %v, code %v", got, windowTypes)
	}
	if got := d["request"].Properties["token"].Pattern; got != reToken.String() {
		t.Errorf("token pattern: schema %q, code %q", got, reToken.String())
	}
	// Every field the Go window always carries is required by the schema,
	// and every schema property is one the Go window reads.
	var required []string
	wt := reflect.TypeFor[window]()
	for i := range wt.NumField() {
		name, opts, _ := strings.Cut(wt.Field(i).Tag.Get("json"), ",")
		if _, ok := d["window"].Properties[name]; !ok {
			t.Errorf("window.%s is not in the schema", name)
		}
		if opts != "omitempty" {
			required = append(required, name)
		}
	}
	if got := d["window"].Required; !slices.Equal(got, required) {
		t.Errorf("window required: schema %v, code %v", got, required)
	}
}

// TestExtensionMatchesCode checks the extension's constants against the
// agent's: the same uuid, file names, version and limits.
func TestExtensionMatchesCode(t *testing.T) {
	var meta struct {
		UUID         string   `json:"uuid"`
		ShellVersion []string `json:"shell-version"`
	}
	if err := json.Unmarshal([]byte(readRepo(t, "extensions/gnome-shell/metadata.json")), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.UUID != UUID {
		t.Errorf("metadata uuid %q, code %q", meta.UUID, UUID)
	}
	if !slices.Contains(meta.ShellVersion, "45") {
		t.Errorf("shell-version %v: GNOME 45 is the oldest ESM release the extension supports", meta.ShellVersion)
	}

	js := readRepo(t, "extensions/gnome-shell/extension.js")
	jsConst := func(name string) string {
		m := regexp.MustCompile(`(?m)^const ` + name + ` = (.+);$`).FindStringSubmatch(js)
		if m == nil {
			t.Fatalf("extension.js has no const %s", name)
		}
		return strings.Trim(m[1], `'`)
	}
	strs := map[string]string{"SOCKET_NAME": SocketName, "TOKEN_NAME": TokenName, "TOKEN_RE": "/" + reToken.String() + "/"}
	for name, want := range strs {
		if got := jsConst(name); got != want {
			t.Errorf("extension.js %s = %s, code %s", name, got, want)
		}
	}
	ints := map[string]int{
		"VERSION":         Version,
		"MAX_WINDOWS":     MaxWindows,
		"MAX_MONITORS":    MaxMonitors,
		"MAX_CLASS_CHARS": MaxClassBytes / 4,
		"MAX_TITLE_CHARS": MaxTitleBytes / 4,
	}
	for name, want := range ints {
		if got, err := strconv.Atoi(jsConst(name)); err != nil || got != want {
			t.Errorf("extension.js %s = %s, code %d", name, jsConst(name), want)
		}
	}
}

// TestExtensionPrivacy enforces privacy & security §8.3: the extension owns
// no session-bus name and exports no D-Bus object, and it does no network
// I/O. It may only listen on the private socket.
func TestExtensionPrivacy(t *testing.T) {
	js := readRepo(t, "extensions/gnome-shell/extension.js")
	for _, banned := range []string{
		"own_name", "DBusExportedObject", "export(", "Gio.DBus", "bus_get", "DBusConnection",
		"DBusServer", "Soup", "http", "InetSocketAddress", "NetworkAddress", "GLib.spawn", "Subprocess",
	} {
		if strings.Contains(js, banned) {
			t.Errorf("extension.js uses %q", banned)
		}
	}
	if !strings.Contains(js, "get_unix_user()") || !strings.Contains(js, "timingSafeEqual(") {
		t.Error("extension.js must check the caller's uid and token")
	}
	if !strings.Contains(js, "if (titles)") {
		t.Error("extension.js must send titles only when the hello asks")
	}
	if strings.Contains(js, "console.log(") || regexp.MustCompile(`console\.\w+\([^)]*title`).MatchString(js) {
		t.Error("extension.js must not log, and never a title")
	}
}

// TestPackagingShipsExtension: the .deb and .rpm install the extension
// under its uuid, and the post-install message tells the user to enable it
// (packages never enable it themselves, AC-2).
func TestPackagingShipsExtension(t *testing.T) {
	goreleaser := readRepo(t, ".goreleaser.yaml")
	dst := "/usr/share/gnome-shell/extensions/" + UUID + "/"
	for _, f := range []string{"metadata.json", "extension.js", "COPYING"} {
		if !strings.Contains(goreleaser, "src: extensions/gnome-shell/"+f) || !strings.Contains(goreleaser, "dst: "+dst+f) {
			t.Errorf(".goreleaser.yaml does not install %s to %s", f, dst)
		}
	}
	post := readRepo(t, "packaging/nfpm/postinstall.sh")
	if !strings.Contains(post, EnableCommand) {
		t.Errorf("postinstall.sh does not print %q", EnableCommand)
	}
	if strings.Contains(strings.ReplaceAll(post, "  "+EnableCommand, ""), "gnome-extensions enable") {
		t.Error("postinstall.sh must only print the enable command, never run it")
	}
	if _, err := os.Stat(filepath.Join(extensionDir, "COPYING")); err != nil {
		t.Error("the extension must ship its GPL-2.0 licence text")
	}
}
