package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The README images are generated from a real database session. If the
// engine's output changes, this fails until `go run ./docs/gen` is rerun,
// so the pictures can't quietly drift away from what the code does.
func TestImagesUpToDate(t *testing.T) {
	for name, render := range renderers {
		t.Run(name, func(t *testing.T) {
			got, err := render()
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("..", "img", name))
			if err != nil {
				t.Fatalf("%v (run `go run ./docs/gen` from the repo root)", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("docs/img/%s is stale; run `go run ./docs/gen` from the repo root", name)
			}
		})
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	for name, render := range renderers {
		a, err1 := render()
		b, err2 := render()
		if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
			t.Fatalf("%s: two renders differ (errors %v, %v)", name, err1, err2)
		}
	}
}

func TestDemoShowsTimeTravel(t *testing.T) {
	svg, err := renderers["demo.svg"]()
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	// The second get is "asof -2s" and must return the old value; history
	// must list the deletion.
	for _, want := range []string{"asof -2s", "&#34;{\\&#34;plan\\&#34;:\\&#34;free\\&#34;}&#34;", "(deleted)", "@keyframes"} {
		if !strings.Contains(s, want) {
			t.Errorf("demo.svg is missing %q", want)
		}
	}
}

func TestTimelineProbesComeFromTheEngine(t *testing.T) {
	svg, err := renderers["timeline.svg"]()
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	for _, want := range []string{"ErrHistoryTruncated", "not found", "free", "pro"} {
		if !strings.Contains(s, want) {
			t.Errorf("timeline.svg is missing %q", want)
		}
	}
}
