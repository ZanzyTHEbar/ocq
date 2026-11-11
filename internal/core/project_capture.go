package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

type worktreeSnapshot struct {
	statusRaw      string
	statusDigest   string
	stagedStatus   string
	unstagedStatus string
	untrackedPaths []string
	changedFiles   []ManifestChangedFile
	contentDigest  string
	diff           string
}

func gitExecRaw(dir string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-c", "core.quotepath=false"}, args...)
	c := exec.Command("git", fullArgs...)
	c.Dir = dir
	b, err := c.Output()
	if err != nil {
		return nil, diagnosticWrap(faults.CodeRuntimeFailed, fmt.Sprintf("git %v failed", args), err)
	}
	return b, nil
}

// Disabling diff's index refresh preserves the index, but --exit-code is also
// needed to filter stat-only changes from raw/name-status output. Exit 1 means
// actual differences, not a failed inspection. Capture keeps gitExecRaw as-is.
func gitVerifyDiff(dir string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"--no-optional-locks", "-c", "core.quotepath=false", "-c", "diff.autoRefreshIndex=false", "diff", "--exit-code"}, args...)
	c := exec.Command("git", fullArgs...)
	c.Dir = dir
	b, err := c.Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, diagnosticWrap(faults.CodeRuntimeFailed, fmt.Sprintf("git diff %v failed", args), err)
	}
	return b, nil
}

