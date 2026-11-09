package main

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mnazarkhudoev/chronokv"
	"github.com/mnazarkhudoev/chronokv/internal/shell"
)

// step is one command typed into the shell. wait is how much (fake) time
// passes before it; note, if set, is shown as a small annotation instead of
// a command.
type step struct {
	wait time.Duration
	cmd  string
	note string
}

var demoScript = []step{
	{cmd: `put user:42 {"plan":"free"}`},
	{wait: 3 * time.Second, note: "3 seconds later"},
	{cmd: `put user:42 {"plan":"pro"}`},
	{wait: 400 * time.Millisecond, cmd: `get user:42`},
	{wait: 400 * time.Millisecond, cmd: `get user:42 asof -2s`},
	{wait: 400 * time.Millisecond, cmd: `del user:42`},
	{wait: 400 * time.Millisecond, cmd: `get user:42`},
	{wait: 400 * time.Millisecond, cmd: `history user:42`},
}

type recorded struct {
	step
	out []string
}

// recordDemo runs demoScript through the real shell against a real
// database and returns what it printed.
func recordDemo() ([]recorded, error) {
	dir, err := os.MkdirTemp("", "chronokv-demo")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	c := &clock{t: time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)}
	db, err := chronokv.Open(dir, chronokv.Options{Clock: c.Now})
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var out bytes.Buffer
	sh := shell.New(db, &out)
	var rec []recorded
	for _, s := range demoScript {
		c.Advance(s.wait)
		r := recorded{step: s}
		if s.cmd != "" {
			out.Reset()
			sh.RunLine(s.cmd)
			r.out = strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		}
		rec = append(rec, r)
	}
	return rec, nil
}

// Terminal look. The colors are GitHub's dark palette, so the window looks
// at home in both of GitHub's themes.
const (
	termWidth   = 780
	titleH      = 34
	padX        = 20
	padY        = 18
	lineH       = 21
	fontSize    = 13.5
	charW       = 8.15 // advance of one monospace glyph at fontSize, roughly
	promptText  = "chronokv> "
	typePerChar = 0.035 // seconds per typed character
)

type row struct {
	kind  string // "cmd", "out", "note"
	text  string
	at    float64 // second it appears
	typed float64 // for commands: second typing ends
}

