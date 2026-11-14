package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"ocq/internal/core/dbquery"
)

const mainSelectionPrefix = "ocq-main-v1 setup: "

func mainFile(id, suffix string) string { return ".main-" + id + "." + suffix + ".json" }

func nativeJournalRoom(j *os.File, additional int) error {
	if _, err := j.Seek(0, 0); err != nil {
		return err
	}
	entries, err := workEntries(j)
	if err == nil && len(entries)+additional > workFileLimit {
		err = errors.New("project auxiliary journal bound reached; no automatic retirement or retry")
	}
	return err
}

func mainGitRead(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	argv := append([]string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)
	c := exec.CommandContext(ctx, "git", argv...)
	c.Dir = dir
	c.WaitDelay = handoffWaitDelay
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_OPTIONAL_LOCKS=") {
			env = append(env, entry)
		}
	}
	c.Env = append(env, "GIT_OPTIONAL_LOCKS=0")
	var out handoffOutput
	c.Stdout = &out
	c.Stderr = io.Discard
	if c.Run() != nil {
		return "", errors.New("bounded read-only canonical Main Git inspection failed")
	}
	return strings.TrimSpace(out.String()), nil
}

func parseMainProfile(raw string) (MainProfile, error) {
	var p MainProfile
	if _, err := decodeStrictJSONObject([]byte(raw), 16<<10, &p); err != nil {
		return p, err
	}
	if p.Role != "integrator" || p.Agent == "" || strings.ContainsAny(p.Agent, "/?#\\ \t\r\n\x00") || p.ProviderID == "" || p.ModelID == "" || !strings.HasPrefix(strings.TrimSpace(string(p.Permissions)), "[") {
		return p, errors.New("explicit integrator role, configured agent/model and ordered permission array required")
	}
	_, err := parseJobDraftPermissions(p.Permissions)
	return p, err
}

// Primary checkout only. Opening dirty MAIN is fine; never modify Git state.
func inspectCanonicalMain(repo, branch string) (common, head string, dirty bool, err error) {
	if !filepath.IsAbs(repo) || strings.ContainsAny(repo, "\r\n\x00") || !strings.HasPrefix(branch, "refs/heads/") || strings.ContainsAny(branch, "\r\n\x00") {
		err = errors.New("explicit canonical primary checkout and full refs/heads/... branch required")
		return
	}
	head, err = mainGitRead(repo, "rev-parse", "HEAD")
	if err != nil {
		return
	}
	_, common, err = canonicalizeGitRepo(repo, head)
	if err != nil {
		return
	}
	var gitDir, current string
	gitDir, err = mainGitRead(repo, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return
	}
	gitDir, err = filepath.EvalSymlinks(gitDir)
	if err != nil {
		return
	}
	if gitDir != common {
		err = errors.New("Main requires the primary checkout, not a linked Workmux/worker checkout")
		return
	}
	current, err = mainGitRead(repo, "symbolic-ref", "HEAD")
	if err != nil || current != branch {
		err = errors.New("canonical checkout is detached or on another branch; select its configured MAIN branch manually without discarding work")
		return
	}
	var inventory string
	inventory, err = mainGitRead(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return
	}
	matches := 0
	for _, entry := range strings.Split(inventory, "\n\n") {
		path, ref := "", ""
		for _, line := range strings.Split(entry, "\n") {
			if strings.HasPrefix(line, "worktree ") {
				path = strings.TrimPrefix(line, "worktree ")
			}
			if strings.HasPrefix(line, "branch ") {
				ref = strings.TrimPrefix(line, "branch ")
			}
		}
		if ref == branch {
			if path != repo {
				err = errors.New("configured MAIN branch has ambiguous checkout ownership")
				return
			}
			matches++
		}
	}
	if matches != 1 {
		err = errors.New("exact canonical MAIN worktree inventory unavailable")
		return
	}
	var status string
	status, err = mainGitRead(repo, "status", "--porcelain=v1", "--untracked-files=normal")
	dirty = status != ""
	return
}