func computeChangedFilesDigest(files []ManifestChangedFile) string {
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write([]byte(f.OldMode))
		h.Write([]byte{0})
		h.Write([]byte(f.NewMode))
		h.Write([]byte{0})
		if f.Contents != nil {
			h.Write([]byte{1})
			h.Write([]byte(*f.Contents))
		} else {
			h.Write([]byte{0})
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func formatUntrackedUnifiedDiff(f ManifestChangedFile) string {
	if f.Contents == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", f.Path, f.Path)
	fmt.Fprintf(&b, "new file mode %s\n", f.NewMode)
	fmt.Fprintf(&b, "--- /dev/null\n")
	fmt.Fprintf(&b, "+++ b/%s\n", f.Path)
	content := *f.Contents
	if content == "" {
		return b.String()
	}
	hasTrailingNL := strings.HasSuffix(content, "\n")
	trimmed := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) == 1 {
		fmt.Fprintf(&b, "@@ -0,0 +1 @@\n")
	} else {
		fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	}
	for _, line := range lines {
		fmt.Fprintf(&b, "+%s\n", line)
	}
	if !hasTrailingNL {
		b.WriteString("\\ No newline at end of file\n")
	}
	return b.String()
}

func verifyCanonicalWorktreeCheckout(worktree, base string) error {
	if worktree == "" || !filepath.IsAbs(worktree) || filepath.Clean(worktree) != worktree {
		return diagnostic(faults.CodeValidationFailed, fmt.Sprintf("worktree %q must be a clean absolute path", worktree))
	}
	info, err := os.Stat(worktree)
	if err != nil || !info.IsDir() {
		return diagnostic(faults.CodeValidationFailed, fmt.Sprintf("worktree %q is not an existing directory", worktree))
	}
	abs, err := filepath.Abs(worktree)
	if err != nil {
		return classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	eval, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	if eval != worktree {
		return diagnostic(faults.CodeValidationFailed, fmt.Sprintf("worktree %q is not canonical (%q)", worktree, eval))
	}
	top, err := gitRead(worktree, "rev-parse", "--show-toplevel")
	if err != nil {
		return diagnosticWrap(faults.CodeValidationFailed, fmt.Sprintf("worktree %q is not a git checkout", worktree), err)
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil {
		return classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	if top != worktree {
		return diagnostic(faults.CodeValidationFailed, fmt.Sprintf("worktree %q is not the canonical checkout root %q", worktree, top))
	}
	pinned, err := gitRead(worktree, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return diagnosticWrap(faults.CodeValidationFailed, fmt.Sprintf("base commit %s not present in worktree %s", base, worktree), err)
	}
	if pinned != base {
		return diagnostic(faults.CodeInvariantFailed, fmt.Sprintf("base commit %s resolved to %s in worktree %s", base, pinned, worktree))
	}
	return nil
}

func captureWorktreeSnapshot(worktree, base string) (worktreeSnapshot, error) {
	var snap worktreeSnapshot
	statusBytes, err := gitExecRaw(worktree, "status", "--porcelain=v1", "-uall")
	if err != nil {
		return snap, err
	}
	snap.statusRaw = string(statusBytes)
	snap.statusDigest = sha256Hex(statusBytes)

	stagedBytes, err := gitExecRaw(worktree, "diff", "--cached", "--name-status")
	if err != nil {
		return snap, err
	}
	snap.stagedStatus = strings.TrimSpace(string(stagedBytes))
	if snap.stagedStatus == "" {
		snap.stagedStatus = "clean"
	}

	unstagedBytes, err := gitExecRaw(worktree, "diff", "--name-status")
	if err != nil {
		return snap, err
	}
	snap.unstagedStatus = strings.TrimSpace(string(unstagedBytes))
	if snap.unstagedStatus == "" {
		snap.unstagedStatus = "clean"
	}

	untrackedBytes, err := gitExecRaw(worktree, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return snap, err
	}
	untrackedPaths := []string{}
	untrackedSet := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(untrackedBytes), "\n"), "\n") {
		if line == "" {
			continue
		}
		if err := validateRelativeRepoPath(line); err != nil {
			return snap, diagnosticWrap(faults.CodeValidationFailed, fmt.Sprintf("untracked path %q", line), err)
		}
		if !untrackedSet[line] {
			untrackedSet[line] = true
			untrackedPaths = append(untrackedPaths, line)
		}
	}
	sort.Strings(untrackedPaths)
	snap.untrackedPaths = untrackedPaths

	rawDiffBytes, err := gitExecRaw(worktree, "diff", "--raw", "--no-renames", "--no-abbrev", base, "--")
	if err != nil {
		return snap, err
	}
	type rawEntry struct {
		oldMode string
		newMode string
	}
	trackedByPath := map[string]rawEntry{}
	allPathsSet := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(rawDiffBytes), "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, ":") {
			return snap, diagnostic(faults.CodeRuntimeFailed, fmt.Sprintf("unexpected git diff --raw line: %q", line))
		}
		parts := strings.Split(line[1:], "\t")
		if len(parts) != 2 {
			return snap, diagnostic(faults.CodeRuntimeFailed, fmt.Sprintf("unexpected git diff --raw path parts in line: %q", line))
		}
		fields := strings.Fields(parts[0])
		if len(fields) < 5 {
			return snap, diagnostic(faults.CodeRuntimeFailed, fmt.Sprintf("unexpected git diff --raw metadata in line: %q", line))
		}
		oldMode, newMode := fields[0], fields[1]
		path := parts[1]
		if err := validateRelativeRepoPath(path); err != nil {
			return snap, diagnosticWrap(faults.CodeValidationFailed, fmt.Sprintf("changed path %q", path), err)
		}
		trackedByPath[path] = rawEntry{oldMode: oldMode, newMode: newMode}
		allPathsSet[path] = true
	}
	for _, p := range untrackedPaths {
		allPathsSet[p] = true
	}

	allPaths := make([]string, 0, len(allPathsSet))
	for p := range allPathsSet {
		allPaths = append(allPaths, p)
	}
	sort.Strings(allPaths)

	changedFiles := make([]ManifestChangedFile, 0, len(allPaths))
	for _, p := range allPaths {
		oldMode := "000000"
		if te, ok := trackedByPath[p]; ok {
			oldMode = te.oldMode
		}
		fullPath := filepath.Join(worktree, filepath.FromSlash(p))
		info, err := os.Lstat(fullPath)
		var newMode string
		var contents *string
		if errors.Is(err, os.ErrNotExist) {
			if oldMode == "000000" {
				return snap, diagnostic(faults.CodeRuntimeFailed, fmt.Sprintf("changed path %q disappeared during capture", p))
			}
			newMode = "000000"
			contents = nil
		} else if err != nil {
			return snap, classifyDiagnostic(faults.CodeStorageFailed, err)
		} else {
			if !info.Mode().IsRegular() {
				return snap, diagnostic(faults.CodeValidationFailed, fmt.Sprintf("changed path %q is not a regular file (mode %v)", p, info.Mode()))
			}
			newMode = "100644"
			if info.Mode().Perm()&0111 != 0 {
				newMode = "100755"
			}
			fileBytes, err := os.ReadFile(fullPath)
			if err != nil {
				return snap, classifyDiagnostic(faults.CodeStorageFailed, err)
			}
			if !utf8.Valid(fileBytes) || bytes.IndexByte(fileBytes, 0) >= 0 {
				return snap, diagnostic(faults.CodeValidationFailed, fmt.Sprintf("changed path %q must be valid UTF-8 text without NUL", p))
			}
			s := string(fileBytes)
			contents = &s
		}
		changedFiles = append(changedFiles, ManifestChangedFile{
			Path:     p,
			OldMode:  oldMode,
			NewMode:  newMode,
			Contents: contents,
		})
	}

	if err := validateChangedFiles(changedFiles); err != nil {
		return snap, err
	}
	snap.changedFiles = changedFiles
	snap.contentDigest = computeChangedFilesDigest(changedFiles)

	trackedDiffBytes, err := gitExecRaw(worktree, "diff", "--no-ext-diff", "--no-renames", base, "--")
	if err != nil {
		return snap, err
	}
	var diffBuf strings.Builder
	diffBuf.Write(trackedDiffBytes)
	for _, f := range changedFiles {
		if _, wasTracked := trackedByPath[f.Path]; !wasTracked {
			diffBuf.WriteString(formatUntrackedUnifiedDiff(f))
		}
	}
	snap.diff = diffBuf.String()
	if err := validateDiffText(snap.diff, len(changedFiles)); err != nil {
		return snap, err
	}
	return snap, nil
}

