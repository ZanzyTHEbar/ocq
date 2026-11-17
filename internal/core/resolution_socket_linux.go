package core

import (
	"encoding/json"
	"errors"
)

func PrepareCommandResolutionBytes(dir string, t CommandResolutionTarget, raw []byte) (CommandResolutionStatus, error) {
	initial, err := InspectCommandResolution(dir, t)
	if err != nil || initial.State != "none" {
		return initial, err
	}
	var p CommandResolutionPayload
	if _, err = decodeStrictJSONObject(raw, composeLimit, &p); err != nil || !p.valid() || p.Scope.CommandResolutionTarget != t {
		return initial, errors.New("invalid private resolution bytes")
	}
	j, err := openLocalJournal(dir, t.journal())
	if err != nil {
		return initial, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return initial, err
	}
	_, _, state, _ := resolutionRecord(j.dir, t)
	if state != "none" {
		return initial, errors.New("retained resolution exists; inspect only")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return initial, err
	}
	observation, err := resolutionObservation(r, t)
	err = errors.Join(err, r.Close())
	if err != nil {
		return initial, err
	}
	if !resolutionEligible(observation, p.Scope) {
		return initial, errors.New("exact unknown command observation changed")
	}
	record := commandResolutionRecord{1, t, sha256Hex(raw), raw}
	b, err := json.Marshal(record)
	if err != nil || len(b) > commandResolutionRecordLimit {
		return initial, errors.New("resolution exceeds bound")
	}
	if err = j.create("intent.json", record); err != nil {
		return initial, err
	}
	return InspectCommandResolution(dir, t)
}

func ReadCommandResolutionBytes(dir string, t CommandResolutionTarget, digest string) (CommandResolutionStatus, string, error) {
	if !t.valid() || !isValidHexLen(digest, 64) {
		return CommandResolutionStatus{}, "", errors.New("invalid exact resolution")
	}
	j, err := openLocalJournal(dir, t.journal())
	if err != nil {
		return CommandResolutionStatus{}, "", err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return CommandResolutionStatus{}, "", err
	}
	rec, _, state, _ := resolutionRecord(j.dir, t)
	if state == "none" || state == "blocked" || rec.Digest != digest {
		return CommandResolutionStatus{}, "", errors.New("exact retained resolution unavailable")
	}
	status, err := InspectCommandResolution(dir, t)
	return status, string(rec.Raw), err
}
