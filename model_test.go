package chronokv

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/mnazarkhudoev/chronokv/internal/compaction"
)

var (
	flagLong = flag.Bool("long", false, "run long randomized tests")
	flagSeed = flag.Uint64("seed", 0, "replay the model test with this seed")
)

// TestModel runs random operations against the DB and the oracle side by
// side, and after every step asks both the same questions at random points
// in time. Flushes, compactions, reopens and clock jumps (including
// backwards) are mixed in, so every layer of the engine gets exercised.
//
// A failure prints the seed; rerun with -args -seed=N to reproduce it.
func TestModel(t *testing.T) {
	steps := 5000
	switch {
	case *flagLong:
		steps = 100000
	case testing.Short():
		steps = 1500
	}
	seeds := []uint64{*flagSeed}
	if *flagSeed == 0 {
		base := uint64(time.Now().UnixNano())
		seeds = []uint64{base, base + 1, base + 2}
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprint("seed=", seed), func(t *testing.T) {
			runModel(t, seed, steps)
		})
	}
}

var modelKeys = func() []string {
	ks := []string{"", "\x00", "a", "a\x00", "a\x00\x01", "ab", "\xff", "\xff\xff"}
	for i := 0; len(ks) < 40; i++ {
		ks = append(ks, fmt.Sprintf("k%02d", i))
	}
	return ks
}()

type modelRun struct {
	t     *testing.T
	seed  uint64
	step  int
	op    string
	rng   *rand.Rand
	dir   string
	opts  Options
	clock *fakeClock
	db    *DB
	o     *oracle
}

func (m *modelRun) fatalf(format string, args ...any) {
	m.t.Helper()
	m.t.Fatalf("seed=%d step=%d op=%s: %s", m.seed, m.step, m.op, fmt.Sprintf(format, args...))
}

func (m *modelRun) key() string { return modelKeys[m.rng.IntN(len(modelKeys))] }

func (m *modelRun) value() []byte {
	n := m.rng.IntN(65)
	if m.rng.IntN(50) == 0 {
		n = 10 << 10
	}
	v := make([]byte, n)
	for i := range v {
		v[i] = byte('a' + m.rng.IntN(26))
	}
	return v
}

func runModel(t *testing.T, seed uint64, steps int) {
	m := &modelRun{
		t: t, seed: seed,
		rng:   rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		dir:   t.TempDir(),
		clock: newClock(time.Unix(0, 1_000_000)),
		o:     newOracle(),
	}
	m.opts = Options{
		Retention:    500,
		MemtableSize: 8 << 10,
		BlockSize:    512,
		SyncMode:     SyncInterval,
		compaction:   compaction.Options{L1MaxBytes: 32 << 10, LevelMultiplier: 4, TargetFileSize: 8 << 10},
	}
	m.opts.Clock = m.clock.Now
	m.open()
	defer func() { m.db.Close() }()

	type op struct {
		name   string
		weight int
		fn     func()
	}
	ops := []op{
		{"put", 40, m.doPut},
		{"delete", 10, m.doDelete},
		{"batch", 15, m.doBatch},
		{"advance", 15, func() { m.clock.Advance(time.Duration(1 + m.rng.IntN(50))) }},
		{"rewind", 3, func() { m.clock.Advance(-time.Duration(1 + m.rng.IntN(100))) }},
		{"flush", 5, func() { m.must(m.db.Flush()) }},
		{"compact", 4, func() { m.must(m.db.Compact()) }},
		{"reopen", 3, func() { m.must(m.db.Close()); m.open() }},
		{"check", 5, m.checkRandom},
	}
	total := 0
	for _, o := range ops {
		total += o.weight
	}
	for m.step = 0; m.step < steps; m.step++ {
		r := m.rng.IntN(total)
		for _, o := range ops {
			if r < o.weight {
				m.op = o.name
				o.fn()
				break
			}
			r -= o.weight
		}
		m.op = "light-check"
		m.checkLatest(m.key())
	}
	m.op = "final"
	m.checkAll()
}

func (m *modelRun) must(err error) {
	m.t.Helper()
	if err != nil {
		m.fatalf("%v", err)
	}
}

func (m *modelRun) open() {
	db, err := Open(m.dir, m.opts)
	if err != nil {
		m.fatalf("open: %v", err)
	}
	m.db = db
}

func (m *modelRun) visible() uint64 { return m.db.visibleTS.Load() }

func (m *modelRun) doPut() {
	k, v := m.key(), m.value()
	ts, err := m.db.Put([]byte(k), v)
	m.must(err)
	m.o.put(k, v, ts)
}

