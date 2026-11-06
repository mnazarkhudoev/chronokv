//go:build unix

package chronokv

import (
	"bufio"
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const crashChildEnv = "CHRONOKV_CRASH_CHILD"

func crashKey(iter, i int) string   { return fmt.Sprintf("it%03d-k%08d", iter, i) }
func crashBatch(iter, j int) string { return fmt.Sprintf("it%03d-b%08d", iter, j) }

// crashValue is deterministic so the parent can check what it reads back.
func crashValue(i int) []byte {
	return bytes.Repeat([]byte{byte('a' + i%26)}, (i*37)%2000)
}

// TestCrashRecovery kills a writer process with SIGKILL at a random moment,
// reopens the database, and checks that:
//   - every write the child saw acknowledged is there, with the right value,
//     and readable at its own timestamp;
//   - what's there is a prefix of the child's writes (nothing out of order,
//     at most the one write that was in flight when it died);
//   - batches are all-or-nothing.
//
// The same directory is reused across iterations, so later iterations also
// recover from whatever state earlier crashes left behind.
func TestCrashRecovery(t *testing.T) {
	if dir := os.Getenv(crashChildEnv); dir != "" {
		iter, _ := strconv.Atoi(os.Getenv(crashChildEnv + "_ITER"))
		crashChild(dir, iter)
		os.Exit(0)
	}

	iterations := 20
	switch {
	case *flagLong:
		iterations = 200
	case testing.Short():
		iterations = 5
	}
	dir := t.TempDir()
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 1))
	acked := map[int]int{} // iteration -> last acked key index

	for iter := 0; iter < iterations; iter++ {
		last, batches, writes := runCrashChild(t, dir, iter, time.Duration(50+rng.IntN(350))*time.Millisecond)
		acked[iter] = last

		db, err := Open(dir, Options{})
		if err != nil {
			t.Fatalf("iteration %d: reopen after crash: %v", iter, err)
		}
		checkCrashIteration(t, db, iter, last, batches)
		for k := 0; k < len(writes); k += 50 {
			w := writes[k]
			expectGet(t, db, crashKey(iter, w.i), sp(string(crashValue(w.i))), AsOf(w.ts))
		}
		// Earlier iterations' acknowledged data must survive later crashes.
		if iter > 0 {
			prev := rng.IntN(iter)
			for i := 0; i <= acked[prev]; i += 97 {
				expectGet(t, db, crashKey(prev, i), sp(string(crashValue(i))))
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

type ackedWrite struct {
	i  int
	ts Timestamp
}

// runCrashChild starts the writer, kills it after d, and returns the last
// acknowledged key index (-1 if none) plus the acknowledged batch numbers.
func runCrashChild(t *testing.T, dir string, iter int, d time.Duration) (int, map[int]bool, []ackedWrite) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashRecovery$")
	cmd.Env = append(os.Environ(), crashChildEnv+"="+dir, fmt.Sprintf("%s_ITER=%d", crashChildEnv, iter))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	lines := make(chan []string, 1)
	go func() {
		var got []string
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			got = append(got, sc.Text())
		}
		lines <- got
	}()
	time.Sleep(d)
	cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()
	got := <-lines

	last := -1
	batches := map[int]bool{}
	var writes []ackedWrite
	for _, line := range got {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue // a line cut off by the kill
		}
		n, err1 := strconv.Atoi(f[1])
		ts, err2 := strconv.ParseUint(f[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		switch f[0] {
		case "ack":
			writes = append(writes, ackedWrite{n, Timestamp(ts)})
			last = n
		case "batch":
			batches[n] = true
		}
	}
	if len(writes) == 0 && stderr.Len() > 0 {
		t.Logf("child produced no acks; stderr:\n%s", stderr.String())
	}
	t.Logf("iteration %d: killed after %v, %d acked writes", iter, d, len(writes))
	return last, batches, writes
}

func checkCrashIteration(t *testing.T, db *DB, iter, last int, batches map[int]bool) {
	t.Helper()
	// Which key indexes survived?
	it := db.Scan([]byte(fmt.Sprintf("it%03d-k", iter)), []byte(fmt.Sprintf("it%03d-l", iter)))
	n := -1
	for it.Next() {
		i, err := strconv.Atoi(string(it.Key()[len("it000-k"):]))
		if err != nil {
			t.Fatalf("odd key %q", it.Key())
		}
		if i != n+1 {
			t.Fatalf("iteration %d: keys are not a prefix: %d follows %d", iter, i, n)
		}
		if !bytes.Equal(it.Value(), crashValue(i)) {
			t.Fatalf("iteration %d: key %d has a wrong value", iter, i)
		}
		n = i
	}
	if err := it.Err(); err != nil {
		t.Fatal(err)
	}
	it.Close()
	if n < last || n > last+1 {
		t.Fatalf("iteration %d: recovered keys 0..%d, but %d were acknowledged", iter, n, last+1)
	}

	// Batches: acknowledged ones fully present, others all-or-nothing.
	for j := 0; j*200 <= n+200; j++ {
		_, errX := db.Get([]byte(crashBatch(iter, j) + "-x"))
		_, errY := db.Get([]byte(crashBatch(iter, j) + "-y"))
		if (errX == nil) != (errY == nil) {
			t.Fatalf("iteration %d: batch %d is half there (x: %v, y: %v)", iter, j, errX, errY)
		}
		if batches[j] && errX != nil {
			t.Fatalf("iteration %d: acknowledged batch %d is missing", iter, j)
		}
	}
}

// crashChild is the process that gets killed. It writes as fast as it can
// and reports each acknowledged write on stdout.
func crashChild(dir string, iter int) {
	db, err := Open(dir, Options{SyncMode: SyncAlways, MemtableSize: 64 << 10})
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	w := bufio.NewWriter(os.Stdout)
	for i := 0; i < 1_000_000; i++ {
		ts, err := db.Put([]byte(crashKey(iter, i)), crashValue(i))
		if err != nil {
			fmt.Fprintln(os.Stderr, "put:", err)
			os.Exit(1)
		}
		fmt.Fprintf(w, "ack %d %d\n", i, ts)
		if i%200 == 0 {
			b := db.NewBatch()
			b.Put([]byte(crashBatch(iter, i/200)+"-x"), []byte("x"))
			b.Put([]byte(crashBatch(iter, i/200)+"-y"), []byte("y"))
			ts, err := db.Commit(b)
			if err != nil {
				fmt.Fprintln(os.Stderr, "batch:", err)
				os.Exit(1)
			}
			fmt.Fprintf(w, "batch %d %d\n", i/200, ts)
		}
		w.Flush()
	}
}
