package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mnazarkhudoev/chronokv"
	"github.com/mnazarkhudoev/chronokv/internal/tsparse"
)

func TestShellTranscript(t *testing.T) {
	start := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	db, err := chronokv.Open(t.TempDir(), chronokv.Options{Clock: func() time.Time { return start }})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var out bytes.Buffer
	sh := newShell(db, &out)

	T := uint64(start.UnixNano())
	f := tsparse.Format
	script := []struct{ in, want string }{
		{"put a 1", "ok @ " + f(T)},
		{"get a", `"1"`},
		{"del a", "ok @ " + f(T+1)},
		{"get a", "(nil)"},
		{fmt.Sprintf("get a asof %d", T), `"1"`},
		{`put "a\x00" z`, "ok @ " + f(T+2)},
		{"put b \"two words\"", "ok @ " + f(T+3)},
		{"scan", `"a\x00" => "z"` + "\n" + `"b" => "two words"`},
		{"scan b", `"b" => "two words"`},
		{"scan a b", `"a\x00" => "z"`},
		{"scan limit 1", `"a\x00" => "z"`},
		{"history a", f(T+1) + "  (deleted)\n" + f(T) + `  "1"`},
		{"history a limit 1", f(T+1) + "  (deleted)"},
		{"get nothing", "(nil)"},
		{"bogus", `error: unknown command "bogus" (try help)`},
		{"get", "error: usage: get <key> [asof <time>]"},
		{"get a asof soon", `error: bad time "soon": want a timestamp, a negative duration like -5m, RFC3339, or now`},
		{"flush", "ok"},
		{"compact", "ok"},
		{"get b", `"two words"`},
	}
	for _, step := range script {
		out.Reset()
		if quit := sh.runLine(step.in); quit {
			t.Fatalf("%q quit the shell", step.in)
		}
		if got := strings.TrimRight(out.String(), "\n"); got != step.want {
			t.Fatalf("%s\n got: %s\nwant: %s", step.in, got, step.want)
		}
	}
	for _, q := range []string{"quit", "exit"} {
		if !sh.runLine(q) {
			t.Fatalf("%q did not quit", q)
		}
	}
	out.Reset()
	if sh.runLine("   "); out.Len() != 0 {
		t.Fatalf("blank line printed %q", out.String())
	}
}

func TestTokenize(t *testing.T) {
	got, err := tokenize(`put "a b" c "\x00"`)
	if err != nil || len(got) != 4 || got[1] != "a b" || got[3] != "\x00" {
		t.Fatalf("tokenize = %q, %v", got, err)
	}
	if _, err := tokenize(`put "unterminated`); err == nil {
		t.Fatal("unterminated quote should fail")
	}
}
