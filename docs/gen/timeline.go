package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mnazarkhudoev/chronokv"
)

// The timeline story: one key, three writes, a 2h retention window, and a
// full compaction before reading. Every probe result in the picture is the
// answer the engine actually gave.
var (
	day        = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	at         = func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	retention  = 2 * time.Hour
	viewFrom   = at(8, 30)
	viewTo     = at(12, 20)
	readAt     = at(12, 0)
	timelineOp = []struct {
		t     time.Time
		label string
		value string // "" = delete
	}{
		{at(9, 0), "Put", "free"},
		{at(10, 30), "Put", "pro"},
		{at(11, 15), "Delete", ""},
	}
	probes = []time.Time{at(9, 30), at(10, 10), at(10, 50), at(11, 40)}
)

type probe struct {
	t      time.Time
	result string
	class  string
}

func runTimeline() ([]probe, error) {
	dir, err := os.MkdirTemp("", "chronokv-timeline")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	c := &clock{t: timelineOp[0].t}
	db, err := chronokv.Open(dir, chronokv.Options{Clock: c.Now, Retention: retention})
	if err != nil {
		return nil, err
	}
	defer db.Close()

	key := []byte("user:42")
	for _, op := range timelineOp {
		c.Set(op.t)
		if op.value == "" {
			_, err = db.Delete(key)
		} else {
			_, err = db.Put(key, []byte(op.value))
		}
		if err != nil {
			return nil, err
		}
	}
	c.Set(readAt)
	if err := db.Compact(); err != nil {
		return nil, err
	}

	var out []probe
	for _, t := range probes {
		v, err := db.Get(key, chronokv.AsOf(chronokv.Timestamp(t.UnixNano())))
		p := probe{t: t}
		switch {
		case errors.Is(err, chronokv.ErrHistoryTruncated):
			p.result, p.class = "ErrHistoryTruncated", "bad"
		case errors.Is(err, chronokv.ErrNotFound):
			p.result, p.class = "not found", "dim"
		case err != nil:
			return nil, err
		default:
			p.result, p.class = string(v), "v-"+string(v)
		}
		out = append(out, p)
	}
	return out, nil
}

func renderTimeline() ([]byte, error) {
	probes, err := runTimeline()
	if err != nil {
		return nil, err
	}

	const (
		W, H    = 780, 330
		left    = 60.0
		right   = 740.0
		axisY   = 178.0
		barY    = 132.0
		barH    = 22.0
		topZone = 76.0
	)
	x := func(t time.Time) float64 {
		return left + float64(t.Sub(viewFrom))/float64(viewTo.Sub(viewFrom))*(right-left)
	}
	hm := func(t time.Time) string { return t.Format("15:04") }
	cutoff := readAt.Add(-retention)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="Timeline of key user:42: set to free at 09:00, pro at 10:30, deleted at 11:15, with a two hour retention window read at 12:00">`+"\n", W, H, W, H)
	b.WriteString(`<style>
text{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;fill:#c9d1d9;font-size:12px}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,'Liberation Mono',monospace}
.title{font-size:16px;font-weight:600;fill:#e6edf3}.sub{fill:#8b949e;font-size:12.5px}
.dim{fill:#8b949e}.bad{fill:#ff7b72}.v-free{fill:#e3b341}.v-pro{fill:#7ee787}.small{font-size:11px}
</style>
<defs>
<pattern id="hatch" width="8" height="8" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="8" height="8" fill="#f85149" fill-opacity=".06"/><line x1="0" y1="0" x2="0" y2="8" stroke="#f85149" stroke-opacity=".22" stroke-width="3"/></pattern>
<marker id="arrow" viewBox="0 0 10 10" refX="5" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="#8b949e"/></marker>
</defs>
`)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" rx="12" fill="#0d1117" stroke="#30363d"/>`+"\n", W, H)
	b.WriteString(`<text x="28" y="36" class="title">One key, every version, any point in time</text>` + "\n")
	fmt.Fprintf(&b, `<text x="28" y="57" class="sub"><tspan class="mono">user:42</tspan> with Retention: %s, read at %s after a full compaction. Every answer below came from the engine.</text>`+"\n",
		strings.TrimSuffix(retention.String(), "0m0s"), hm(readAt))

	// Retention zone and cutoff.
	fmt.Fprintf(&b, `<rect x="%.1f" y="%.0f" width="%.1f" height="%.0f" fill="url(#hatch)"/>`+"\n", left, topZone, x(cutoff)-left, 196-topZone+44)
	fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" class="bad small">older than the retention window</text>`+"\n", left+8, topZone+16)
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f" stroke="#f85149" stroke-dasharray="4 4"/>`+"\n", x(cutoff), topZone, x(cutoff), axisY+44)
	fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" class="bad small">cutoff = now − %s</text>`+"\n", x(cutoff)+6, topZone+16, strings.TrimSuffix(retention.String(), "0m0s"))

	// Value bars: how long each version was the current one.
	for i, op := range timelineOp {
		end := readAt
		if i+1 < len(timelineOp) {
			end = timelineOp[i+1].t
		}
		x0, x1 := x(op.t), x(end)
		if op.value == "" {
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.0f" width="%.1f" height="%.0f" rx="4" fill="none" stroke="#6e7681" stroke-dasharray="4 3"/>`+"\n", x0, barY, x1-x0, barH)
			fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle" class="dim">deleted</text>`+"\n", (x0+x1)/2, barY+15)
		} else {
			color := map[string]string{"free": "#e3b341", "pro": "#7ee787"}[op.value]
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.0f" width="%.1f" height="%.0f" rx="4" fill="%s" fill-opacity=".18" stroke="%s"/>`+"\n", x0, barY, x1-x0, barH, color, color)
			fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle" class="mono v-%s">%q</text>`+"\n", (x0+x1)/2, barY+15, op.value, op.value)
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" class="small dim">%s %s</text>`+"\n", x0, barY-8, op.label, hm(op.t))
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.0f" r="4.5" fill="#e6edf3"/>`+"\n", x0, axisY)
	}

	// Axis with hour ticks and "now".
	fmt.Fprintf(&b, `<line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" stroke="#8b949e" stroke-width="1.5" marker-end="url(#arrow)"/>`+"\n", left, axisY, right+12, axisY)
	for h := 9; h <= 12; h++ {
		tx := x(at(h, 0))
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f" stroke="#8b949e"/>`+"\n", tx, axisY-4, tx, axisY+4)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle" class="small dim">%02d:00</text>`+"\n", tx, axisY+18, h)
	}
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f" stroke="#58a6ff" stroke-dasharray="2 3"/>`+"\n", x(readAt), topZone, x(readAt), axisY)
	fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle" class="small" style="fill:#58a6ff">now</text>`+"\n", x(readAt), topZone+16)

	// Probes: reads at past moments, with the engine's answers.
	for _, p := range probes {
		px := x(p.t)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f" stroke="#8b949e" stroke-dasharray="2 2" marker-end="url(#arrow)"/>`+"\n", px, axisY+58, px, axisY+10)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle" class="mono small">AsOf(%s)</text>`+"\n", px, axisY+74, hm(p.t))
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle" class="mono %s">→ %s</text>`+"\n", px, axisY+92, p.class, esc(p.result))
	}

	fmt.Fprintf(&b, `<text x="28" y="%d" class="small dim">The %s version is older than the cutoff but survives compaction: it's still the right answer for reads between %s and %s.</text>`+"\n",
		H-18, hm(timelineOp[0].t), hm(cutoff), hm(timelineOp[1].t))
	b.WriteString("</svg>\n")
	return []byte(b.String()), nil
}