func boundedUTF8String(b []byte, maxBytes int) string {
	if idx := bytes.IndexByte(b, 0); idx >= 0 {
		b = b[:idx]
	}
	if len(b) > maxBytes {
		b = b[:maxBytes]
	}
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}

func marshalCanonicalManifestJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func sameResultSnapshotIgnoringObservedAt(a, b ResultManifest) bool {
	a.ObservedAt = nil
	b.ObservedAt = nil
	return reflect.DeepEqual(a, b)
}

// CaptureRequest identifies the implementation snapshot and explicit exclusions.
type CaptureRequest struct {
	ProjectID, JobID, Worktree, Supersedes string
	IgnoredExclusions                      []string
}

// CaptureResult records a cooperative before/after snapshot and its handoff.
func (q *Queue) CaptureResult(in CaptureRequest) (_ ProjectResultAssociation, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	var result ProjectResultAssociation
	err := q.store.captureResult(in, &result)
	return result, err
}

func (s *store) captureResult(in CaptureRequest, committed *ProjectResultAssociation) error {
	p, err := s.prepareCapture(in)
	if err != nil {
		return err
	}
	return s.publishCapture(p, committed, nil, false, true)
}

// Only the cooperative reader and its private retained capture journal construct
// this value. Imported manifests never enter machine-capture publication.
type preparedCapture struct {
	manifest   ResultManifest
	supersedes string
}

func (s *store) prepareCapture(in CaptureRequest) (preparedCapture, error) {
	var prepared preparedCapture
	err := s.readCapture(in, &prepared)
	return prepared, err
}

