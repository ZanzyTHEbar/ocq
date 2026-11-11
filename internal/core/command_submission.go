package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"ocq/internal/core/dbquery"
)

// This is a private form, not a v1 receipt. Decimal memory text avoids lossy
// JavaScript numbers. Existing command helpers own path resolution/v1 encoding.
type CommandSubmissionDraft struct {
	Key        string   `json:"key"`
	Executable string   `json:"executable"`
	Arguments  []string `json:"arguments"`
	Cwd        string   `json:"cwd"`
	Memory     string   `json:"memory_bytes"`
}

type CommandSubmissionScope struct {
	Machine string `json:"machine"`
	Space   string `json:"space_id"`
	Queue   string `json:"queue_identity"`
}

func (s CommandSubmissionScope) valid() bool {
	return s.Machine == "local" && isValidHexLen(s.Queue, 64) && strings.HasPrefix(s.Space, "sp") && len(s.Space) > 2 && len(s.Space) <= 128 && utf8.ValidString(s.Space) && !strings.ContainsAny(s.Space, "/\\ \t\r\n\x00")
}

func (r *Reader) CommandSubmissionContext(space string) (CommandSubmissionScope, error) {
	id, e := notificationQueueIdentity(r.store)
	s := CommandSubmissionScope{Machine: "local", Space: space, Queue: id}
	if e != nil || !s.valid() {
		return CommandSubmissionScope{}, errors.New("exact local queue/space required")
	}
	return s, nil
}

type preparedCommandSubmission struct {
	Scope     CommandSubmissionScope `json:"scope"`
	Key       string                 `json:"key"`
	Canonical string                 `json:"canonical"`
}

func validCommandKey(key string) bool { return key != "" && len(key) <= 1024 && utf8.ValidString(key) }

// BuildCommandSubmissionDraft resolves once, BEFORE review. Like CLI intake,
// absolute/relative explicit executable paths are not executable-file proof.
func BuildCommandSubmissionDraft(scope CommandSubmissionScope, d CommandSubmissionDraft) ([]byte, error) {
	m, e := strconv.ParseInt(d.Memory, 10, 64)
	if e != nil || m < 0 || strconv.FormatInt(m, 10) != d.Memory || !scope.valid() || !validCommandKey(d.Key) || d.Executable == "" || d.Cwd == "" || d.Arguments == nil {
		return nil, errors.New("explicit key, executable, argument array, cwd and decimal memory required")
	}
	r, e := newCommandRequest(append([]string{d.Executable}, d.Arguments...), d.Cwd, m)
	if e != nil {
		return nil, e
	}
	b, _, e := encodeCommandRequest(r)
	if e != nil {
		return nil, e
	}
	raw, e := json.Marshal(preparedCommandSubmission{scope, d.Key, string(b)})
	if e != nil || len(raw) > composeLimit {
		return nil, errors.New("command draft exceeds private frame")
	}
	return raw, nil
}

// Validate the already-resolved v1 representation without consulting today's
// filesystem/PATH. Canonical bytes and v1 field order remain the CLI contract.
func preparedCommandBytes(raw []byte) (commandRequest, error) {
	var r commandRequest
	if _, e := decodeStrictJSONObject(raw, composeLimit, &r); e != nil {
		return r, e
	}
	if r.Version != 1 || r.MemoryBytes < 0 || len(r.Argv) == 0 || !filepath.IsAbs(r.Cwd) || filepath.Clean(r.Cwd) != r.Cwd || !filepath.IsAbs(r.Argv[0]) || filepath.Clean(r.Argv[0]) != r.Argv[0] {
		return r, errors.New("resolved v1 command required")
	}
	for _, v := range append([]string{r.Cwd}, r.Argv...) {
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return r, errors.New("lossless UTF-8 command required")
		}
	}
	b, _, e := encodeCommandRequest(r)
	if e != nil || !bytes.Equal(b, raw) {
		return r, errors.New("exact v1 canonical encoding required")
	}
	return r, nil
}

func parsePreparedCommandSubmission(raw []byte) (preparedCommandSubmission, error) {
	var p preparedCommandSubmission
	if _, e := decodeStrictJSONObject(raw, composeLimit, &p); e != nil {
		return p, e
	}
	if !p.Scope.valid() || !validCommandKey(p.Key) {
		return p, errors.New("invalid command submission context/key")
	}
	_, e := preparedCommandBytes([]byte(p.Canonical))
	return p, e
}

// CommandSubmitter is a non-initializing schema-5 admission boundary. It never
// dispatches. Receipt recovery and new admission both use the reviewed bytes,
// NOT SubmitCommand/newCommandRequest and NOT today's symlink/PATH resolution.
type CommandSubmitter struct{ store *store }

func OpenExistingCommandSubmitter(dir string) (*CommandSubmitter, error) {
	s, e := openExistingWritableStore(dir)
	if e != nil {
		return nil, e
	}
	s.existingSchemaOnly = true
	return &CommandSubmitter{s}, nil
}
func (w *CommandSubmitter) Close() error { return w.store.database.Close() }
func (w *CommandSubmitter) SubmitPreparedCommand(key string, canonical []byte) (Job, error) {
	canonical = append([]byte(nil), canonical...)
	if !validCommandKey(key) {
		return Job{}, errors.New("invalid command key")
	}
	if _, e := preparedCommandBytes(canonical); e != nil {
		return Job{}, e
	}
	return w.store.acceptCanonical(key, canonical, false)
}

// Exact immutable receipt lookup. No path resolution or job mutation, including
// after command execution or replacement/removal of the originally resolved path.
func (r *Reader) CommandSubmissionReceipt(key string, canonical []byte) (QueueJobOutput, error) {
	if !validCommandKey(key) {
		return QueueJobOutput{}, errors.New("invalid command key")
	}
	if _, e := preparedCommandBytes(canonical); e != nil {
		return QueueJobOutput{}, e
	}
	row, e := dbquery.New(r.store.db).ReceiptByKey(context.Background(), key)
	if errors.Is(e, sql.ErrNoRows) {
		return QueueJobOutput{}, e
	}
	if e != nil {
		return QueueJobOutput{}, e
	}
	if !bytes.Equal(row.Canonical, canonical) {
		return QueueJobOutput{}, errors.New("key belongs to different canonical bytes")
	}
	return r.QueueJob(row.JobID)
}
