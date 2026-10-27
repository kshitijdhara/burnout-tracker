package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func dirs(t *testing.T) (string, string) {
	t.Helper()
	data, cfg := filepath.Join(t.TempDir(), "data"), filepath.Join(t.TempDir(), "config")
	for _, d := range []string{data, cfg} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "x"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return data, cfg
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestUninstall(t *testing.T) {
	ctx := context.Background()
	t.Run("no answer changes nothing", func(t *testing.T) {
		data, cfg := dirs(t)
		f := &fakeUninstall{}
		if err := uninstall(ctx, strings.NewReader("yes\n"), io.Discard, true, data, cfg, f.steps()); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 0 || !exists(data) || !exists(cfg) {
			t.Fatalf("calls %v, data %v, cfg %v", f.calls, exists(data), exists(cfg))
		}
	})
	t.Run("plain keeps the data", func(t *testing.T) {
		data, cfg := dirs(t)
		f := &fakeUninstall{}
		if err := uninstall(ctx, strings.NewReader("uninstall\n"), io.Discard, false, data, cfg, f.steps()); err != nil {
			t.Fatal(err)
		}
		if strings.Join(f.calls, ",") != "service,hooks" || !exists(data) || !exists(cfg) {
			t.Fatalf("calls %v, data %v, cfg %v", f.calls, exists(data), exists(cfg))
		}
	})
	t.Run("purge deletes everything", func(t *testing.T) {
		data, cfg := dirs(t)
		f := &fakeUninstall{}
		if err := uninstall(ctx, strings.NewReader("delete\n"), io.Discard, true, data, cfg, f.steps()); err != nil {
			t.Fatal(err)
		}
		if strings.Join(f.calls, ",") != "service,hooks,keys" || exists(data) || exists(cfg) {
			t.Fatalf("calls %v, data %v, cfg %v", f.calls, exists(data), exists(cfg))
		}
	})
	t.Run("purge refuses while a daemon runs", func(t *testing.T) {
		data, cfg := dirs(t)
		f := &fakeUninstall{running: true}
		if err := uninstall(ctx, strings.NewReader("delete\n"), io.Discard, true, data, cfg, f.steps()); err == nil {
			t.Fatal("purged under a running daemon")
		}
		if !exists(data) || !exists(cfg) {
			t.Fatal("deleted data under a running daemon")
		}
	})
	t.Run("a failed step deletes nothing", func(t *testing.T) {
		data, cfg := dirs(t)
		f := &fakeUninstall{fail: "hooks"}
		if err := uninstall(ctx, strings.NewReader("delete\n"), io.Discard, true, data, cfg, f.steps()); err == nil {
			t.Fatal("no error")
		}
		if !exists(data) || !exists(cfg) {
			t.Fatal("deleted data after a failed step")
		}
	})
}
