package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fm(num uint64, small, large string) FileMeta {
	return FileMeta{Num: num, Size: int64(num) * 100, Smallest: []byte(small), Largest: []byte(large)}
}

func nums(files []FileMeta) []uint64 {
	var out []uint64
	for _, f := range files {
		out = append(out, f.Num)
	}
	return out
}

func eqNums(t *testing.T, what string, got []uint64, want ...uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

func TestCreateApplyLoad(t *testing.T) {
	dir := t.TempDir()
	m, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(Edit{Added: []NewFile{{0, fm(5, "a", "c")}, {0, fm(6, "b", "d")}, {1, fm(3, "a", "z")}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(Edit{Deleted: []DeletedFile{{0, 5}}, LastTS: 900, LastSeq: 7, LogNum: 4, Cutoff: 100}); err != nil {
		t.Fatal(err)
	}
	m.Close()

	m2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	v := m2.Current()
	defer v.Unref()
	eqNums(t, "L0", nums(v.Levels[0]), 6)
	eqNums(t, "L1", nums(v.Levels[1]), 3)
	if f := v.Levels[1][0]; string(f.Smallest) != "a" || string(f.Largest) != "z" || f.Size != 300 {
		t.Fatalf("file meta not preserved: %+v", f)
	}
	s := m2.State()
	if s.LogNum != 4 || s.LastSeq != 7 || s.LastTS != 900 || s.Cutoff != 100 {
		t.Fatalf("state = %+v", s)
	}
}

func TestStateOnlyMovesUp(t *testing.T) {
	m, err := Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.Apply(Edit{LastTS: 50, Cutoff: 20, LastSeq: 9})
	m.Apply(Edit{LastTS: 10, Cutoff: 5, LastSeq: 3})
	if s := m.State(); s.LastTS != 50 || s.Cutoff != 20 || s.LastSeq != 9 {
		t.Fatalf("state went backwards: %+v", s)
	}
}

func TestLoadRewritesManifest(t *testing.T) {
	dir := t.TempDir()
	m, _ := Create(dir)
	m.Apply(Edit{Added: []NewFile{{0, fm(9, "a", "b")}}})
	m.Close()
	for i := 0; i < 3; i++ {
		m, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		m.Close()
	}
	entries, _ := os.ReadDir(dir)
	var manifests []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "MANIFEST-") {
			manifests = append(manifests, e.Name())
		}
	}
	cur, _ := os.ReadFile(filepath.Join(dir, "CURRENT"))
	if len(manifests) != 1 || strings.TrimSpace(string(cur)) != manifests[0] {
		t.Fatalf("manifests %v, CURRENT %q", manifests, cur)
	}
	m, _ = Load(dir)
	defer m.Close()
	v := m.Current()
	defer v.Unref()
	eqNums(t, "L0 after reloads", nums(v.Levels[0]), 9)
}

func TestTornManifestTail(t *testing.T) {
	dir := t.TempDir()
	m, _ := Create(dir)
	m.Apply(Edit{Added: []NewFile{{0, fm(5, "a", "b")}}})
	m.Apply(Edit{Added: []NewFile{{0, fm(6, "c", "d")}}, LastTS: 77})
	m.Close()
	cur, _ := os.ReadFile(filepath.Join(dir, "CURRENT"))
	path := filepath.Join(dir, strings.TrimSpace(string(cur)))
	st, _ := os.Stat(path)
	if err := os.Truncate(path, st.Size()-3); err != nil {
		t.Fatal(err)
	}
	m2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	v := m2.Current()
	defer v.Unref()
	eqNums(t, "L0", nums(v.Levels[0]), 5)
	if m2.State().LastTS != 0 {
		t.Fatal("torn edit was applied")
	}
}

func TestVersionRefcount(t *testing.T) {
	m, _ := Create(t.TempDir())
	defer m.Close()
	m.Apply(Edit{Added: []NewFile{{0, fm(6, "a", "b")}, {0, fm(7, "c", "d")}}})
	released := 0
	m.SetReleaseHook(func() { released++ })

	v := m.Current()
	m.Apply(Edit{Deleted: []DeletedFile{{0, 6}}})
	if _, ok := m.LiveFiles()[6]; !ok {
		t.Fatal("file 6 should stay live while an old version is referenced")
	}
	if released != 0 {
		t.Fatal("hook fired too early")
	}
	v.Unref()
	if released != 1 {
		t.Fatalf("hook fired %d times, want 1", released)
	}
	live := m.LiveFiles()
	if _, ok := live[6]; ok {
		t.Fatal("file 6 should be dead")
	}
	if _, ok := live[7]; !ok {
		t.Fatal("file 7 should be live")
	}
}

func TestLevelOrdering(t *testing.T) {
	m, _ := Create(t.TempDir())
	defer m.Close()
	m.Apply(Edit{Added: []NewFile{
		{1, fm(10, "m", "p")}, {1, fm(11, "a", "c")}, {1, fm(12, "x", "z")},
		{0, fm(3, "a", "z")}, {0, fm(8, "a", "z")}, {0, fm(5, "a", "z")},
	}})
	v := m.Current()
	defer v.Unref()
	eqNums(t, "L1", nums(v.Levels[1]), 11, 10, 12)
	eqNums(t, "L0", nums(v.Levels[0]), 8, 5, 3)
}

func TestNextFileNumNeverReused(t *testing.T) {
	dir := t.TempDir()
	m, _ := Create(dir)
	m.Close()
	os.WriteFile(filepath.Join(dir, "000050.sst"), nil, 0o644)
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if n := m.NewFileNum(); n <= 50 {
		t.Fatalf("NewFileNum = %d, want > 50", n)
	}
	a, b := m.NewFileNum(), m.NewFileNum()
	if b != a+1 {
		t.Fatalf("file numbers not sequential: %d %d", a, b)
	}
}

func TestLoadMissingCurrent(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("Load without CURRENT should fail")
	}
}
