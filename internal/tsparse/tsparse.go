// Package tsparse parses the human-friendly time arguments accepted by the
// shell and the server (ASOF ...).
package tsparse

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Parse understands:
//
//	1794045600000000000      a raw timestamp (ns since the epoch)
//	-5m, -1h30m              that long before now
//	2026-11-07T10:00:00Z     RFC 3339, with or without fractional seconds
//	now                      now
//
// Positive durations are rejected on purpose: "5m" is more likely a typo
// for "-5m" than a request to read the future.
func Parse(s string, now uint64) (uint64, error) {
	bad := fmt.Errorf("bad time %q: want a timestamp, a negative duration like -5m, RFC3339, or now", s)
	switch {
	case s == "now":
		return now, nil
	case s == "":
		return 0, bad
	case isDigits(s):
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, bad
		}
		return n, nil
	case strings.HasPrefix(s, "-"):
		d, err := time.ParseDuration(s[1:])
		if err != nil || d < 0 {
			return 0, bad
		}
		if uint64(d) > now {
			return 0, nil
		}
		return now - uint64(d), nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.UnixNano() < 0 {
		return 0, bad
	}
	return uint64(t.UnixNano()), nil
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Format renders a timestamp as RFC 3339 in UTC, with the raw number
// alongside so it can be pasted back into an ASOF argument.
func Format(ts uint64) string {
	return fmt.Sprintf("%s (%d)", time.Unix(0, int64(ts)).UTC().Format(time.RFC3339Nano), ts)
}
