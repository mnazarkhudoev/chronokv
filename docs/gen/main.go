// Command gen draws the images used in the README from a real chronokv
// session, so they always match what the engine actually does.
//
//	go run ./docs/gen
//
// Run it from the repository root; it writes docs/img/*.svg. A test in this
// package fails when the committed images are out of date.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var renderers = map[string]func() ([]byte, error){
	"demo.svg":     renderDemo,
	"timeline.svg": renderTimeline,
}

func main() {
	for name, render := range renderers {
		svg, err := render()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			os.Exit(1)
		}
		path := filepath.Join("docs", "img", name)
		if err := os.WriteFile(path, svg, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(svg))
	}
}

// clock is a settable time source, so the generated images are identical
// on every run.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *clock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }
