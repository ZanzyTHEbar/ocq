package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"

	"github.com/ZanzyTHEbar/faults-go"
)

// No path is accepted from a caller. The exact retained job and selected stream
// resolve through the same query/private-file opener as Reader.QueueLog.
type ObservationLogCursor struct {
	JobID        string `json:"job_id"`
	Stream       string `json:"stream"`
	Offset       string `json:"offset"`
	FileIdentity string `json:"file_identity"`
	ObservedSize string `json:"observed_size"`
	ModifiedNS   string `json:"modified_ns"`
}

type ObservationLogOutput struct {
	Status                string               `json:"status"`
	ResetReason           string               `json:"reset_reason,omitempty"`
	ObservedAt            string               `json:"observed_at"`
	NonAtomicWithDatabase bool                 `json:"non_atomic_with_database"`
	Cursor                ObservationLogCursor `json:"cursor"`
	Bytes                 []byte               `json:"bytes_base64,omitempty"`
	NextOffset            string               `json:"next_offset"`
	Offset                string               `json:"offset"`
	EOF                   bool                 `json:"eof"`
}

func (c ObservationLogCursor) validate() error {
	if !isValidJobID(c.JobID) || (c.Stream != "stdout" && c.Stream != "stderr") {
		return faults.New(faults.CodeValidationFailed, "log requires exact job and stdout|stderr")
	}
	for _, s := range []string{c.Offset, c.ObservedSize, c.ModifiedNS} {
		if _, err := observationDecimal(s); err != nil {
			return err
		}
	}
	if c.FileIdentity != "" && !isValidHexLen(c.FileIdentity, 64) {
		return faults.New(faults.CodeValidationFailed, "invalid log identity")
	}
	if c.FileIdentity == "" && (c.Offset != "0" || c.ObservedSize != "0" || c.ModifiedNS != "0") {
		return faults.New(faults.CodeValidationFailed, "initial/missing log cursor must be zero")
	}
	return nil
}

func observationLogIdentity(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return ""
	}
	return sha256Hex([]byte(fmt.Sprintf("%d:%d", st.Dev, st.Ino)))
}

// This function owns and closes its file. It never runs inside a read transaction.
// Metadata-only checks do not read bytes and may be used while watching.
func observeLog(ctx context.Context, s *store, c ObservationLogCursor, readBytes bool) ObservationLogOutput {
	out := ObservationLogOutput{Status: "unavailable", ObservedAt: now(), NonAtomicWithDatabase: true, Cursor: c, NextOffset: c.Offset, Offset: c.Offset}
	f, err := openQueueLog(ctx, s.db, c.JobID, c.Stream == "stderr")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || faults.IsCode(err, faults.CodeNotFound) {
			out.Status = "not_found"
		}
		if c.FileIdentity != "" && out.Status == "not_found" {
			out.Status = "reset"
			out.ResetReason = "missing"
		}
		return out
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out
	}
	identity := observationLogIdentity(info)
	if identity == "" {
		return out
	}
	out.Cursor.FileIdentity = identity
	out.Cursor.ObservedSize = strconv.FormatInt(info.Size(), 10)
	out.Cursor.ModifiedNS = strconv.FormatInt(info.ModTime().UnixNano(), 10)
	if c.FileIdentity != "" && identity != c.FileIdentity {
		out.Status = "reset"
		out.ResetReason = "replaced"
		return out
	}
	offset, _ := observationDecimal(c.Offset)
	size, _ := observationDecimal(c.ObservedSize)
	if info.Size() < size || info.Size() < offset {
		out.Status = "reset"
		out.ResetReason = "truncated"
		return out
	}
	if c.FileIdentity != "" && info.Size() == size && out.Cursor.ModifiedNS != c.ModifiedNS {
		out.Status = "reset"
		out.ResetReason = "rewritten"
		return out
	}
	if readBytes {
		page, err := readQueueLog(f, c.JobID, c.Stream, offset, out.ObservedAt)
		if err != nil {
			return out
		}
		out.Bytes, out.NextOffset, out.EOF = page.Bytes, page.NextOffset, page.EOF
		out.Cursor.Offset = page.NextOffset
		out.Cursor.ObservedSize = page.ObservedSize
	}
	after, err := f.Stat()
	if err != nil {
		return out
	}
	if after.Size() < info.Size() {
		out.Status = "reset"
		out.ResetReason = "truncated_during_read"
		out.Bytes = nil
		out.NextOffset = c.Offset
		return out
	}
	if after.Size() == info.Size() && !after.ModTime().Equal(info.ModTime()) {
		out.Status = "reset"
		out.ResetReason = "rewritten_during_read"
		out.Bytes = nil
		out.NextOffset = c.Offset
		return out
	}
	if strconv.FormatInt(after.Size(), 10) == out.Cursor.ObservedSize {
		out.Cursor.ModifiedNS = strconv.FormatInt(after.ModTime().UnixNano(), 10)
	}
	// Reopen through the same secure retained-path boundary to catch rename /
	// replacement during the read; no raw caller path or filesystem adoption.
	current, err := openQueueLog(ctx, s.db, c.JobID, c.Stream == "stderr")
	if err != nil {
		out.Status = "reset"
		out.ResetReason = "unavailable_during_read"
		out.Bytes = nil
		return out
	}
	defer current.Close()
	currentInfo, err := current.Stat()
	if err != nil || !os.SameFile(info, currentInfo) {
		out.Status = "reset"
		out.ResetReason = "replaced_during_read"
		out.Bytes = nil
		out.NextOffset = c.Offset
		return out
	}
	out.Status = "ready"
	return out
}

func observeLogAt(ctx context.Context, dir string, c ObservationLogCursor) ObservationLogOutput {
	s, err := openReadOnlyStoreContext(ctx, dir)
	if err != nil {
		return ObservationLogOutput{Status: "unavailable", ObservedAt: now(), NonAtomicWithDatabase: true, Cursor: c, NextOffset: c.Offset}
	}
	defer s.database.Close()
	return observeLog(ctx, s, c, false)
}