func decodeMain(raw []byte) (MainPrepareRequest, error) {
	var in MainPrepareRequest
	if _, err := decodeStrictJSONObject(raw, WorkRequestBytes, &in); err != nil {
		return in, err
	}
	if in.Protocol != MainProtocol || !isValidProjectID(in.ProjectID) || !workID(in.OperationID) || !utf8.Valid(raw) || len(in.Prompt) > 16<<10 {
		return in, errors.New("invalid bounded Main operation")
	}
	switch in.Mode {
	case "create", "adopt":
		if in.Repo == "" || in.MainBranch == "" || in.Label == "" || in.Profile == "" || in.Role == "" {
			return in, errors.New("initial Main setup requires explicit repo/branch/label/profile/role")
		}
		if _, err := parseMainRole(in.Role); err != nil {
			return in, err
		}
		if _, err := parseMainProfile(in.Profile); err != nil {
			return in, err
		}
		rev, err := observationDecimal(in.ExpectedRevision)
		if err != nil || in.Mode == "create" && rev != 0 || in.Mode == "adopt" && rev < 1 {
			return in, errors.New("explicit Main registration CAS revision required")
		}
	case "refresh", "reopen", "send":
		if in.Repo != "" || in.MainBranch != "" || in.Label != "" || in.Profile != "" || in.Role != "" {
			return in, errors.New("reuse the SQL-selected Main setup; do not silently change its profile/checkout")
		}
		if in.Mode == "send" && strings.TrimSpace(in.Prompt) == "" || in.Mode != "send" && in.Prompt != "" {
			return in, errors.New("only a distinct explicit send may carry a subsequent prompt")
		}
		if in.Mode == "refresh" {
			rev, e := observationDecimal(in.ExpectedRevision)
			if e != nil || rev < 1 {
				return in, errors.New("Main anchor refresh requires explicit CAS revision")
			}
		}
	default:
		return in, errors.New("unsupported Main operation")
	}
	return in, nil
}

func mainLoad(r *Reader, j *os.File, project, id string) (mainRecord, MainPrepareRequest, error) {
	var rec mainRecord
	b, err := workRead(j, mainFile(id, "intent"))
	if err != nil {
		return rec, MainPrepareRequest{}, err
	}
	if _, err = decodeStrictJSONObject(b, WorkResponseBytes, &rec); err != nil {
		return rec, MainPrepareRequest{}, err
	}
	in, err := decodeMain([]byte(rec.Input))
	if err != nil || in.ProjectID != project || in.OperationID != id || rec.Protocol != MainProtocol || rec.Digest != sha256Hex([]byte(rec.Input)) || rec.Queue != r.store.dir {
		return rec, in, errors.New("Main intent content/identity mismatch")
	}
	info, err := os.Lstat(filepath.Join(r.store.dir, "queue.db"))
	if err != nil {
		return rec, in, err
	}
	st := info.Sys().(*syscall.Stat_t)
	if rec.Device != strconv.FormatUint(uint64(st.Dev), 10) || rec.Inode != strconv.FormatUint(st.Ino, 10) {
		return rec, in, errors.New("Main queue device/inode changed")
	}
	if rec.Role.Protocol != MainRoleProtocol || rec.Role.Key != mainRoleKey || rec.Role.SHA256 != mainRoleDigest(rec.Role) || rec.Role.ProjectID != project || rec.Role.SessionID != rec.SessionID || rec.Role.SetupID != rec.SetupID || rec.Role.StateDir != rec.Queue || rec.Role.Repo != rec.Repo || rec.Role.MainBranch != rec.Branch || rec.Role.GitCommonDir != rec.Common {
		return rec, in, errors.New("retained Main role contract missing/mismatched; no implicit upgrade")
	}
	return rec, in, nil
}