func (m *modelRun) doDelete() {
	k := m.key()
	ts, err := m.db.Delete([]byte(k))
	m.must(err)
	m.o.del(k, ts)
}

// doBatch deliberately repeats keys inside one batch: the last write to a
// key in a batch must win.
func (m *modelRun) doBatch() {
	b := m.db.NewBatch()
	type w struct {
		k   string
		v   []byte
		del bool
	}
	var ws []w
	for i := 2 + m.rng.IntN(5); i > 0; i-- {
		k := m.key()
		if m.rng.IntN(4) == 0 {
			b.Delete([]byte(k))
			ws = append(ws, w{k: k, del: true})
		} else {
			v := m.value()
			b.Put([]byte(k), v)
			ws = append(ws, w{k: k, v: v})
		}
	}
	ts, err := m.db.Commit(b)
	m.must(err)
	last := map[string]w{}
	for _, x := range ws {
		last[x.k] = x
	}
	for k, x := range last {
		if x.del {
			m.o.del(k, ts)
		} else {
			m.o.put(k, x.v, ts)
		}
	}
}

// randomTS picks a timestamp a reader is allowed to ask about: anywhere
// from the cutoff to a little past the latest commit.
func (m *modelRun) randomTS() uint64 {
	lo, hi := m.db.cutoff(), m.visible()+5
	if hi <= lo {
		return lo
	}
	return lo + m.rng.Uint64N(hi-lo+1)
}

// checkLatest reads k without AsOf.
func (m *modelRun) checkLatest(k string) {
	m.t.Helper()
	got, err := m.db.Get([]byte(k))
	m.compareGet(k, "latest", m.o.get(k, m.visible()), got, err)
}

func (m *modelRun) checkGet(k string, ts uint64) {
	m.t.Helper()
	got, err := m.db.Get([]byte(k), AsOf(Timestamp(ts)))
	m.compareGet(k, fmt.Sprint(ts), m.o.get(k, min(ts, m.visible())), got, err)
}

func (m *modelRun) compareGet(k, at string, want, got []byte, err error) {
	m.t.Helper()
	switch {
	case want == nil && !errors.Is(err, ErrNotFound):
		m.fatalf("Get(%q @%s) = %q, %v; want not found", k, at, got, err)
	case want != nil && (err != nil || !bytes.Equal(got, want)):
		m.fatalf("Get(%q @%s) = %q, %v; want %q", k, at, got, err, want)
	}
}

func (m *modelRun) checkScan(start, end []byte, ts uint64) {
	m.t.Helper()
	it := m.db.Scan(start, end, AsOf(Timestamp(ts)))
	defer it.Close()
	var got []kv
	for it.Next() {
		got = append(got, kv{string(it.Key()), string(it.Value())})
	}
	if err := it.Err(); err != nil {
		m.fatalf("Scan(%q, %q @%d): %v", start, end, ts, err)
	}
	want := m.o.scan(start, end, min(ts, m.visible()))
	if fmt.Sprint(got) != fmt.Sprint(want) {
		m.fatalf("Scan(%q, %q @%d)\n got  %q\n want %q", start, end, ts, got, want)
	}
}

func (m *modelRun) checkHistory(k string) {
	m.t.Helper()
	cutoff, visible := m.db.cutoff(), m.visible()
	got, err := m.db.History([]byte(k), 0)
	if err != nil {
		m.fatalf("History(%q): %v", k, err)
	}
	if want := m.o.history(k, cutoff, visible); !sameVersions(got, want) {
		m.fatalf("History(%q) cutoff=%d\n got  %v\n want %v", k, cutoff, fmtHistory(got), fmtHistory(want))
	}
}

func (m *modelRun) checkRandom() {
	for i := 0; i < 5; i++ {
		m.checkGet(m.key(), m.randomTS())
	}
	if c := m.db.cutoff(); c > 0 {
		if _, err := m.db.Get([]byte(m.key()), AsOf(Timestamp(c-1))); !errors.Is(err, ErrHistoryTruncated) {
			m.fatalf("Get below cutoff %d: err = %v, want ErrHistoryTruncated", c, err)
		}
	}
	var start, end []byte
	if m.rng.IntN(2) == 0 {
		start = []byte(m.key())
	}
	if m.rng.IntN(2) == 0 {
		end = []byte(m.key())
	}
	m.checkScan(start, end, m.randomTS())
	m.checkHistory(m.key())
}

func (m *modelRun) checkAll() {
	for _, k := range modelKeys {
		m.checkLatest(k)
		m.checkGet(k, m.db.cutoff())
		m.checkHistory(k)
	}
	m.checkScan(nil, nil, max(m.visible(), m.db.cutoff()))
	m.checkScan(nil, nil, m.db.cutoff())
}