func (s *store) readCapture(in CaptureRequest, prepared *preparedCapture) error {
	projectID, jobID, supersedesID := in.ProjectID, in.JobID, in.Supersedes
	worktreeFlag, ignoredExclusions := in.Worktree, in.IgnoredExclusions
	for _, exclusion := range ignoredExclusions {
		if strings.TrimSpace(exclusion) == "" || !utf8.ValidString(exclusion) || strings.IndexByte(exclusion, 0) >= 0 {
			return faults.New(faults.CodeValidationFailed, "--ignored-exclusion must be non-empty UTF-8 without NUL")
		}
	}
	if !isValidProjectID(projectID) || !isValidJobID(jobID) {
		return faults.New(faults.CodeValidationFailed, "project capture-result requires --project PROJECT --job JOB [--worktree PATH] [--ignored-exclusion TEXT]... [--supersedes ASSOC_ID]")
	}
	if supersedesID != "" && !isValidAssociationID(supersedesID) {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("invalid --supersedes association ID %q", supersedesID))
	}

	ctx := context.Background()
	jobReceipt, err := dbquery.New(s.db).ProjectJobReceipt(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("job %s is not attributed to project %s", jobID, projectID), err)
	}
	if err != nil {
		return classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	pjProjectID, pjRole, receiptCanonical, receiptDigest := jobReceipt.ProjectID, jobReceipt.Role, jobReceipt.Canonical, jobReceipt.Digest
	if pjProjectID != projectID {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("job %s belongs to project %s, not %s", jobID, pjProjectID, projectID))
	}
	if pjRole != "implementation" {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("job %s has role %q; project capture-result requires role \"implementation\"", jobID, pjRole))
	}
	_, agentReq, err := storedRequest(receiptCanonical)
	if err != nil || agentReq == nil {
		return faults.New(faults.CodeInvariantFailed, fmt.Sprintf("job %s does not have a valid canonical v2 agent request", jobID))
	}

	j, err := s.get(jobID)
	if err != nil {
		return err
	}
	if j.Agent == nil {
		return faults.New(faults.CodeInvariantFailed, fmt.Sprintf("job %s agent ownership unavailable", jobID))
	}

	targetWorktree := j.Agent.Worktree
	if targetWorktree != "" {
		if worktreeFlag != "" && worktreeFlag != targetWorktree {
			return faults.New(faults.CodeValidationFailed, fmt.Sprintf("--worktree %q does not match stored job worktree %q", worktreeFlag, targetWorktree))
		}
	} else {
		if worktreeFlag == "" {
			return faults.New(faults.CodeValidationFailed, "job worktree is empty; specify --worktree PATH")
		}
		targetWorktree = worktreeFlag
	}

	if err := verifyCanonicalWorktreeCheckout(targetWorktree, agentReq.Base); err != nil {
		return err
	}

	// Snapshot 1 (before)
	snap1, err := captureWorktreeSnapshot(targetWorktree, agentReq.Base)
	if err != nil {
		return err
	}
	// Snapshot 2 (after)
	snap2, err := captureWorktreeSnapshot(targetWorktree, agentReq.Base)
	if err != nil {
		return err
	}
	if snap1.statusDigest != snap2.statusDigest ||
		snap1.contentDigest != snap2.contentDigest ||
		snap1.stagedStatus != snap2.stagedStatus ||
		snap1.unstagedStatus != snap2.unstagedStatus ||
		snap1.diff != snap2.diff ||
		!reflect.DeepEqual(snap1.changedFiles, snap2.changedFiles) ||
		!reflect.DeepEqual(snap1.untrackedPaths, snap2.untrackedPaths) {
		return faults.New(faults.CodeRuntimeFailed, "worktree changed during capture; retry when quiescent")
	}

	evidenceDigests := map[string]string{
		"stdout":            "not_recorded",
		"stderr":            "not_recorded",
		"terminal_evidence": "not_recorded",
	}
	inlineOutput := ""
	if j.Stdout != "" {
		if b, err := os.ReadFile(j.Stdout); err == nil {
			evidenceDigests["stdout"] = sha256Hex(b)
			inlineOutput = boundedUTF8String(b, 2048)
		}
	}
	if j.Stderr != "" {
		if b, err := os.ReadFile(j.Stderr); err == nil {
			evidenceDigests["stderr"] = sha256Hex(b)
		}
	}
	if j.Agent != nil && j.Agent.TerminalEvidence != "" {
		if b, err := os.ReadFile(j.Agent.TerminalEvidence); err == nil {
			evidenceDigests["terminal_evidence"] = sha256Hex(b)
		}
	}

	exclusions := []string(ignoredExclusions)
	if exclusions == nil {
		exclusions = []string{}
	}

	ts := now()
	candidate := ResultManifest{
		Version:             1,
		Kind:                "result",
		ProjectID:           projectID,
		Source:              "machine_capture",
		ObservedAt:          &ts,
		ClaimLabel:          "machine_verified_snapshot",
		JobID:               jobID,
		RequestDigest:       receiptDigest,
		Base:                agentReq.Base,
		Worktree:            targetWorktree,
		StagedStatus:        snap1.stagedStatus,
		UntrackedPaths:      snap1.untrackedPaths,
		UnstagedStatus:      snap1.unstagedStatus,
		BeforeStatusDigest:  snap1.statusDigest,
		AfterStatusDigest:   snap2.statusDigest,
		BeforeContentDigest: snap1.contentDigest,
		AfterContentDigest:  snap2.contentDigest,
		IgnoredExclusions:   exclusions,
		ChangedFiles:        snap1.changedFiles,
		Diff:                snap1.diff,
		InlineOutput:        inlineOutput,
		EvidenceDigests:     evidenceDigests,
		ClaimedValidation: ClaimedValidation{
			Status:      "not_run",
			Commands:    []string{},
			Results:     "not_run",
			Provenance:  "not_recorded",
			Limitations: "machine snapshot capture verifies worktree file state only, not test execution",
		},
	}
	prepared.manifest, prepared.supersedes = candidate, supersedesID
	return nil
}