func selectedMain(r *Reader, j *os.File, project string) (mainRecord, MainPrepareRequest, ProjectBinding, error) {
	binding, err := workBinding(r, project)
	if err != nil {
		return mainRecord{}, MainPrepareRequest{}, binding, err
	}
	raw, err := r.Manifest(project, binding.HandoffManifestDigest)
	if err != nil {
		return mainRecord{}, MainPrepareRequest{}, binding, err
	}
	m, err := parseManifestBytes(raw, "handoff", project)
	if err != nil {
		return mainRecord{}, MainPrepareRequest{}, binding, err
	}
	id := ""
	for _, decision := range m.Handoff.Decisions {
		if strings.HasPrefix(decision, mainSelectionPrefix) {
			if id != "" {
				return mainRecord{}, MainPrepareRequest{}, binding, errors.New("ambiguous Main setup selection")
			}
			id = strings.TrimPrefix(decision, mainSelectionPrefix)
		}
	}
	if !workID(id) {
		return mainRecord{}, MainPrepareRequest{}, binding, errors.New("legacy coordinator binding is not configured Main; explicitly adopt it with a validated profile")
	}
	rec, in, err := mainLoad(r, j, project, id)
	if err == nil && (rec.SetupID != id || rec.SessionID != binding.ElephantSessionID || rec.Repo != binding.RepoRoot || rec.Common != binding.GitCommonDir || binding.OpenCodeDirectory != rec.Repo || binding.OpenCodeProjectID != rec.ProjectID) {
		err = errors.New("SQL binding does not match selected Main ownership; no replacement")
	}
	return rec, in, binding, err
}

