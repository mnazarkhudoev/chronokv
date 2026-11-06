package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/mnazarkhudoev/chronokv"
	"github.com/mnazarkhudoev/chronokv/internal/tsparse"
)

const helpText = `commands:
  put <key> <value>                      write a value
  get <key> [asof <time>]                read (optionally as of a past time)
  del <key>                              delete
  scan [<start> [<end>]] [asof <time>] [limit <n>]
  history <key> [limit <n>]              every stored version, newest first
  now                                    current database timestamp
  flush                                  flush memtables to disk
  compact                                full compaction
  help, quit

keys and values are bare words or Go-style quoted strings ("a b", "\x00").
<time> is a raw timestamp, a negative duration (-5m), RFC3339, or now.`

type shell struct {
	db  *chronokv.DB
	out io.Writer
}

func newShell(db *chronokv.DB, out io.Writer) *shell { return &shell{db: db, out: out} }

var errQuit = errors.New("quit")

type usageError string

func (u usageError) Error() string { return "usage: " + string(u) }

// runLine executes one line and prints the result or the error. It returns
// true when the user asked to quit.
func (s *shell) runLine(line string) bool {
	err := s.exec(line)
	if errors.Is(err, errQuit) {
		return true
	}
	if err != nil {
		fmt.Fprintln(s.out, "error:", err)
	}
	return false
}

func (s *shell) exec(line string) error {
	args, err := tokenize(line)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return nil
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "put":
		if len(args) != 2 {
			return usageError("put <key> <value>")
		}
		ts, err := s.db.Put([]byte(args[0]), []byte(args[1]))
		return s.ok(ts, err)
	case "del":
		if len(args) != 1 {
			return usageError("del <key>")
		}
		ts, err := s.db.Delete([]byte(args[0]))
		return s.ok(ts, err)
	case "get":
		return s.get(args)
	case "scan":
		return s.scan(args)
	case "history":
		return s.history(args)
	case "now":
		fmt.Fprintln(s.out, tsparse.Format(uint64(s.db.Now())))
		return nil
	case "flush":
		return s.done(s.db.Flush())
	case "compact":
		return s.done(s.db.Compact())
	case "help":
		fmt.Fprintln(s.out, helpText)
		return nil
	case "quit", "exit":
		return errQuit
	}
	return fmt.Errorf("unknown command %q (try help)", cmd)
}

func (s *shell) ok(ts chronokv.Timestamp, err error) error {
	if err != nil {
		return err
	}
	fmt.Fprintln(s.out, "ok @", tsparse.Format(uint64(ts)))
	return nil
}

func (s *shell) done(err error) error {
	if err == nil {
		fmt.Fprintln(s.out, "ok")
	}
	return err
}

// options pulls "asof <t>" and "limit <n>" pairs out of args and returns
// what's left.
func (s *shell) options(args []string) (rest []string, opts []chronokv.ReadOption, limit int, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "asof", "limit":
			if i+1 >= len(args) {
				return nil, nil, 0, fmt.Errorf("%s needs a value", args[i])
			}
			v := args[i+1]
			i++
			if args[i-1] == "asof" {
				ts, err := tsparse.Parse(v, uint64(s.db.Now()))
				if err != nil {
					return nil, nil, 0, err
				}
				opts = append(opts, chronokv.AsOf(chronokv.Timestamp(ts)))
			} else if limit, err = strconv.Atoi(v); err != nil || limit < 1 {
				return nil, nil, 0, fmt.Errorf("bad limit %q", v)
			}
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, opts, limit, nil
}

func (s *shell) get(args []string) error {
	rest, opts, _, err := s.options(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usageError("get <key> [asof <time>]")
	}
	v, err := s.db.Get([]byte(rest[0]), opts...)
	if errors.Is(err, chronokv.ErrNotFound) {
		fmt.Fprintln(s.out, "(nil)")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "%q\n", v)
	return nil
}

func (s *shell) scan(args []string) error {
	rest, opts, limit, err := s.options(args)
	if err != nil {
		return err
	}
	if len(rest) > 2 {
		return usageError("scan [<start> [<end>]] [asof <time>] [limit <n>]")
	}
	if limit == 0 {
		limit = 100
	}
	var start, end []byte
	if len(rest) > 0 {
		start = []byte(rest[0])
	}
	if len(rest) > 1 {
		end = []byte(rest[1])
	}
	it := s.db.Scan(start, end, opts...)
	defer it.Close()
	n := 0
	for n < limit && it.Next() {
		fmt.Fprintf(s.out, "%q => %q\n", it.Key(), it.Value())
		n++
	}
	if err := it.Err(); err != nil {
		return err
	}
	if n == 0 {
		fmt.Fprintln(s.out, "(empty)")
	}
	return nil
}

func (s *shell) history(args []string) error {
	rest, _, limit, err := s.options(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usageError("history <key> [limit <n>]")
	}
	vs, err := s.db.History([]byte(rest[0]), limit)
	if err != nil {
		return err
	}
	if len(vs) == 0 {
		fmt.Fprintln(s.out, "(no history)")
	}
	for _, v := range vs {
		if v.Deleted {
			fmt.Fprintf(s.out, "%s  (deleted)\n", tsparse.Format(uint64(v.TS)))
		} else {
			fmt.Fprintf(s.out, "%s  %q\n", tsparse.Format(uint64(v.TS)), v.Value)
		}
	}
	return nil
}

// tokenize splits a line on whitespace, treating "double quoted" words as
// Go string literals so keys can contain spaces and arbitrary bytes.
func tokenize(line string) ([]string, error) {
	var out []string
	for i := 0; i < len(line); {
		switch c := line[i]; {
		case c == ' ' || c == '\t':
			i++
		case c == '"':
			j := i + 1
			for ; j < len(line); j++ {
				if line[j] == '\\' {
					j++
				} else if line[j] == '"' {
					break
				}
			}
			if j >= len(line) {
				return nil, errors.New("unterminated quote")
			}
			s, err := strconv.Unquote(line[i : j+1])
			if err != nil {
				return nil, fmt.Errorf("bad quoted string %s", line[i:j+1])
			}
			out = append(out, s)
			i = j + 1
		default:
			j := i
			for j < len(line) && line[j] != ' ' && line[j] != '\t' {
				j++
			}
			out = append(out, line[i:j])
			i = j
		}
	}
	return out, nil
}
