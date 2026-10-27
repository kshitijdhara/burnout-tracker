package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Options is one `burnout update`.
type Options struct {
	Current        string  // the running version ("dev" when built without one)
	Channel        Channel // used when To is empty
	To             string  // a specific version to install, "" for the newest
	AllowDowngrade bool    // allow a version ≤ Current
	CheckOnly      bool    // find the version, change nothing
	Install        Install
	DownloadDir    string // where a package or app download is saved
}

// Result is what Run did.
type Result struct {
	Version  Version // the release chosen
	UpToDate bool    // it is the running version; nothing was done
	Swapped  bool    // the binary was replaced in place
	Previous string  // where the replaced binary was kept, when Swapped
	Download string  // a verified package or app for the user to install
}

// ErrDowngrade refuses a release that isn't newer than the running one.
var ErrDowngrade = errors.New("update: that release isn't newer than this one; add --allow-downgrade to install it anyway")

// ErrDevBuild refuses to compare against a build without a version.
var ErrDevBuild = errors.New("update: this is a development build without a version, so it can't tell which release is newer; " +
	"add --allow-downgrade to install a release over it")

// Run finds the release, verifies it and installs it the way o.Install
// allows. Nothing on disk changes until the checksums' signature and the
// download's SHA-256 have both been checked.
func (u *Updater) Run(ctx context.Context, o Options) (Result, error) {
	var r Release
	var err error
	if o.To != "" {
		v, perr := ParseVersion(o.To)
		if perr != nil {
			return Result{}, fmt.Errorf("update: --to %.40q: want a version like v1.2.3", o.To)
		}
		r, err = u.Tag(ctx, v)
	} else {
		r, err = u.Latest(ctx, o.Channel)
	}
	if err != nil {
		return Result{}, err
	}
	res := Result{Version: r.Version}

	cur, err := ParseVersion(o.Current)
	switch {
	case err != nil && !o.AllowDowngrade:
		return res, ErrDevBuild
	case err != nil:
	case r.Version.Compare(cur) == 0 && !o.AllowDowngrade:
		res.UpToDate = true
		return res, nil
	case r.Version.Compare(cur) < 0 && !o.AllowDowngrade:
		return res, ErrDowngrade
	}
	if o.CheckOnly {
		return res, nil
	}

	sums, err := u.Checksums(ctx, r)
	if err != nil {
		return res, err
	}
	name, err := o.Install.Asset(r.Version)
	if err != nil {
		return res, err
	}
	if !r.Has(name) {
		return res, fmt.Errorf("update: release %s has no %s for this system", r.Tag, name)
	}

	if o.Install.Kind == UserPrefix {
		// Into the binary's own folder, so the swap is a rename.
		path, err := u.Download(ctx, r, sums, name, filepath.Dir(o.Install.Path))
		if err != nil {
			return res, err
		}
		defer func() { _ = os.Remove(path) }()
		if err := o.Install.Swap(path); err != nil {
			return res, err
		}
		res.Swapped, res.Previous = true, o.Install.Previous()
		return res, nil
	}

	if err := os.MkdirAll(o.DownloadDir, 0o700); err != nil {
		return res, err
	}
	path, err := u.Download(ctx, r, sums, name, o.DownloadDir)
	if err != nil {
		return res, err
	}
	dst := filepath.Join(o.DownloadDir, name)
	if err := os.Rename(path, dst); err != nil {
		_ = os.Remove(path)
		return res, err
	}
	res.Download = dst
	return res, nil
}

// Check returns the newest version on ch, for the weekly check. It only
// reads the release list: nothing is downloaded.
func (u *Updater) Check(ctx context.Context, ch Channel) (Version, error) {
	r, err := u.Latest(ctx, ch)
	if err != nil {
		return Version{}, err
	}
	return r.Version, nil
}