func PrepareProjectMain(dir string, raw []byte) (MainStatus, error) {
	in, err := decodeMain(raw)
	if err != nil {
		return MainStatus{}, err
	}
	j, err := openLocalJournal(dir, workJournal(in.ProjectID))
	if err != nil {
		return MainStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return MainStatus{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return MainStatus{}, err
	}
	defer r.Close()
	if b, e := workRead(j.dir, mainFile(in.OperationID, "intent")); e == nil {
		var rec mainRecord
		if json.Unmarshal(b, &rec) != nil || rec.Input != string(raw) {
			return MainStatus{}, errors.New("Main operation ID binds different original bytes")
		}
		return inspectMainRecord(r, j.dir, in.ProjectID, in.OperationID, false)
	} else if !errors.Is(e, syscall.ENOENT) {
		return MainStatus{}, e
	}
	entries, err := workEntries(j.dir)
	if err != nil {
		return MainStatus{}, err
	}
	count := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".main-") && strings.HasSuffix(e.Name(), ".intent.json") {
			count++
		}
		if strings.HasPrefix(e.Name(), ".main-"+in.OperationID+".") {
			return MainStatus{}, errors.New("orphan Main evidence; do not retry")
		}
	}
	if count >= workIntentLimit {
		return MainStatus{}, errors.New("Main operation bound reached")
	}
	rec := mainRecord{Protocol: MainProtocol, Input: string(raw), Digest: sha256Hex(raw), Queue: r.store.dir, Repo: in.Repo, Branch: in.MainBranch, SetupID: in.OperationID, Created: now()}
	var binding ProjectBinding
	var profile MainProfile
	var predecessor, predecessorDigest string
	if in.Mode == "create" {
		_, e := dbquery.New(r.store.db).ProjectByID(context.Background(), in.ProjectID)
		if !errors.Is(e, sql.ErrNoRows) {
			return MainStatus{}, errors.New("new Main creation requires a genuinely unbound project")
		}
		predecessor, predecessorDigest, err = mainCreatePredecessor(r, j, in.ProjectID, in.OperationID, rec.Digest)
		if err != nil {
			return MainStatus{}, err
		}
	} else {
		binding, err = workBinding(r, in.ProjectID)
		if err != nil {
			return MainStatus{}, err
		}
		if in.ExpectedRevision != "" && strconv.Itoa(binding.Revision) != in.ExpectedRevision {
			return MainStatus{}, errors.New("Main binding CAS revision changed")
		}
		if in.Mode == "adopt" {
			if in.Repo != binding.RepoRoot {
				return MainStatus{}, errors.New("explicit adoption must use existing bound canonical repository")
			}
		} else {
			setup, _, _, e := selectedMain(r, j.dir, in.ProjectID)
			if e != nil {
				return MainStatus{}, e
			}
			rec.Repo, rec.Branch, rec.SetupID = setup.Repo, setup.Branch, setup.SetupID
			rec.SessionID, rec.ProjectID, rec.Metadata, rec.OwnershipMode = setup.SessionID, setup.ProjectID, setup.Metadata, setup.OwnershipMode
			rec.Role = setup.Role
		}
	}
	rec.Common, rec.Head, _, err = inspectCanonicalMain(rec.Repo, rec.Branch)
	if err != nil {
		return MainStatus{}, err
	}
	if binding.GitCommonDir != "" && binding.GitCommonDir != rec.Common {
		return MainStatus{}, errors.New("Main git common directory changed")
	}
	rec.Binding = binding
	rec.Server, err = discoverNativeService(rec.Repo)
	if err != nil {
		return MainStatus{}, err
	}
	if in.Mode == "create" || in.Mode == "adopt" {
		profile, err = parseMainProfile(in.Profile)
		if err != nil {
			return MainStatus{}, err
		}
	}
	if in.Mode == "create" {
		rec.SessionID, err = randomID("ses_ocqmain_")
		if err != nil {
			return MainStatus{}, err
		}
		token, e := composeRandom()
		if e != nil {
			return MainStatus{}, e
		}
		p := map[string]any{"agent": profile.Agent, "model": agentModel{ID: profile.ModelID, ProviderID: profile.ProviderID, Variant: profile.Variant}, "permissions": json.RawMessage(profile.Permissions)}
		// Hash generic JSON maps, exactly matching the native TUI verifier.
		pbytes, _ := json.Marshal(p)
		var generic map[string]any
		_ = json.Unmarshal(pbytes, &generic)
		rec.ProfileSHA = nativeHash(generic)
		rec.Metadata, _ = json.Marshal(map[string]any{"ocq_main": map[string]string{"project_id": in.ProjectID, "main_id": rec.SetupID, "role": "integrator", "owner_token": token, "profile_sha256": rec.ProfileSHA, "main_branch": rec.Branch, "git_common_dir": rec.Common}})
		rec.OwnershipMode = "ocq_main"
		agentBody, e := nativeCall(rec.Repo, rec.Server, "get", "/api/agent/"+url.PathEscape(profile.Agent)+locationQuery(rec.Repo), nil)
		if e != nil {
			return MainStatus{}, e
		}
		var agent struct {
			Location struct {
				Directory string `json:"directory"`
				Project   struct {
					ID string `json:"id"`
				} `json:"project"`
			} `json:"location"`
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(agentBody, &agent) != nil || agent.Location.Directory != rec.Repo || agent.Data.ID != profile.Agent || agent.Location.Project.ID == "" {
			return MainStatus{}, errors.New("exact configured Main agent/project/location unavailable")
		}
		rec.ProjectID = agent.Location.Project.ID
		create := map[string]any{"id": rec.SessionID, "title": "Main / Integrator: " + in.Label, "agent": profile.Agent, "model": generic["model"], "permissions": generic["permissions"], "metadata": json.RawMessage(rec.Metadata), "location": map[string]string{"directory": rec.Repo}}
		b, _ := json.Marshal(create)
		rec.SessionJSON = string(b)
	} else {
		if in.Mode == "adopt" {
			rec.SessionID = binding.ElephantSessionID
			rec.ProjectID = binding.OpenCodeProjectID
		}
		s, e := nativeSession(rec.Repo, rec.Server, rec.SessionID)
		if e != nil {
			return MainStatus{}, e
		}
		if in.Mode == "adopt" {
			metadata, _ := s["metadata"].(map[string]any)
			if owned, _ := metadata["ocq"].(map[string]any); owned["job_id"] != nil {
				return MainStatus{}, errors.New("a queued worker cannot be adopted as persistent Main")
			}
			if metadata["ocq_main"] != nil {
				return MainStatus{}, errors.New("session already has Main ownership; recover its exact setup rather than reinterpreting it as legacy")
			}
			rec.Metadata, _ = json.Marshal(s["metadata"])
			rec.OwnershipMode = "legacy_adoption"
			p, _ := json.Marshal(map[string]any{"agent": profile.Agent, "model": agentModel{ID: profile.ModelID, ProviderID: profile.ProviderID, Variant: profile.Variant}, "permissions": json.RawMessage(profile.Permissions)})
			var generic map[string]any
			_ = json.Unmarshal(p, &generic)
			if nativeProfile(s) != nativeHash(generic) {
				return MainStatus{}, errors.New("legacy adoption requires an explicitly selected profile matching the existing session; no profile changes attempted")
			}
		}
		if e = nativeIdentity(s, rec.SessionID, rec.Repo, rec.ProjectID, nativeHash(json.RawMessage(rec.Metadata))); e != nil {
			return MainStatus{}, e
		}
		rec.ProfileSHA = nativeProfile(s)
		b, _ := json.Marshal(map[string]any{"agent": s["agent"], "model": s["model"], "permissions": s["permissions"], "metadata": s["metadata"], "location": s["location"], "id": s["id"]})
		rec.SessionJSON = string(b)
	}
	if in.Mode == "create" || in.Mode == "adopt" {
		rec.Role, err = prepareMainRole(r, rec, in.ProjectID, in.Role)
		if err != nil {
			return MainStatus{}, err
		}
	}
	if in.Mode == "create" || in.Mode == "adopt" || in.Mode == "refresh" {
		expected := binding.Revision
		mode := "rebind"
		if in.Mode == "create" {
			mode = "new"
		}
		label := in.Label
		if label == "" {
			label, err = dbquery.New(r.store.db).ProjectLabel(context.Background(), in.ProjectID)
			if err != nil {
				return MainStatus{}, err
			}
		}
		reg, e := BuildProjectRegistrationDraft(ProjectRegistrationDraft{Mode: mode, Key: "main-" + sha256Hex([]byte(in.ProjectID+"\n"+in.OperationID)), ProjectID: in.ProjectID, Label: label, ExpectedRevision: &expected, Repo: rec.Repo, CommitAnchor: rec.Head, ElephantSessionID: rec.SessionID, ExpectedOpenCodeProjectID: rec.ProjectID, ExpectedOpenCodeDirectory: rec.Repo, AuthRef: MainProtocol, Intent: "Persistent project Main / Integrator; not a queued job or integration authorization.", Decisions: []string{mainSelectionPrefix + rec.SetupID}, Risks: []string{"Native Main accepts normal human conversation and explicit profile changes; worker restrictions do not apply."}})
		if e != nil {
			return MainStatus{}, e
		}
		rec.RegistrationJSON = string(reg)
	}
	if in.Prompt != "" {
		rec.PromptID, err = randomID("msg_ocqmain_")
		if err != nil {
			return MainStatus{}, err
		}
		b, _ := json.Marshal(map[string]any{"id": rec.PromptID, "text": in.Prompt, "metadata": map[string]any{"ocq_main_input": map[string]string{"project_id": in.ProjectID, "operation_id": in.OperationID, "prompt_digest": sha256Hex([]byte(in.Prompt))}}})
		rec.PromptJSON = string(b)
	}
	rec.Device, rec.Inode, err = j.queueIdentity()
	if err != nil {
		return MainStatus{}, err
	}
	room := 1
	if predecessor != "" {
		room++
	}
	if err = nativeJournalRoom(j.dir, room); err != nil {
		return MainStatus{}, err
	}
	if predecessor != "" {
		// No stage of the old intent can race this tombstone: Start takes this
		// same journal lock and checks it before any external observation/effect.
		if err = j.create(mainFile(predecessor, "superseded"), mainSupersession{1, predecessorDigest, in.OperationID, rec.Digest}); err != nil {
			return MainStatus{}, err
		}
	}
	if err = j.create(mainFile(in.OperationID, "intent"), rec); err != nil {
		return MainStatus{}, err
	}
	return inspectMainRecord(r, j.dir, in.ProjectID, in.OperationID, false)
}
