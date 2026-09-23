package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/electrokomplekt/replenishment/internal/planning"
)

func TestPersistenceAndIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := planning.EmptyDataset()
	d.Suppliers = append(d.Suppliers, planning.Supplier{ID: "s", Name: "Supplier"})
	saved, err := s.Replace(context.Background(), d, 0)
	if err != nil {
		t.Fatal(err)
	}
	d.Suppliers[0].Name = "input mutation"
	saved.Data.Suppliers[0].Name = "output mutation"
	snapshot := s.Read()
	snapshot.Data.Suppliers[0].Name = "read mutation"
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []*Store{s, reopened} {
		if got := current.Read(); got.Revision != 1 || got.Data.Suppliers[0].Name != "Supplier" {
			t.Fatalf("snapshot corrupted: %+v", got)
		}
	}
	if _, err := s.Replace(context.Background(), planning.EmptyDataset(), 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestConcurrentRevision(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Replace(context.Background(), planning.EmptyDataset(), 0)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("unexpected error: %v", err)
			}
			_ = s.Read()
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("committed %d concurrent writes", successes.Load())
	}
}

func TestFailedWriteDoesNotChangeMemory(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(dir, "missing", "data.json")
	if _, err := s.Replace(context.Background(), planning.EmptyDataset(), 0); err == nil {
		t.Fatal("expected write failure")
	}
	if s.Read().Revision != 0 {
		t.Fatal("failed write changed revision")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Replace(ctx, planning.EmptyDataset(), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestCorruptFileRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("corruption silently accepted")
	}
}