// Publication of a prepared capture never reads the filesystem. With reuse=true
// the existing CLI retains its snapshot-equivalence idempotency. Retained UI
// snapshots use reuse=false: observed_at and every reviewed byte stay exact.
func (s *store) publishCapture(p preparedCapture, committed *ProjectResultAssociation, check func(sqlQueryer) error, preview, reuse bool) error {
	candidate, supersedesID := p.manifest, p.supersedes
	projectID, jobID, targetWorktree, receiptDigest := candidate.ProjectID, candidate.JobID, candidate.Worktree, candidate.RequestDigest
	ctx := context.Background()
	var supersedesPtr *string
	if supersedesID != "" {
		supersedesPtr = &supersedesID
	}
	run := func(c sqlQueryer) error {
		if check != nil {
			if err := check(c); err != nil {
				return err
			}
		}
		queries := dbquery.New(c)
		metadata, err := queries.CaptureJobMetadata(ctx, jobID)
		if err != nil {
			return classifyDiagnostic(faults.CodeDatabaseRead, err)
		}
		if metadata.ProjectID != projectID || metadata.Role != "implementation" || metadata.Digest != receiptDigest {
			return faults.New(faults.CodeInvariantFailed, "job metadata mismatch during capture-result commit")
		}
		if metadata.Worktree != "" && metadata.Worktree != targetWorktree {
			return faults.New(faults.CodeInvariantFailed, fmt.Sprintf("stored job worktree %q does not match capture worktree %q", metadata.Worktree, targetWorktree))
		}
		if supersedesID != "" {
			sup, err := queries.AssociationIdentity(ctx, supersedesID)
			if errors.Is(err, sql.ErrNoRows) {
				return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("supersedes association %s does not exist", supersedesID), err)
			}
			if err != nil {
				return classifyDiagnostic(faults.CodeDatabaseRead, err)
			}
			if sup.ProjectID != projectID || sup.JobID != jobID || sup.Kind != "result" {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("supersedes association %s must belong to same project %s, job %s, and kind result", supersedesID, projectID, jobID))
			}
		}
		ts := now()
		// Check if an existing result association for (project_id, job_id) with matching supersedes_id
		// already captured this exact quiescent snapshot (allowing idempotent retry across CLI invocations).
		if reuse {
			rows, err := queries.CapturedAssociations(ctx, dbquery.CapturedAssociationsParams{ProjectID: projectID, JobID: jobID})
			if err != nil {
				return classifyDiagnostic(faults.CodeDatabaseRead, err)
			}
			for _, row := range rows {
				if !nullStringEqual(row.SupersedesID, supersedesPtr) {
					continue
				}
				if exPM, err := parseManifestBytes(row.Canonical, "result", projectID); err == nil && exPM.Result != nil && exPM.Source == "machine_capture" {
					if sameResultSnapshotIgnoringObservedAt(*exPM.Result, candidate) {
						*committed = summarizeAssociation(row.ID, row.Sequence, projectID, jobID, "result", row.ManifestDigest, supersedesPtr, row.RecordedAt, &exPM, true)
						return sealLaneAndDeliverMail(ctx, c.(*sql.Conn), exPM, projectID, jobID, row.ID, targetWorktree, row.RecordedAt)
					}
				}
			}
		}

		rawBytes, err := marshalCanonicalManifestJSON(candidate)
		if err != nil {
			return classifyDiagnostic(faults.CodeInvariantFailed, err)
		}
		m, err := parseManifestBytes(rawBytes, "result", projectID)
		if err != nil {
			return faults.Wrap(faults.CodeInvariantFailed, "generated capture manifest invalid", err)
		}

		assocID := deterministicAssociationID(projectID, jobID, m.Kind, m.Digest, supersedesID)
		if supersedesID != "" && supersedesID == assocID {
			return faults.New(faults.CodeValidationFailed, "result association cannot supersede itself")
		}

		existing, err := queries.ExistingAssociation(ctx, dbquery.ExistingAssociationParams{ProjectID: projectID, JobID: jobID, Kind: "result", ManifestDigest: m.Digest})
		if err == nil {
			if err := verifyStoredManifest(ctx, c, m); err != nil {
				return err
			}
			if !nullStringEqual(existing.SupersedesID, supersedesPtr) || existing.ID != assocID {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("result association for project %s job %s kind result manifest %s already exists with conflicting supersedes linkage", projectID, jobID, m.Digest))
			}
			*committed = summarizeAssociation(existing.ID, existing.Sequence, projectID, jobID, m.Kind, m.Digest, supersedesPtr, existing.RecordedAt, &m, true)
			if preview {
				return nil
			}
			return sealLaneAndDeliverMail(ctx, c.(*sql.Conn), m, projectID, jobID, existing.ID, targetWorktree, existing.RecordedAt)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return classifyDiagnostic(faults.CodeDatabaseRead, err)
		}

		if preview {
			*committed = summarizeAssociation(assocID, 0, projectID, jobID, m.Kind, m.Digest, supersedesPtr, "", &m, true)
			return nil
		}
		if err := insertManifestConn(ctx, c.(*sql.Conn), m, ts); err != nil {
			return err
		}
		seq, err := queries.InsertAssociation(ctx, dbquery.InsertAssociationParams{ID: assocID, ProjectID: projectID, JobID: jobID, Kind: m.Kind, ManifestDigest: m.Digest, SupersedesID: sqlStringPtr(supersedesPtr), RecordedAt: ts})
		if err != nil {
			return err
		}
		*committed = summarizeAssociation(assocID, seq, projectID, jobID, m.Kind, m.Digest, supersedesPtr, ts, &m, true)
		return sealLaneAndDeliverMail(ctx, c.(*sql.Conn), m, projectID, jobID, assocID, targetWorktree, ts)
	}
	var err error
	if preview {
		err = s.readSchema5(run)
	} else {
		err = s.write(func(c *sql.Conn) error { return run(c) })
	}
	if err != nil {
		return classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}
	return nil
}

