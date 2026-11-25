package core

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const MainRoleProtocol = "ocq-main-role-v1"
const mainRoleKey = "ocq-main-role-v1"

// Exact user-selected role mapping, not names guessed from Elephant/Guppy labels.
type MainRoleSelection struct {
	Protocol              string `json:"protocol"`
	ImplementationProfile string `json:"implementation_profile"`
	ReviewProfile         string `json:"review_profile"`
}

// Retained and previewed before any instruction mutation. No ownership or auth
// token. State and executable pointers are local-only, never tracked defaults.
type MainRoleContract struct {
	Protocol       string            `json:"protocol"`
	Key            string            `json:"key"`
	ProjectID      string            `json:"project_id"`
	SetupID        string            `json:"setup_id"`
	SessionID      string            `json:"session_id"`
	StateDir       string            `json:"state_dir"`
	QueueIdentity  string            `json:"queue_identity"`
	StoreContext   string            `json:"store_context"`
	CLI            string            `json:"cli"`
	CLISHA256      string            `json:"cli_sha256"`
	Repo           string            `json:"repo"`
	GitCommonDir   string            `json:"git_common_dir"`
	MainBranch     string            `json:"main_branch"`
	Selection      MainRoleSelection `json:"selection"`
	Instructions   string            `json:"instructions"`
	SHA256         string            `json:"sha256"`
	PreviousSHA256 string            `json:"previous_sha256,omitempty"`
}

func parseMainRole(raw string) (MainRoleSelection, error) {
	var selection MainRoleSelection
	_, err := decodeStrictJSONObject([]byte(raw), 48<<10, &selection)
	if err != nil {
		return selection, err
	}
	if selection.Protocol != MainRoleProtocol {
		return selection, errors.New("explicit ocq-main-role-v1 selection required")
	}
	for _, profile := range []string{selection.ImplementationProfile, selection.ReviewProfile} {
		if _, err = workProfile(profile); err != nil {
			return selection, err
		}
	}
	return selection, nil
}

