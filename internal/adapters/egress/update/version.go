package update

import (
	"errors"
	"strconv"
	"strings"
)

// Version is a semantic version: MAJOR.MINOR.PATCH with an optional
// pre-release ("1.2.0-beta.1"). Build metadata after "+" is ignored.
type Version struct {
	Major, Minor, Patch int
	Pre                 []string // dot-separated pre-release identifiers
}

// ParseVersion reads "1.2.3", "v1.2.3" or "1.2.3-rc.1".
func ParseVersion(s string) (Version, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(s), "v")
	raw, _, _ = strings.Cut(raw, "+")
	core, pre, hasPre := strings.Cut(raw, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, errors.New("update: not a version like 1.2.3")
	}
	var nums [3]int
	for i, p := range parts {
		n, err := number(p)
		if err != nil {
			return Version{}, errors.New("update: not a version like 1.2.3")
		}
		nums[i] = n
	}
	v := Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}
	if hasPre {
		v.Pre = strings.Split(pre, ".")
		for _, id := range v.Pre {
			if id == "" || strings.Trim(id, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-") != "" {
				return Version{}, errors.New("update: bad pre-release in version")
			}
		}
	}
	return v, nil
}

// number reads a decimal without sign or leading zeros.
func number(s string) (int, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') || strings.Trim(s, "0123456789") != "" {
		return 0, errors.New("not a number")
	}
	return strconv.Atoi(s)
}

// String is the version without a leading v.
func (v Version) String() string {
	s := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// Prerelease reports whether v is a pre-release.
func (v Version) Prerelease() bool { return len(v.Pre) > 0 }

// ChannelFor is the channel a build follows by default: Beta for a
// pre-release build such as an alpha, whose successors are pre-releases
// too and which GitHub's "latest" release never names; Stable otherwise,
// including for dev builds.
func ChannelFor(current string) Channel {
	if v, err := ParseVersion(current); err == nil && v.Prerelease() {
		return Beta
	}
	return Stable
}

// Compare returns -1, 0 or 1 as v is older than, the same as or newer
// than w, by semver precedence: a pre-release sorts before its release,
// and numeric identifiers compare as numbers, below alphanumeric ones.
func (v Version) Compare(w Version) int {
	for _, d := range [][2]int{{v.Major, w.Major}, {v.Minor, w.Minor}, {v.Patch, w.Patch}} {
		if c := cmpInt(d[0], d[1]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.Pre) == 0 && len(w.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(w.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(w.Pre); i++ {
		if c := cmpIdent(v.Pre[i], w.Pre[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(v.Pre), len(w.Pre))
}

func cmpIdent(a, b string) int {
	na, errA := number(a)
	nb, errB := number(b)
	switch {
	case errA == nil && errB == nil:
		return cmpInt(na, nb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
