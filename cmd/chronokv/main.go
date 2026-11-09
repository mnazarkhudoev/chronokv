// Command chronokv is an interactive shell for poking at a chronokv
// database directory.
//
//	chronokv [-retention 24h] ./data
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"

	"github.com/mnazarkhudoev/chronokv"
	"github.com/mnazarkhudoev/chronokv/internal/shell"
)

func main() {
	retention := flag.Duration("retention", 0, "history to keep (0 = forever)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: chronokv [-retention 24h] <dir>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	db, err := chronokv.Open(flag.Arg(0), chronokv.Options{Retention: *retention})
	if err != nil {
		fmt.Fprintln(os.Stderr, "chronokv:", err)
		os.Exit(1)
	}
	defer db.Close()

	sh := shell.New(db, os.Stdout)
	interactive := false
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
		interactive = true
		fmt.Println(`chronokv shell, type "help" for commands`)
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 64<<20)
	for {
		if interactive {
			fmt.Print("chronokv> ")
		}
		if !in.Scan() {
			break
		}
		if sh.RunLine(in.Text()) {
			break
		}
	}
}
