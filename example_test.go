package chronokv_test

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/mnazarkhudoev/chronokv"
)

func ExampleDB_Get() {
	dir, _ := os.MkdirTemp("", "chronokv-example")
	defer os.RemoveAll(dir)

	db, err := chronokv.Open(dir, chronokv.Options{Retention: 24 * time.Hour})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	before, _ := db.Put([]byte("user:42"), []byte(`{"plan":"free"}`))
	db.Put([]byte("user:42"), []byte(`{"plan":"pro"}`))

	now, _ := db.Get([]byte("user:42"))
	then, _ := db.Get([]byte("user:42"), chronokv.AsOf(before))
	fmt.Println("now: ", string(now))
	fmt.Println("then:", string(then))

	history, _ := db.History([]byte("user:42"), 0)
	fmt.Println("versions:", len(history))
	// Output:
	// now:  {"plan":"pro"}
	// then: {"plan":"free"}
	// versions: 2
}

func ExampleDB_Scan() {
	dir, _ := os.MkdirTemp("", "chronokv-example")
	defer os.RemoveAll(dir)
	db, _ := chronokv.Open(dir, chronokv.Options{})
	defer db.Close()

	db.Put([]byte("fruit:apple"), []byte("3"))
	db.Put([]byte("fruit:banana"), []byte("5"))
	snapshot := db.Now()
	db.Delete([]byte("fruit:apple"))
	db.Put([]byte("fruit:cherry"), []byte("9"))

	it := db.Scan([]byte("fruit:"), []byte("fruit;"), chronokv.AsOf(snapshot))
	defer it.Close()
	for it.Next() {
		fmt.Printf("%s=%s\n", it.Key(), it.Value())
	}
	// Output:
	// fruit:apple=3
	// fruit:banana=5
}
