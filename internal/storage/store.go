package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/electrokomplekt/replenishment/internal/planning"
)

var ErrConflict = errors.New("dataset revision has changed")

type Snapshot struct {
	Revision  uint64           `json:"revision"`
	UpdatedAt time.Time        `json:"updated_at"`
	Data      planning.Dataset `json:"data"`
}

// Store owns one on-disk snapshot. Run exactly one process per data file.
// All returned snapshots are copies and writes use an atomic rename.
type Store struct {
	mu       sync.RWMutex
	path     string
	snapshot Snapshot
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("data file path must not be empty")
	}
	s := &Store{path: path, snapshot: Snapshot{Data: planning.EmptyDataset()}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}
	if err := json.Unmarshal(b, &s.snapshot); err != nil {
		return nil, fmt.Errorf("decode data: %w", err)
	}
	if err := s.snapshot.Data.Validate(); err != nil {
		return nil, fmt.Errorf("validate stored data: %w", err)
	}
	return s, nil
}

func clone(d planning.Dataset) planning.Dataset {
	d.Suppliers = append([]planning.Supplier{}, d.Suppliers...)
	for i := range d.Suppliers {
		d.Suppliers[i].Seasonality = append([]float64(nil), d.Suppliers[i].Seasonality...)
	}
	d.Products = append([]planning.Product{}, d.Products...)
	for i := range d.Products {
		d.Products[i].ReviewReasons = append([]string(nil), d.Products[i].ReviewReasons...)
	}
	d.Source.Warnings = append([]string(nil), d.Source.Warnings...)
	d.Sales = append([]planning.Sale{}, d.Sales...)
	d.Stock = append([]planning.Stock{}, d.Stock...)
	d.Shipments = append([]planning.Shipment{}, d.Shipments...)
	return d
}

func (s *Store) Read() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := s.snapshot
	snapshot.Data = clone(snapshot.Data)
	return snapshot
}

func (s *Store) Replace(ctx context.Context, d planning.Dataset, expected uint64) (Snapshot, error) {
	if err := d.Validate(); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != s.snapshot.Revision {
		return Snapshot{}, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	next := Snapshot{Revision: expected + 1, UpdatedAt: time.Now().UTC(), Data: clone(d)}
	encoded, err := json.Marshal(next)
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode data: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".snapshot-*")
	if err != nil {
		return Snapshot{}, fmt.Errorf("create temporary data file: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(encoded); err != nil {
		_ = f.Close()
		return Snapshot{}, fmt.Errorf("write data: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return Snapshot{}, fmt.Errorf("sync data: %w", err)
	}
	if err := f.Close(); err != nil {
		return Snapshot{}, fmt.Errorf("close data: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := os.Rename(f.Name(), s.path); err != nil {
		return Snapshot{}, fmt.Errorf("commit data: %w", err)
	}
	s.snapshot = next
	next.Data = clone(next.Data)
	return next, nil
}
