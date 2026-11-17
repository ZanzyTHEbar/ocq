package core

import (
	"context"
	"errors"
	"syscall"
	"time"
)

// The one native run's launcher and returned-terminal writer share this project
// journal. Contention is not a failed launch/receipt: wait only for an unacquired
// lock. Once acquired, all existing validators and one-shot writes run once.
func openClientAdmissionJournal(ctx context.Context, dir, project string) (*composeJournal, error) {
	j, err := openLocalJournal(dir, workJournal(project))
	if err != nil {
		return nil, err
	}
	events, err := watchObservationEvents(ctx, dir, workJournal(project))
	if err != nil {
		j.close()
		return nil, err
	}
	defer events.Close()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	if err = acquireJournalOnEvents(ctx, j, events, deadline.C, true, "native client journal lock unavailable; no admission or receipt write attempted"); err != nil {
		j.close()
		return nil, err
	}
	return j, nil
}

// Callers install the watcher before acquiring and own its lifetime and timer.
// Client admission checks cancellation before every acquire; dispatcher admission
// intentionally checks it only in the wait after contention.
func acquireJournalOnEvents(ctx context.Context, j *composeJournal, events *ObservationEvents, deadline <-chan time.Time, checkBeforeAcquire bool, timeoutMessage string) error {
	for {
		if checkBeforeAcquire {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		err := j.acquire()
		if err == nil {
			return nil
		}
		// acquire can own the outer lock while failing on the inner lock.
		// Release only this descriptor's locks; never steal/replace a lock.
		_ = syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN)
		_ = syscall.Flock(int(j.queueLock.Fd()), syscall.LOCK_UN)
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return errors.New(timeoutMessage)
		case err := <-events.Errors:
			return err
		case <-events.Changes:
		}
	}
}

// These writes are performed only after their caller's original pointer,
// context, ownership and one-attempt checks, while the project lock is owned.
func recordClientLaunchAdmission(j *composeJournal, digest string) error {
	if err := nativeJournalRoom(j.dir, 1); err != nil {
		return err
	}
	return j.create(clientFile(digest, "launch.attempt"), publicationMarker{Version: 1, Digest: digest})
}

func retainClientTerminalReceipt(j *composeJournal, digest string, receipt ClientTerminalReceipt) error {
	if err := nativeJournalRoom(j.dir, 1); err != nil {
		return err
	}
	return j.create(clientFile(digest, "terminal"), receipt)
}
