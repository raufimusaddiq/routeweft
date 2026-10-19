package sqlite

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// seedContentionTable creates the minimal durable table this suite hammers. It
// keeps the test independent of migration contents while still exercising the
// real Store pool, WAL mode and busy timeout (BDR-006, SPEC §28.4).
func seedContentionTable(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.DB().Exec(`CREATE TABLE contention(id INTEGER PRIMARY KEY, value INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO contention(id, value) VALUES(1, 0)`); err != nil {
		t.Fatal(err)
	}
}

// TestSQLiteContentionMixedReadersAndWriters drives concurrent writers and
// readers through the single Routeweft pool and proves none of them fail with a
// busy/locked error, every increment is applied exactly once, and the durable
// value matches the number of committed writes (SPEC §28.4 SQLite integration).
func TestSQLiteContentionMixedReadersAndWriters(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedContentionTable(t, store)

	const writers = 8
	const perWriter = 60
	const readers = 8
	var writeFailures, readFailures atomic.Int64
	stop := make(chan struct{})
	var writersWG, readersWG sync.WaitGroup

	for i := 0; i < writers; i++ {
		writersWG.Add(1)
		go func() {
			defer writersWG.Done()
			for j := 0; j < perWriter; j++ {
				if _, err := store.DB().ExecContext(ctx, `UPDATE contention SET value=value+1 WHERE id=1`); err != nil {
					t.Errorf("writer failed: %v", err)
					writeFailures.Add(1)
				}
			}
		}()
	}
	for i := 0; i < readers; i++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var value int
				if err := store.DB().QueryRowContext(ctx, `SELECT value FROM contention WHERE id=1`).Scan(&value); err != nil {
					readFailures.Add(1)
					t.Errorf("reader failed: %v", err)
				} else if value < 0 {
					t.Errorf("read negative counter %d", value)
				}
			}
		}()
	}
	writersWG.Wait()
	close(stop)
	readersWG.Wait()

	if writeFailures.Load() != 0 {
		t.Fatalf("%d writes failed under contention", writeFailures.Load())
	}
	if readFailures.Load() != 0 {
		t.Fatalf("%d reads failed under contention", readFailures.Load())
	}
	var final int
	if err := store.DB().QueryRowContext(ctx, `SELECT value FROM contention WHERE id=1`).Scan(&final); err != nil {
		t.Fatal(err)
	}
	if want := writers * perWriter; final != want {
		t.Fatalf("counter=%d, want %d (lost writes)", final, want)
	}
	if err := store.IntegrityCheck(ctx); err != nil {
		t.Fatalf("integrity after contention: %v", err)
	}
}

// TestSQLiteContentionMultiplePools proves a second Store opened on the same
// file can read and write concurrently with the first without busy errors,
// because WAL plus the configured busy timeout are the single-instance durable
// contract (BDR-006, SPEC §18).
func TestSQLiteContentionMultiplePools(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "routeweft.sqlite")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	seedContentionTable(t, first)
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	const rounds = 100
	var failures atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if _, err := first.DB().ExecContext(ctx, `UPDATE contention SET value=value+1 WHERE id=1`); err != nil {
				failures.Add(1)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			var value int
			if err := second.DB().QueryRowContext(ctx, `SELECT value FROM contention WHERE id=1`).Scan(&value); err != nil {
				failures.Add(1)
			}
		}
	}()
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d cross-connection operations failed", failures.Load())
	}

	var value int
	if err := first.DB().QueryRowContext(ctx, `SELECT value FROM contention WHERE id=1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != rounds {
		t.Fatalf("counter=%d, want %d", value, rounds)
	}
}
