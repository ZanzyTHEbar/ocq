package core

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func clientFile(pointer, suffix string) string { return ".client-" + pointer + "." + suffix + ".json" }
func clientDigest(p ClientPointer) string {
	p.PointerSHA256 = ""
	p.LauncherArgv = append([]string(nil), p.LauncherArgv...)
	if len(p.LauncherArgv) > 0 {
		p.LauncherArgv[len(p.LauncherArgv)-1] = ""
	}
	return nativeHash(p)
}

func clientTarget(r *Reader, j *os.File, in ClientPrepareRequest) (ClientPointer, error) {
	p := ClientPointer{Protocol: ClientProtocol, ProjectID: in.ProjectID, NavigationID: in.NavigationID, WorkRequestID: in.WorkRequestID, TargetKind: in.TargetKind, Space: in.Space}
	binding, err := workBinding(r, in.ProjectID)
	if err != nil {
		return p, err
	}
	p.BindingRevision = strconv.Itoa(binding.Revision)
	p.BindingSHA256 = nativeHash(binding)
	p.QueueIdentity, p.StoreContext, err = r.WorkQueueContext()
	if err != nil {
		return p, err
	}
	var expectedProfile string
	if in.TargetKind == "worker" {
		status, e := workInspect(r, j, in.ProjectID, in.WorkRequestID)
		if e != nil {
			return p, e
		}
		if status.Receipt == nil || status.Job == nil || status.Kind != "implementation" && status.Kind != "review" {
			return p, errors.New("exact admitted implementation/review worker required")
		}
		job, e := r.store.get(status.Job.JobID)
		if e != nil {
			return p, e
		}
		if job.Agent == nil || job.agentRequest == nil || job.Agent.SessionID == "" || job.Agent.Worktree == "" {
			return p, errors.New("worker session/worktree not yet owned")
		}
		p.JobID, p.Sequence, p.SessionID, p.Cwd, p.OpenCodeProjectID = job.ID, strconv.FormatInt(job.Sequence, 10), job.Agent.SessionID, job.Agent.Worktree, job.Agent.ProjectID
		p.OwnershipSHA256 = nativeHash(ownershipMetadata(job))
		// The entire immutable worker profile, not a typed subset of live fields.
		b, _ := json.Marshal(map[string]any{"agent": job.agentRequest.Agent, "model": job.agentRequest.Model, "permissions": job.agentRequest.Permissions})
		var profile map[string]any
		_ = json.Unmarshal(b, &profile)
		expectedProfile = nativeHash(profile)
	} else {
		setup, _, _, e := selectedMain(r, j, in.ProjectID)
		if e != nil {
			return p, e
		}
		common, _, _, e := inspectCanonicalMain(setup.Repo, setup.Branch)
		if e != nil || common != setup.Common {
			return p, errors.New("configured canonical MAIN checkout/branch unavailable")
		}
		p.SetupID, p.OwnershipMode, p.SessionID, p.Cwd, p.OpenCodeProjectID = setup.SetupID, setup.OwnershipMode, setup.SessionID, setup.Repo, setup.ProjectID
		p.OwnershipSHA256 = nativeHash(json.RawMessage(setup.Metadata))
	}
	service, err := discoverNativeService(p.Cwd)
	if err != nil {
		return p, err
	}
	p.ServerOrigin, p.ServiceIdentity = service.Origin, service.Identity
	s, err := nativeSession(p.Cwd, service, p.SessionID)
	if err != nil {
		return p, err
	}
	if err = nativeIdentity(s, p.SessionID, p.Cwd, p.OpenCodeProjectID, p.OwnershipSHA256); err != nil {
		return p, err
	}
	p.ProfileSHA256 = nativeProfile(s)
	if expectedProfile != "" && p.ProfileSHA256 != expectedProfile {
		return p, errors.New("worker immutable execution profile changed; navigation refused")
	}
	p.Options = ClientOptions{Protocol: "ocq-handoff-v1", TargetKind: p.TargetKind, ProjectID: p.ProjectID, MainID: p.SetupID, OwnershipMode: p.OwnershipMode, QueueIdentity: p.QueueIdentity, JobID: p.JobID, SessionID: p.SessionID, Worktree: p.Cwd, OpenCodeProjectID: p.OpenCodeProjectID, OwnershipSHA256: p.OwnershipSHA256, ProfileSHA256: p.ProfileSHA256}
	return p, nil
}

