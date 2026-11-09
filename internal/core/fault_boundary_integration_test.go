package core_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core"
)

// Exercise the public API against a real private queue, including its error
// chain and the graceful dispatcher cancellation contract.
func TestPublicFaultBoundary(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".local", "state"), "ocq-fault-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	_, err = core.OpenReader(filepath.Join(root, "missing"))
	if !faults.IsCode(err, faults.CodeStorageFailed) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing reader: expected storage fault retaining not-exist cause, got %v", err)
	}
	q, err := core.OpenQueue(filepath.Join(root, "queue"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	_, err = q.Job("job-missing")
	if !faults.IsCode(err, faults.CodeNotFound) || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing job: expected not-found fault retaining no-rows cause, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Dispatch(ctx, 1, 0, true, nil); err != nil {
		t.Fatalf("canceled idle dispatch did not shut down gracefully: %v", err)
	}
}
