package core

import (
	"encoding/json"
	"errors"
)

func CommandSubmissionBytes(dir string, s CommandSubmissionScope, mode, id string, raw []byte) (CommandSubmissionStatus, json.RawMessage, error) {
	return commandAdmission.exchangeBytes(dir, s, mode, id, raw)
}
func AgentSubmissionBytes(dir string, s AgentSubmissionScope, mode, id string, raw []byte) (AgentSubmissionStatus, json.RawMessage, error) {
	return agentAdmission.exchangeBytes(dir, s, mode, id, raw)
}
func (k standaloneAdmission) exchangeBytes(dir string, s CommandSubmissionScope, mode, id string, raw []byte) (CommandSubmissionStatus, json.RawMessage, error) {
	if mode != "draft" && mode != "receive" && mode != "preview" && !(k == agentAdmission && mode == "import") {
		return CommandSubmissionStatus{}, nil, errors.New("unsupported admission exchange")
	}
	j, err := k.open(dir, s)
	if err != nil {
		return CommandSubmissionStatus{}, nil, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return CommandSubmissionStatus{}, nil, err
	}
	status, record := k.status(j, s)
	if mode == "preview" {
		if status.ID != id || !commandSubmissionPreviewable(status.State) {
			return status, nil, errors.New("exact retained admission required")
		}
		raw = record.Request
	} else {
		if status.State != "idle" || len(raw) == 0 || len(raw) > composeLimit {
			return status, nil, errors.New("admission blocked or exceeds bound")
		}
		if mode == "receive" {
			status, err = k.prepareLocked(j, s, raw)
			return status, nil, err
		}
		raw, err = k.draft(mode, s, raw)
		if err != nil {
			return status, nil, err
		}
		status.State = "draft"
	}
	review, err := k.review(raw)
	return status, json.RawMessage(review), err
}

// These entry points carry already authenticated, bounded private bytes. They
// retain the same journal identity, immutable intent, parser and review rules as
// the loopback transport; they never dispatch, arm or retry an action.
func PrepareLocalComposeBytes(dir string, raw []byte) (ComposeStatus, error) {
	if len(raw) == 0 || len(raw) > composeLimit || !validComposeRequest(raw) {
		return ComposeStatus{}, errors.New("invalid compose bytes")
	}
	j, err := openComposeJournal(dir)
	if err != nil {
		return ComposeStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return ComposeStatus{}, err
	}
	status, err := j.status()
	if err != nil || status.State != "idle" {
		return ComposeStatus{}, errors.New("unresolved compose intent")
	}
	dev, ino, err := j.queueIdentity()
	if err != nil {
		return ComposeStatus{}, err
	}
	id, err := composeRandom()
	if err != nil {
		return ComposeStatus{}, err
	}
	key, err := composeRandom()
	if err != nil {
		return ComposeStatus{}, err
	}
	record := composeRecord{Version: 1, ID: id, Key: key, Device: dev, Inode: ino, Digest: sha256Hex(raw), Request: raw}
	if err = j.create("intent.json", record); err != nil {
		return ComposeStatus{}, err
	}
	return ComposeStatus{State: "prepared", ID: id}, nil
}

func ReadLocalProjectIntakeReview(dir, id string) (ProjectIntakeStatus, string, []byte, error) {
	intent, err := ReadLocalProjectIntake(dir, id)
	if err != nil {
		return ProjectIntakeStatus{}, "", nil, err
	}
	if !intakePreviewable(intent.Status.State) {
		return ProjectIntakeStatus{}, "", nil, errors.New("intake not previewable")
	}
	raw, err := projectIntakeReview(intent.Kind, intent.Request)
	if err != nil || len(raw) > 8*composeLimit {
		return ProjectIntakeStatus{}, "", nil, errors.New("intake review unavailable")
	}
	return intent.Status, intent.Kind, raw, nil
}

func DraftProjectIntakeBytes(dir, kind string, input []byte) (string, []byte, error) {
	var raw []byte
	var err error
	previewKind := "submission"
	if kind == "registration" {
		previewKind = kind
		var draft ProjectRegistrationDraft
		if _, err = decodeStrictJSONObject(input, maxRegistrationBytes, &draft); err == nil {
			raw, err = BuildProjectRegistrationDraft(draft)
		}
	} else {
		reader, e := OpenReader(dir)
		if e != nil {
			return "", nil, e
		}
		defer reader.Close()
		switch kind {
		case "implementation":
			var draft ProjectImplementationDraft
			if _, err = decodeStrictJSONObject(input, maxEnvelopeBytes, &draft); err == nil {
				raw, err = reader.BuildProjectImplementationDraft(draft)
			}
		case "review":
			var draft ProjectReviewDraft
			if _, err = decodeStrictJSONObject(input, projectPayloadBytes, &draft); err == nil {
				raw, err = reader.BuildProjectReviewDraft(draft)
			}
		default:
			return "", nil, errors.New("unsupported intake draft")
		}
	}
	if err != nil {
		return "", nil, err
	}
	projection, err := projectIntakeReview(previewKind, raw)
	if err != nil || len(projection) > 8*composeLimit {
		return "", nil, errors.New("invalid intake draft projection")
	}
	return previewKind, projection, nil
}
