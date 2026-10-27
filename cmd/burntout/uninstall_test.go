package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/collector/wayland"
	"github.com/kshitijdhara/burntout/internal/adapters/export"
)

type fakeUninstall struct {
	calls   []string
	running bool
	fail    string
}

func (f *fakeUninstall) steps() uninstallSteps {
	step := func(name string) error {
		f.calls = append(f.calls, name)
		if f.fail == name {
			return errors.New(name + " failed")
		}
		return nil
	}
	return uninstallSteps{
		service: func(context.Context) error { return step("service") },
		hooks:   func(context.Context) error { return step("hooks") },
		running: func(context.Context) bool { return f.running },
		keys:    func() error { return step("keys") },
	}
}

func dirs(t *testing.T) uninstallDirs {
	t.Helper()
	tmp := t.TempDir()
	d := uninstallDirs{
		data: filepath.Join(tmp, "data"), config: filepath.Join(tmp, "config"), cache: filepath.Join(tmp, "cache", "burntout"),
		exe: filepath.Join(tmp, "bin", "burntout"), goos: "darwin",
	}
	for _, dir := range []string{d.data, d.config, filepath.Join(d.cache, "updates")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "x"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestUninstall(t *testing.T) {
	ctx := context.Background()
	all := func(d uninstallDirs) bool { return exists(d.data) && exists(d.config) && exists(d.cache) }
	t.Run("no answer changes nothing", func(t *testing.T) {
		d := dirs(t)
		f := &fakeUninstall{}
		if err := uninstall(ctx, strings.NewReader("yes\n"), io.Discard, true, d, f.steps()); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 0 || !all(d) {
			t.Fatalf("calls %v, all there %v", f.calls, all(d))
		}
	})
	t.Run("plain keeps the data", func(t *testing.T) {
		d := dirs(t)
		f := &fakeUninstall{}
		if err := uninstall(ctx, strings.NewReader("uninstall\n"), io.Discard, false, d, f.steps()); err != nil {
			t.Fatal(err)
		}
		if strings.Join(f.calls, ",") != "service,hooks" || !all(d) {
			t.Fatalf("calls %v, all there %v", f.calls, all(d))
		}
	})
	t.Run("purge deletes everything", func(t *testing.T) {
		d := dirs(t)
		f := &fakeUninstall{}
		var out strings.Builder
		if err := uninstall(ctx, strings.NewReader("delete\n"), &out, true, d, f.steps()); err != nil {
			t.Fatal(err)
		}
		if strings.Join(f.calls, ",") != "service,hooks,keys" || exists(d.data) || exists(d.config) || exists(d.cache) {
			t.Fatalf("calls %v, data %v, cfg %v, cache %v", f.calls, exists(d.data), exists(d.config), exists(d.cache))
		}
		if !strings.Contains(out.String(), d.cache+" (downloaded updates)") {
			t.Errorf("the question doesn't name the updates cache:\n%s", out.String())
		}
	})
	t.Run("purge deletes what the old name left behind", func(t *testing.T) {
		d := dirs(t)
		old := filepath.Join(t.TempDir(), "burnout")
		if err := os.MkdirAll(old, 0o700); err != nil {
			t.Fatal(err)
		}
		d.legacy = []string{old}
		f := &fakeUninstall{}
		var out strings.Builder
		if err := uninstall(ctx, strings.NewReader("delete\n"), &out, true, d, f.steps()); err != nil {
			t.Fatal(err)
		}
		if exists(old) {
			t.Fatal("the old folder is left after a purge")
		}
		if !strings.Contains(out.String(), old+" (from burnout, this app's old name)") {
			t.Errorf("the question doesn't name the old folder:\n%s", out.String())
		}
	})
	t.Run("purge lists exports first", func(t *testing.T) {
		d := dirs(t)
		saved := filepath.Join(t.TempDir(), "burntout-export.json")
		if err := os.WriteFile(saved, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := export.Remember(d.data, export.Entry{At: time.Now(), Path: saved, Format: "json"}); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := uninstall(ctx, strings.NewReader("no\n"), &out, true, d, (&fakeUninstall{}).steps()); err != nil {
			t.Fatal(err)
		}
		listed, asked := strings.Index(out.String(), saved), strings.Index(out.String(), `Type "delete"`)
		if listed < 0 || asked < listed {
			t.Errorf("the export isn't listed before the question:\n%s", out.String())
		}
	})
	t.Run("leftover hints", func(t *testing.T) {
		d := dirs(t)
		d.goos = "linux"
		if err := os.MkdirAll(filepath.Dir(d.exe), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.exe+".old", []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := uninstall(ctx, strings.NewReader("uninstall\n"), &out, false, d, (&fakeUninstall{}).steps()); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{d.exe + ".old", "gnome-extensions disable " + wayland.UUID} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("no hint about %s:\n%s", want, out.String())
			}
		}
		d = dirs(t) // no .old, not Linux: no hints
		out.Reset()
		if err := uninstall(ctx, strings.NewReader("uninstall\n"), &out, false, d, (&fakeUninstall{}).steps()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), ".old") || strings.Contains(out.String(), "gnome-extensions") {
			t.Errorf("hints with nothing left over:\n%s", out.String())
		}
	})
	t.Run("purge refuses while a daemon runs", func(t *testing.T) {
		d := dirs(t)
		f := &fakeUninstall{running: true}
		if err := uninstall(ctx, strings.NewReader("delete\n"), io.Discard, true, d, f.steps()); err == nil {
			t.Fatal("purged under a running daemon")
		}
		if !all(d) {
			t.Fatal("deleted data under a running daemon")
		}
	})
	t.Run("a failed step deletes nothing", func(t *testing.T) {
		d := dirs(t)
		f := &fakeUninstall{fail: "hooks"}
		if err := uninstall(ctx, strings.NewReader("delete\n"), io.Discard, true, d, f.steps()); err == nil {
			t.Fatal("no error")
		}
		if !all(d) {
			t.Fatal("deleted data after a failed step")
		}
	})
}