func renderDemo() ([]byte, error) {
	rec, err := recordDemo()
	if err != nil {
		return nil, err
	}

	// Lay the session out on a timeline (in seconds of animation).
	var rows []row
	t := 0.8
	for _, r := range rec {
		if r.note != "" {
			rows = append(rows, row{kind: "note", text: "  ⋯ " + r.note + " ⋯", at: t})
			t += 1.3
			continue
		}
		typing := min(float64(utf8.RuneCountInString(r.cmd))*typePerChar, 1.2)
		rows = append(rows, row{kind: "cmd", text: r.cmd, at: t, typed: t + typing})
		t += typing + 0.35
		for _, line := range r.out {
			rows = append(rows, row{kind: "out", text: line, at: t})
			t += 0.06
		}
		t += 0.9
	}
	total := t + 3.5 // hold the finished screen for a moment, then loop
	pct := func(s float64) string { return fmt.Sprintf("%.2f%%", 100*s/total) }
	fadeStart, fadeEnd := pct(total-0.8), pct(total-0.3)

	height := titleH + 2*padY + len(rows)*lineH
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="chronokv shell demo: a key is written twice, read now and as of two seconds ago, deleted, and its history listed">`+"\n",
		termWidth, height, termWidth, height)
	b.WriteString("<style>\n")
	fmt.Fprintf(&b, "text{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,'Liberation Mono',monospace;font-size:%.1fpx;white-space:pre}\n", fontSize)
	b.WriteString(".p{fill:#7ee787}.c{fill:#e6edf3}.o{fill:#c9d1d9}.d{fill:#8b949e}.s{fill:#ffa657}.x{fill:#ff7b72}.n{fill:#6e7681;font-style:italic}\n")
	for i, r := range rows {
		fmt.Fprintf(&b, ".r%d{animation:k%d %.2fs linear infinite}@keyframes k%d{0%%,%s{opacity:0}%s,%s{opacity:1}%s,100%%{opacity:0}}\n",
			i, i, total, i, pct(r.at), pct(r.at+0.01), fadeStart, fadeEnd)
		if r.kind == "cmd" {
			w := float64(utf8.RuneCountInString(r.text))*charW + 14
			n := utf8.RuneCountInString(r.text)
			// A background-colored cover slides right in steps, which
			// reveals the command one character at a time.
			fmt.Fprintf(&b, ".t%d{animation:m%d %.2fs linear infinite}@keyframes m%d{0%%,%s{transform:translateX(0);animation-timing-function:steps(%d,end)}%s,100%%{transform:translateX(%.0fpx)}}\n",
				i, i, total, i, pct(r.at), n, pct(r.typed), w)
			// The cursor only shows while this command is being typed.
			fmt.Fprintf(&b, ".u%d{animation:q%d %.2fs linear infinite}@keyframes q%d{0%%,%s{opacity:0}%s,%s{opacity:.8}%s,100%%{opacity:0}}\n",
				i, i, total, i, pct(r.at), pct(r.at+0.01), pct(r.typed+0.3), pct(r.typed+0.31))
		}
	}
	b.WriteString("@media (prefers-reduced-motion:reduce){[class^=r]{animation:none}[class^=t],[class^=u]{display:none}}\n")
	b.WriteString("</style>\n")

	// Window chrome.
	fmt.Fprintf(&b, `<rect width="%d" height="%d" rx="10" fill="#0d1117" stroke="#30363d"/>`+"\n", termWidth, height)
	fmt.Fprintf(&b, `<path d="M1 %d V11 a10 10 0 0 1 10 -10 H%d a10 10 0 0 1 10 10 V%d Z" fill="#161b22"/>`+"\n", titleH, termWidth-11, titleH)
	fmt.Fprintf(&b, `<line x1="1" y1="%d" x2="%d" y2="%d" stroke="#30363d"/>`+"\n", titleH, termWidth-1, titleH)
	for i, col := range []string{"#ff5f57", "#febc2e", "#28c840"} {
		fmt.Fprintf(&b, `<circle cx="%d" cy="17" r="6" fill="%s"/>`+"\n", 22+i*20, col)
	}
	fmt.Fprintf(&b, `<text x="%d" y="22" text-anchor="middle" class="d" style="font-size:12px">chronokv ./data</text>`+"\n", termWidth/2)

	for i, r := range rows {
		y := titleH + padY + i*lineH + 15
		fmt.Fprintf(&b, `<g class="r%d">`, i)
		switch r.kind {
		case "cmd":
			cmdX := float64(padX) + float64(len(promptText))*charW
			fmt.Fprintf(&b, `<text x="%d" y="%d"><tspan class="p">%s</tspan><tspan class="c">%s</tspan></text>`,
				padX, y, esc(promptText), esc(r.text))
			fmt.Fprintf(&b, `<g class="t%d"><rect x="%.1f" y="%d" width="%.0f" height="%d" fill="#0d1117"/><rect class="u%d" x="%.1f" y="%d" width="8" height="16" fill="#7ee787"/></g>`,
				i, cmdX, y-15, float64(utf8.RuneCountInString(r.text))*charW+14, lineH, i, cmdX, y-12)
		case "note":
			fmt.Fprintf(&b, `<text x="%d" y="%d" class="n">%s</text>`, padX, y, esc(r.text))
		default:
			fmt.Fprintf(&b, `<text x="%d" y="%d">%s</text>`, padX, y, outputSpans(r.text))
		}
		b.WriteString("</g>\n")
	}
	b.WriteString("</svg>\n")
	return []byte(b.String()), nil
}

// outputSpans colors one line of shell output.
func outputSpans(line string) string {
	switch {
	case strings.HasPrefix(line, "ok @ "):
		return `<tspan class="d">` + esc(line) + `</tspan>`
	case line == "(nil)":
		return `<tspan class="d">` + esc(line) + `</tspan>`
	case strings.HasPrefix(line, `"`):
		return `<tspan class="s">` + esc(line) + `</tspan>`
	}
	// history: "<timestamp>  <value or (deleted)>"
	if ts, val, ok := strings.Cut(line, "  "); ok {
		cls := "s"
		if val == "(deleted)" {
			cls = "x"
		}
		return `<tspan class="d">` + esc(ts) + `  </tspan><tspan class="` + cls + `">` + esc(val) + `</tspan>`
	}
	return `<tspan class="o">` + esc(line) + `</tspan>`
}

func esc(s string) string { return html.EscapeString(s) }