type WorktreeCheckRequest struct {
	ProjectID, JobID, ManifestDigest, Worktree string
}

// VerifyWorktree compares the live worktree to an exact retained snapshot.
func (r *Reader) VerifyWorktree(in WorktreeCheckRequest) (_ ProjectWorktreeCheckOutput, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	var result ProjectWorktreeCheckOutput
	err := r.store.verifyWorktree(in, &result)
	return result, err
}

func (s *store) verifyWorktree(in WorktreeCheckRequest, result *ProjectWorktreeCheckOutput) (resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	projectID, jobID, targetDigest, worktreeFlag := in.ProjectID, in.JobID, in.ManifestDigest, in.Worktree
	if !isValidProjectID(projectID) || !isValidJobID(jobID) {
		return faults.New(faults.CodeValidationFailed, "project verify-worktree requires --project PROJECT --job JOB [--manifest DIGEST] [--worktree PATH]")
	}
	if targetDigest != "" && !isValidHexLen(targetDigest, 64) {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("invalid --manifest digest %q (must be 64-hex SHA-256)", targetDigest))
	}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	projectJob, err := dbquery.New(tx).ProjectJobByID(ctx, dbquery.ProjectJobByIDParams{ProjectID: projectID, JobID: jobID})
	if errors.Is(err, sql.ErrNoRows) {
		return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("job %s is not attributed to project %s", jobID, projectID), err)
	}
	if err != nil {
		return err
	}
	pj := projectJobFromRow(projectJob)
	if pj.role != "implementation" {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("job %s has role %q; project verify-worktree requires role \"implementation\"", jobID, pj.role))
	}

	j, err := getJobTx(ctx, tx, jobID)
	if err != nil {
		return err
	}
	assocs, manifests, _, err := loadJobAssociationsTx(ctx, tx, projectID, jobID)
	if err != nil {
		return err
	}
	_, _, revDim, err := computeJobDimensionsTx(ctx, tx, pj, j, assocs, manifests)
	if err != nil {
		return err
	}

	var selectedManifest *parsedManifest
	if targetDigest != "" {
		for i := len(assocs) - 1; i >= 0; i-- {
			if assocs[i].Kind == "result" && assocs[i].ManifestDigest == targetDigest {
				if assocs[i].Availability != "available" || manifests[i] == nil || manifests[i].Result == nil {
					return faults.New(faults.CodeInvariantFailed, fmt.Sprintf("recorded result manifest %s is unavailable or invalid", targetDigest))
				}
				selectedManifest = manifests[i]
				break
			}
		}
		if selectedManifest == nil {
			return faults.New(faults.CodeNotFound, fmt.Sprintf("manifest %s is not a recorded result association for job %s in project %s", targetDigest, jobID, projectID))
		}
	} else {
		for i := len(assocs) - 1; i >= 0; i-- {
			if assocs[i].Kind == "result" {
				if assocs[i].Availability != "available" || manifests[i] == nil || manifests[i].Result == nil {
					return faults.New(faults.CodeInvariantFailed, fmt.Sprintf("latest result manifest %s is unavailable or invalid", assocs[i].ManifestDigest))
				}
				selectedManifest = manifests[i]
				break
			}
		}
		if selectedManifest == nil {
			return faults.New(faults.CodeNotFound, fmt.Sprintf("job %s in project %s has no recorded result associations", jobID, projectID))
		}
	}

	res := selectedManifest.Result
	jobWorktree := ""
	if j.Agent != nil {
		jobWorktree = j.Agent.Worktree
	}
	targetWorktree := jobWorktree
	if targetWorktree != "" {
		if worktreeFlag != "" && worktreeFlag != targetWorktree {
			return faults.New(faults.CodeValidationFailed, fmt.Sprintf("--worktree %q does not match stored job worktree %q", worktreeFlag, targetWorktree))
		}
	} else if worktreeFlag != "" {
		targetWorktree = worktreeFlag
	} else {
		targetWorktree = res.Worktree
	}

	// Check if a review exists specifically for selectedManifest.Digest if revDim was for a different digest.
	hasReviewForTarget := revDim.ReviewManifestDigest != "" && revDim.ReviewedResultDigest == selectedManifest.Digest
	if !hasReviewForTarget {
		rows, err := dbquery.New(tx).TargetReviews(ctx, dbquery.TargetReviewsParams{ProjectID: projectID, TargetJobID: sql.NullString{String: jobID, Valid: true}})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if pm, err := parseManifestBytes(row.Canonical, "review_result", projectID); err == nil && pm.ReviewResult != nil {
				if pm.ReviewResult.ReviewedResultDigest == selectedManifest.Digest {
					hasReviewForTarget = true
					if targetDigest != "" {
						revDim.ReviewJobID = row.JobID
						revDim.ReviewAssociationID = row.ID
						revDim.ReviewManifestDigest = row.ManifestDigest
						revDim.Status = pm.ReviewResult.Verdict
						revDim.Verdict = pm.ReviewResult.Verdict
						revDim.ReviewSessionID = pm.ReviewResult.ReviewSessionID
						revDim.ReviewedResultDigest = pm.ReviewResult.ReviewedResultDigest
					}
					break
				}
			}
		}
	}

	worktreeMatch, differences := compareLiveWorktreeToSnapshot(targetWorktree, *res)
	if worktreeMatch != "matches_snapshot" && (hasReviewForTarget || revDim.ReviewManifestDigest != "") {
		revDim.StaleForCurrentWorktree = true
	}

	*result = ProjectWorktreeCheckOutput{
		ProjectID:      projectID,
		JobID:          jobID,
		ManifestDigest: selectedManifest.Digest,
		Worktree:       targetWorktree,
		CheckedAt:      now(),
		Freshness:      "live_worktree_check",
		WorktreeMatch:  worktreeMatch,
		Differences:    differences,
		Review:         revDim,
		Integration:    "not_authorized",
	}
	return nil
}

