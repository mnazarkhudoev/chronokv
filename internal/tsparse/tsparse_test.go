package tsparse

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	const now = 10_000_000_000
	rfc := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want uint64
		err  bool
	}{
		{"123", 123, false},
		{"0", 0, false},
		{"-1s", 9_000_000_000, false},
		{"-1m30s", 0, false}, // clamps at zero instead of wrapping around
		{"-20s", 0, false},
		{"now", now, false},
		{"2026-11-07T10:00:00Z", uint64(rfc.UnixNano()), false},
		{"2026-11-07T10:00:00.5+00:00", uint64(rfc.Add(500 * time.Millisecond).UnixNano()), false},
		{"5m", 0, true},
		{"yesterday", 0, true},
		{"", 0, true},
		{"-", 0, true},
		{"99999999999999999999999", 0, true},
	}
	for _, c := range cases {
		got, err := Parse(c.in, now)
		if c.err {
			if err == nil {
				t.Errorf("Parse(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("Parse(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
}

func TestFormat(t *testing.T) {
	ts := uint64(time.Date(2026, 11, 7, 10, 0, 0, 123456789, time.UTC).UnixNano())
	want := "2026-11-07T10:00:00.123456789Z (1794045600123456789)"
	if got := Format(ts); got != want {
		t.Fatalf("Format = %q, want %q", got, want)
	}
}