func PrepareProjectClient(dir string, raw []byte) (ClientPointer, error) {
	var in ClientPrepareRequest
	if _, err := decodeStrictJSONObject(raw, 16<<10, &in); err != nil {
		return ClientPointer{}, err
	}
	if in.Protocol != ClientProtocol || !isValidProjectID(in.ProjectID) || !workID(in.NavigationID) || in.Space == "" || len(in.Space) > 256 || strings.ContainsAny(in.Space, "\r\n\x00") || (in.TargetKind != "worker" && in.TargetKind != "integrator") || in.TargetKind == "worker" && !workID(in.WorkRequestID) || in.TargetKind == "integrator" && in.WorkRequestID != "" {
		return ClientPointer{}, errors.New("invalid exact native client target")
	}
	j, err := openLocalJournal(dir, workJournal(in.ProjectID))
	if err != nil {
		return ClientPointer{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return ClientPointer{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return ClientPointer{}, err
	}
	defer r.Close()
	name := clientFile("nav-"+in.NavigationID, "intent")
	if b, e := workRead(j.dir, name); e == nil {
		var saved struct {
			Input   string `json:"input"`
			Pointer string `json:"pointer"`
		}
		if json.Unmarshal(b, &saved) != nil || saved.Input != string(raw) {
			return ClientPointer{}, errors.New("navigation ID already binds different bytes")
		}
		return loadClientPointer(r, j.dir, in.ProjectID, saved.Pointer)
	} else if !errors.Is(e, syscall.ENOENT) {
		return ClientPointer{}, e
	}
	if err = nativeJournalRoom(j.dir, 2); err != nil {
		return ClientPointer{}, err
	}
	p, err := clientTarget(r, j.dir, in)
	if err != nil {
		return p, err
	}
	images, err := nativeReleaseImages()
	if err != nil {
		return p, err
	}
	p.LauncherSHA256, p.OpenCodeSHA256, p.AssetSHA256 = images.LauncherSHA, images.OpenCodeSHA, images.AssetSHA
	p.LauncherArgv = []string{images.Launcher, "--state-dir", r.store.dir, "project", "client", "run", in.ProjectID, ""}
	config, err := nativeConfigDir()
	if err != nil {
		return p, err
	}
	overlay, err := BuildNativeClientOverlay(config, os.Getenv("OPENCODE_CLI_CONFIG_CONTENT"), images.Asset, p.Options)
	if err != nil {
		return p, err
	}
	p.OverlaySHA256 = sha256Hex([]byte(overlay))
	p.PointerSHA256 = clientDigest(p)
	p.LauncherArgv[len(p.LauncherArgv)-1] = p.PointerSHA256
	if err = workOutputBound(p); err != nil {
		return p, err
	}
	// A failed private preparation has no HC/target effect. Preallocating the
	// caller's navigation ID still prevents silently selecting a newer target.
	if err = j.create(name, struct {
		Input   string `json:"input"`
		Pointer string `json:"pointer"`
	}{string(raw), p.PointerSHA256}); err != nil {
		return p, err
	}
	if err = j.create(clientFile(p.PointerSHA256, "pointer"), p); err != nil {
		return p, err
	}
	return p, nil
}

func loadClientPointer(r *Reader, j *os.File, project, digest string) (ClientPointer, error) {
	var p ClientPointer
	if !isValidProjectID(project) || !isValidHexLen(digest, 64) {
		return p, errors.New("invalid native client pointer")
	}
	b, err := workRead(j, clientFile(digest, "pointer"))
	if err != nil {
		return p, err
	}
	if _, err = decodeStrictJSONObject(b, WorkResponseBytes, &p); err != nil {
		return p, err
	}
	if p.Protocol != ClientProtocol || p.ProjectID != project || p.PointerSHA256 != digest || clientDigest(p) != digest || len(p.LauncherArgv) != 8 || p.LauncherArgv[2] != r.store.dir || p.LauncherArgv[7] != digest {
		return p, errors.New("native pointer identity/content mismatch")
	}
	queue, store, err := r.WorkQueueContext()
	if err != nil {
		return p, err
	}
	if queue != p.QueueIdentity || store != p.StoreContext {
		return p, errors.New("native pointer queue/store changed")
	}
	return p, nil
}

func verifyClientPointer(r *Reader, j *os.File, p ClientPointer) (nativeImages, string, error) {
	in := ClientPrepareRequest{Protocol: ClientProtocol, ProjectID: p.ProjectID, NavigationID: p.NavigationID, TargetKind: p.TargetKind, WorkRequestID: p.WorkRequestID, Space: p.Space}
	current, err := clientTarget(r, j, in)
	if err != nil {
		return nativeImages{}, "", err
	}
	current.LauncherArgv = p.LauncherArgv
	current.LauncherSHA256 = p.LauncherSHA256
	current.OpenCodeSHA256 = p.OpenCodeSHA256
	current.AssetSHA256 = p.AssetSHA256
	current.OverlaySHA256 = p.OverlaySHA256
	current.PointerSHA256 = p.PointerSHA256
	if !jsonEqual(current, p) {
		return nativeImages{}, "", errors.New("native target/binding/current profile/service changed; prepare a distinct explicit navigation")
	}
	images, err := nativeReleaseImages()
	if err != nil {
		return images, "", err
	}
	if images.Launcher != p.LauncherArgv[0] || images.LauncherSHA != p.LauncherSHA256 || images.OpenCodeSHA != p.OpenCodeSHA256 || images.AssetSHA != p.AssetSHA256 {
		return images, "", errors.New("native release/client/asset image changed")
	}
	config, err := nativeConfigDir()
	if err != nil {
		return images, "", err
	}
	overlay, err := BuildNativeClientOverlay(config, os.Getenv("OPENCODE_CLI_CONFIG_CONTENT"), images.Asset, p.Options)
	if err != nil {
		return images, "", err
	}
	if sha256Hex([]byte(overlay)) != p.OverlaySHA256 {
		return images, "", errors.New("effective CLI plugin configuration changed; no plugins dropped")
	}
	return images, overlay, nil
}

func RunProjectClient(dir, project, digest string) error {
	if !isValidProjectID(project) || !isValidHexLen(digest, 64) {
		return errors.New("invalid native client pointer")
	}
	owned, err := openLocalJournal(dir, workJournal(project))
	if err != nil {
		return err
	}
	if err = owned.acquire(); err != nil {
		owned.close()
		return err
	}
	defer func() {
		if owned != nil {
			owned.close()
		}
	}()
	r, err := OpenReader(dir)
	if err != nil {
		return err
	}
	j, err := workReadDir(r, project)
	if err != nil {
		r.Close()
		return err
	}
	p, err := loadClientPointer(r, j, project, digest)
	if err != nil {
		j.Close()
		r.Close()
		return err
	}
	// Launcher can only be consumed after native terminal admission was journaled.
	status, e := inspectClient(r, j, project, digest)
	if e != nil || status.State != "attempted_unknown" && status.State != "terminal_recorded" {
		j.Close()
		r.Close()
		return errors.New("native client has no valid durable HC attempt")
	}
	if _, e := workRead(j, clientFile(digest, "launch.attempt")); !errors.Is(e, syscall.ENOENT) {
		j.Close()
		r.Close()
		return errors.New("native TUI launch already attempted/unknown; never repeat")
	}
	images, overlay, err := verifyClientPointer(r, j, p)
	j.Close()
	r.Close()
	if err != nil {
		return err
	}
	if err = nativeJournalRoom(owned.dir, 1); err != nil {
		return err
	}
	if err = owned.create(clientFile(digest, "launch.attempt"), publicationMarker{Version: 1, Digest: digest}); err != nil {
		return err
	}
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "OPENCODE_CLI_CONFIG_CONTENT=") {
			env = append(env, entry)
		}
	}
	env = append(env, "OPENCODE_CLI_CONFIG_CONTENT="+overlay)
	if err = os.Chdir(p.Cwd); err != nil {
		return err
	}
	owned.close()
	// Do not hold intake locks for the lifetime of the ordinary native client.
	owned = nil
	// --server suppresses 2.3.0's terminal ENV capture. No target/session flags:
	// verified-existing navigation is solely the trusted TUI router extension.
	return syscall.Exec(images.OpenCode, []string{images.OpenCode, "--server", p.ServerOrigin}, env)
}
