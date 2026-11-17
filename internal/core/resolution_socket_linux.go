package core

import "errors"

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
	if err = prepareCommandResolutionLocked(dir, j, t, p, raw, "resolution exceeds bound"); err != nil {
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