func compareLiveWorktreeToSnapshot(worktree string, res ResultManifest) (string, []string) {
	if err := verifyCanonicalWorktreeCheckout(worktree, res.Base); err != nil {
		return "worktree_unavailable", []string{err.Error()}
	}

	// Verification is observational: even a stat-cache refresh must not write
	// the index. Keep this scoped here; capture retains its existing semantics.
	// diff's auto-refresh also needs disabling; optional-lock suppression alone
	// does not prevent that refresh on supported Git versions.
	statusBytes, err := gitExecRaw(worktree, "--no-optional-locks", "status", "--porcelain=v1", "-uall")
	if err != nil {
		return "worktree_unavailable", []string{err.Error()}
	}
	liveStatusDigest := sha256Hex(statusBytes)

	stagedBytes, err := gitVerifyDiff(worktree, "--cached", "--name-status")
	if err != nil {
		return "worktree_unavailable", []string{err.Error()}
	}
	liveStagedStatus := strings.TrimSpace(string(stagedBytes))
	if liveStagedStatus == "" {
		liveStagedStatus = "clean"
	}

	unstagedBytes, err := gitVerifyDiff(worktree, "--name-status")
	if err != nil {
		return "worktree_unavailable", []string{err.Error()}
	}
	liveUnstagedStatus := strings.TrimSpace(string(unstagedBytes))
	if liveUnstagedStatus == "" {
		liveUnstagedStatus = "clean"
	}

	untrackedBytes, err := gitExecRaw(worktree, "--no-optional-locks", "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return "worktree_unavailable", []string{err.Error()}
	}
	liveUntracked := []string{}
	liveUntrackedSet := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(untrackedBytes), "\n"), "\n") {
		if line == "" {
			continue
		}
		if !liveUntrackedSet[line] {
			liveUntrackedSet[line] = true
			liveUntracked = append(liveUntracked, line)
		}
	}
	sort.Strings(liveUntracked)

	rawDiffBytes, err := gitVerifyDiff(worktree, "--raw", "--no-renames", "--no-abbrev", res.Base, "--")
	if err != nil {
		return "worktree_unavailable", []string{err.Error()}
	}

	type liveFileState struct {
		oldMode   string
		newMode   string
		contents  *string
		irregular bool
	}
	liveByPath := map[string]liveFileState{}
	allLivePaths := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(rawDiffBytes), "\n"), "\n") {
		if line == "" || !strings.HasPrefix(line, ":") {
			continue
		}
		parts := strings.Split(line[1:], "\t")
		if len(parts) < 2 {
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) < 5 {
			continue
		}
		path := parts[len(parts)-1]
		liveByPath[path] = liveFileState{oldMode: fields[0], newMode: fields[1]}
		allLivePaths[path] = true
	}
	for _, p := range liveUntracked {
		allLivePaths[p] = true
	}

	for p := range allLivePaths {
		oldMode := "000000"
		if st, ok := liveByPath[p]; ok {
			oldMode = st.oldMode
		}
		fullPath := filepath.Join(worktree, filepath.FromSlash(p))
		info, err := os.Lstat(fullPath)
		if errors.Is(err, os.ErrNotExist) {
			liveByPath[p] = liveFileState{oldMode: oldMode, newMode: "000000", contents: nil}
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			liveByPath[p] = liveFileState{oldMode: oldMode, irregular: true}
			continue
		}
		newMode := "100644"
		if info.Mode().Perm()&0111 != 0 {
			newMode = "100755"
		}
		b, err := os.ReadFile(fullPath)
		if err != nil {
			liveByPath[p] = liveFileState{oldMode: oldMode, irregular: true}
			continue
		}
		s := string(b)
		liveByPath[p] = liveFileState{oldMode: oldMode, newMode: newMode, contents: &s}
	}

	expectedByPath := map[string]ManifestChangedFile{}
	for _, f := range res.ChangedFiles {
		expectedByPath[f.Path] = f
	}

	diffSet := map[string]bool{}
	for p, exp := range expectedByPath {
		live, ok := liveByPath[p]
		if !ok || live.irregular || live.oldMode != exp.OldMode || live.newMode != exp.NewMode {
			diffSet[p] = true
			continue
		}
		if (exp.Contents == nil) != (live.contents == nil) {
			diffSet[p] = true
			continue
		}
		if exp.Contents != nil && *exp.Contents != *live.contents {
			diffSet[p] = true
		}
	}
	for p := range liveByPath {
		if _, ok := expectedByPath[p]; !ok {
			diffSet[p] = true
		}
	}

	statusMismatch := false
	if liveStagedStatus != res.StagedStatus || liveUnstagedStatus != res.UnstagedStatus || !reflect.DeepEqual(liveUntracked, res.UntrackedPaths) {
		statusMismatch = true
	}
	if isValidHexLen(res.AfterStatusDigest, 64) && liveStatusDigest != res.AfterStatusDigest {
		statusMismatch = true
	}
	if statusMismatch && len(diffSet) == 0 {
		diffSet["git_status"] = true
	}

	if len(diffSet) == 0 {
		return "matches_snapshot", []string{}
	}
	diffs := make([]string, 0, len(diffSet))
	for p := range diffSet {
		diffs = append(diffs, p)
	}
	sort.Strings(diffs)
	return "modified_since_snapshot", diffs
}
