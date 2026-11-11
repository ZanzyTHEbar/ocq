package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"ocq/internal/core"
)

// Extends the real CLI/process lifecycle with the same non-initializing public
// boundaries used by the panel. The caller selects the actual volume via
// XDG_STATE_HOME; no filesystem labels, mocks or synthetic syscall failures.
func (s *scenario) runStorage() {
	missing := filepath.Join(s.root, "missing-store")
	reader, err := core.OpenReader(missing)
	if err == nil {
		reader.Close()
		panic("Reader initialized missing state")
	}
	writer, err := core.OpenExistingMailSender(missing)
	if err == nil {
		writer.Close()
		panic("existing writer initialized missing state")
	}
	_, err = os.Lstat(missing)
	check(errors.Is(err, os.ErrNotExist), "non-initializing boundary created state")

	reader, err = core.OpenReader(s.state)
	must(err)
	defer reader.Close()
	page, err := reader.QueueJobs(0, 100)
	must(err)
	check(len(page.Items) > 0, "Reader lost reopened lifecycle receipts")
	writer, err = core.OpenExistingMailSender(s.state)
	must(err)
	must(writer.Close())
	s.note("PASS existing read-only and writable public opens on selected volume; missing-store reads/writes did not initialize")

	// A real unusable lock path must be refused, not bypassed or repaired.
	blocked := filepath.Join(s.root, "blocked-store")
	must(os.Mkdir(blocked, 0700))
	must(os.Mkdir(filepath.Join(blocked, "schema.lock"), 0700))
	queue, err := core.OpenQueue(blocked)
	if err == nil {
		queue.Close()
		panic("unusable schema lock was bypassed")
	}
	s.note(fmt.Sprintf("observed lock-path refusal: %v", err))
	_, err = os.Lstat(filepath.Join(blocked, "queue.db"))
	check(errors.Is(err, os.ErrNotExist), "database created after lock-open failure")
	s.note("PASS actual schema.lock file-open failure refused before database creation; no capability/durability certification inferred")
}