func prepareMainRole(r *Reader, rec mainRecord, project, raw string) (MainRoleContract, error) {
	selection, err := parseMainRole(raw)
	if err != nil {
		return MainRoleContract{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return MainRoleContract{}, err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return MainRoleContract{}, err
	}
	switch filepath.Base(exe) {
	case "ocq":
	case "ocq-herdr":
		dir := filepath.Dir(exe)
		exe, err = filepath.EvalSymlinks(filepath.Join(dir, "ocq"))
		if err != nil || filepath.Dir(exe) != dir || filepath.Base(exe) != "ocq" {
			return MainRoleContract{}, errors.New("matched canonical CLI sibling required for Main role")
		}
	default:
		return MainRoleContract{}, errors.New("Main role preparation requires the actual ocq CLI or matched bridge executable")
	}
	_, _, image, err := dispatcherImage(exe)
	if err != nil {
		return MainRoleContract{}, err
	}
	queue, store, err := r.WorkQueueContext()
	if err != nil {
		return MainRoleContract{}, err
	}
	role := MainRoleContract{Protocol: MainRoleProtocol, Key: mainRoleKey, ProjectID: project, SetupID: rec.SetupID, SessionID: rec.SessionID, StateDir: r.store.dir, QueueIdentity: queue, StoreContext: store, CLI: exe, CLISHA256: image, Repo: rec.Repo, GitCommonDir: rec.Common, MainBranch: rec.Branch, Selection: selection, Instructions: mainOperatingInstructions}
	role.SHA256 = mainRoleDigest(role)
	return role, nil
}

func mainRoleDigest(role MainRoleContract) string { role.SHA256 = ""; return nativeHash(role) }

const mainOperatingInstructions = `Disclosed persistent Main / Integrator operating contract, ocq-main-role-v1.
You are the multi-turn integrator for this ONE product project, not a queued worker or capacity owner.
Use the exact state_dir, CLI image/path, project_id, canonical repo/common-dir/main_branch and selected role profiles in this entry. Verify these pointers before effects; changed/missing executable or queue identity is a blocker, not permission to guess alternatives. Existing project registration/CAS is the sole binding authority; get its current revision with project show. Read project main role PROJECT for the retained contract. Never invent Elephant/Guppy agent/model IDs. Selection.implementation_profile and selection.review_profile are exact WorkProfile JSON text; save each to a private local file when supplying --profile-file, never put permissions/briefs/prompts in argv.

Preserve dirty user work. Canonical primary MAIN must still own main_branch with the same common-dir. Never switch/reset/stash, make an alternate Main checkout, or adopt by title/branch/latest. Anchor advancement requires explicit project main refresh preparation/preview/CAS, not an implicit rebind.

Installed, advertised and permitted capabilities are different. Before declaring a requested skill or agent unavailable, inspect the effective read-only /api/skill and /api/agent catalogs at this exact repo Location on the existing service. A skill with autoinvoke=false is hidden from the advertised list but can be loaded explicitly by its registered ID when the user requests it. An architecture agent is not necessarily an architecture skill. If inspection is unavailable, report discovery as unverified, not missing. Never install, change shared configuration, substitute the selected role profiles, or invoke hidden/untracked subagents to work around a capability boundary. OCQ trusts configured cooperative foreground tools, native catalog search and awaited Code Mode/MCP RPC completion under its versioned harness lifecycle contract; it does not maintain a second ordinary-tool permission list. Concrete background shell/job/task/PTY/delegation resources require independent terminal observation, not merely an outer tool return. Static review still permits only read/glob/grep.

Keep ONE user goal alive through the following ordinary agent loop. Use only OCQ admission for bounded implementation/review assignments; no hidden direct subagent or unqueued execution. Choose and retain exact new request IDs; do not reuse unknown attempts.
1. Write the bounded implementation brief privately. Use project work prepare PROJECT --request-id ID --binding-revision REV --profile-file FILE --brief-file FILE. If the goal requires a feature/milestone base, supply --base-ref REF_OR_COMMIT; preparation resolves it once in this repo and freezes the full commit in the retained payload and brief. Omission uses the Main binding anchor. Main remains on main_branch; never move/rebind it to select a worker base. Review the requested ref AND frozen worker commit in project work preview PROJECT ID, then project work confirm PROJECT ID --preview-sha256 DIGEST only within the user's goal authority. Reviewers inherit the exact captured implementation base, not a newly resolved branch tip.
2. Await project work wait PROJECT ID --timeout-seconds 20. pending is NOT completion: renew this same read-only wait in the ordinary tool loop without requiring another human prompt. It installs the existing observation lease with watch-before-check and expiry revalidation; no dispatch/API mutation. attention/unknown ownership/permission/form/blocker requires human attention, never approval replies, retry, adoption or capacity release.
3. Only terminal execution proof permits project work capture-prepare PROJECT --request-id CAPTURE --binding-revision REV --implementation ID; preview/confirm its exact retained machine snapshot. Admission, idle, shell/client exit, prose success or snapshot existence alone is not completion proof.
4. Prepare a fresh independent review against that EXACT confirmed capture using project work review-prepare PROJECT --request-id REVIEW --binding-revision REV --capture CAPTURE --profile-file REVIEW_FILE --criteria-file FILE --rules-file FILE. Criteria/rules must concern the supplied inline diff/contents, not queue storage, capture authenticity/digest verification or live canonical state; those are machine checks. The reviewer checkout is pristine baseline/context, not the candidate filesystem. Do not add a report schema: Work supplies the canonical strict JSON contract (verdict approved|needs_revision|unable_to_review, findings array, nonempty limitations string). Use the supported bounded static-review policy; no worker profile/ownership/resource-fence relaxation. Preview/confirm, then renew the exact review wait until terminal proof or genuine attention.
5. Read project work output PROJECT REVIEW. Use project work review-result-prepare PROJECT --request-id RESULT --binding-revision REV --review REVIEW; with no result-file it uses the exact strict terminal report, never parses prose approval. Preview/confirm the linked result, then project work handoff PROJECT RESULT. Preserve exact IDs, manifests, immutable association/snapshot lineage and limitations throughout. No latest-result lookup or implicit resubmission.
6. Deliver the usable handoff under this same user goal. Distinguish execution proof, tests/validation, reviewer verdict, snapshot drift and readiness. integration_authorized remains false: approved/ready_for_integrator_review is NOT permission to merge/integrate. Act on MAIN only under separate standing human integration authority, preserving its preconditions and dirty work. No commit/push/deploy merely because a review approved.

Native human permissions remain human-only. Never auto-answer permissions/forms, bypass blocks, retry an ambiguous POST, release unknown capacity, or treat auxiliary/system/synthetic events as causal execution proof. This instruction entry is ordinary disclosed context, not a fake producer/system event or permission override. Human Main turns/profile changes are legitimate; bounded workers still require one exact prompt, dedicated fresh session/worktree, stable full transcript, explicit terminal outcome, no active loop/pending inputs/resources and conservative unknown ownership.

Private orchestration briefs, profiles and criteria are control-plane inputs, not product edits. Author them only through tools permitted by your CURRENT effective agent and policy. This contract grants no write permission and does not override a Coordinator no-write boundary. Never use shell, another interpreter or an admission API to evade a denied write. Report a real policy conflict rather than asking the user to supply an internal file path as though it were a business requirement.
Before reporting an earlier blocked handoff as current, read the exact goal-linked capture, review-result and project inbox non-consumingly. A later recorded result may resolve an earlier preparation failure; retain both histories. Review approval does not authorize integration or a different business scope.
`

func mainRolePath(rec mainRecord) string {
	return "/api/experimental/session/" + url.PathEscape(rec.SessionID) + "/instructions/entries"
}

func mainRoleEntryKey(role MainRoleContract) string {
	return role.Key
}

func observeMainRole(rec mainRecord) (bool, bool, error) {
	if rec.Role.Protocol != MainRoleProtocol || rec.Role.SHA256 != mainRoleDigest(rec.Role) {
		return false, false, errors.New("retained Main role missing or corrupt; explicit setup required")
	}
	seen, present, err := readMainRoleEntry(rec)
	if err != nil || !present {
		return false, present, err
	}
	if !jsonEqual(seen, rec.Role) {
		return false, true, errors.New("Main role entry changed; explicit versioned refresh required, never implicit overwrite")
	}
	return true, true, nil
}

// There is one active API context key. Immutable previous values live in OCQ's
// intent journal, not as competing active instruction sources in OpenCode.
func readMainRoleEntry(rec mainRecord) (MainRoleContract, bool, error) {
	var seen MainRoleContract
	b, err := nativeCall(rec.Repo, rec.Server, "get", mainRolePath(rec), nil)
	if err != nil {
		return seen, false, err
	}
	var entries []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	if err = decodeData(b, &entries); err != nil {
		return seen, false, err
	}
	present := false
	for _, entry := range entries {
		if entry.Key == mainRoleEntryKey(rec.Role) {
			if present {
				return seen, true, errors.New("duplicate active Main instruction key")
			}
			present = true
			if _, err := decodeStrictJSONObject(entry.Value, WorkResponseBytes, &seen); err != nil ||
				seen.Protocol != MainRoleProtocol || seen.Key != mainRoleKey || seen.SHA256 != mainRoleDigest(seen) {
				return seen, true, errors.New("active Main instruction entry corrupt")
			}
		} else {
			var header struct {
				Protocol string `json:"protocol"`
			}
			if json.Unmarshal(entry.Value, &header) == nil && header.Protocol == MainRoleProtocol {
				return seen, present, errors.New("competing active Main contracts; explicit operator review required, no automatic removal")
			}
		}
	}
	return seen, present, nil
}

func ensureMainRole(j *composeJournal, rec mainRecord, id string, claim workClaim) error {
	_, _, image, err := dispatcherImage(rec.Role.CLI)
	if err != nil || image != rec.Role.CLISHA256 {
		return errors.New("retained Main CLI image changed/unavailable; no instructions mutation")
	}
	matches, present, err := observeMainRole(rec)
	if err != nil {
		return err
	}
	if matches {
		return retainMainRoleObservation(j, rec, id)
	}
	if present {
		return errors.New("Main role conflict")
	}
	fresh, err := mainStage(j, id, "role", claim)
	if err != nil {
		return err
	}
	if !fresh {
		return errors.New("Main role attempt unknown; exact GET recovery only, never repeat PUT")
	}
	_, putErr := nativeCall(rec.Repo, rec.Server, "put", mainRolePath(rec)+"/"+url.PathEscape(mainRoleEntryKey(rec.Role)), map[string]any{"value": rec.Role})
	matches, _, readErr := observeMainRole(rec)
	if matches && readErr == nil {
		return retainMainRoleObservation(j, rec, id)
	}
	return errors.Join(putErr, readErr, errors.New("Main role read-back not proven; no registration/prompt"))
}

func retainMainRoleObservation(j *composeJournal, rec mainRecord, id string) error {
	name := mainFile(id, "role.observed")
	marker := publicationMarker{Version: 1, Digest: rec.Role.SHA256}
	if b, err := workRead(j.dir, name); err == nil {
		var old publicationMarker
		if json.Unmarshal(b, &old) != nil || old != marker {
			return errors.New("retained Main role observation differs")
		}
		return j.syncFile(name)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := nativeJournalRoom(j.dir, 1); err != nil {
		return err
	}
	return j.create(name, marker)
}

func ProjectMainRole(dir, project string) (MainRoleContract, error) {
	if !isValidProjectID(project) {
		return MainRoleContract{}, errors.New("invalid Main project")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return MainRoleContract{}, err
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if err != nil {
		return MainRoleContract{}, err
	}
	defer j.Close()
	rec, _, _, err := selectedMain(r, j, project)
	if err != nil {
		return MainRoleContract{}, err
	}
	if rec.Role.Protocol != MainRoleProtocol || rec.Role.SHA256 != mainRoleDigest(rec.Role) || strings.TrimSpace(rec.Role.Instructions) == "" {
		return MainRoleContract{}, errors.New("retained Main role missing/corrupt")
	}
	return rec.Role, nil
}
