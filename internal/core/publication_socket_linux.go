package core

import (
	"encoding/json"
	"errors"
)

// Authenticated socket byte boundaries retain the existing publication fences,
// durable before/after capture claims, exact digests and one-attempt journals.
func PrepareResultPublicationBytes(dir string, t ResultPublicationTarget, raw []byte) (ResultPublicationStatus, error) {
	var p ResultPublicationPayload
	if !t.valid() {
		return ResultPublicationStatus{}, errors.New("invalid publication target")
	}
	if _, err := decodeStrictJSONObject(raw, composeLimit, &p); err != nil || !p.valid() || p.Target != t {
		return ResultPublicationStatus{}, errors.New("invalid publication bytes")
	}
	j, err := openLocalJournal(dir, t.journal())
	if err != nil {
		return ResultPublicationStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return ResultPublicationStatus{}, err
	}
	digest := sha256Hex([]byte(p.Manifest))
	status, _ := publicationLoad(j.dir, t, digest)
	if status.State != "none" {
		return status, errors.New("publication already retained; inspect only")
	}
	if err = prepareResultPublicationLocked(dir, j, p, raw, "publication exceeds bound"); err != nil {
		return status, err
	}
	// Do not reacquire this journal while holding its exclusive lock.
	reader, err := OpenReader(dir)
	if err != nil {
		return status, err
	}
	defer reader.Close()
	return publicationInspect(reader, j.dir, t, digest), nil
}

func ReadResultPublicationBytes(dir string, t ResultPublicationTarget, digest, payloadDigest string) (ResultPublicationStatus, string, error) {
	if !t.valid() || !isValidHexLen(digest, 64) || !isValidHexLen(payloadDigest, 64) {
		return ResultPublicationStatus{}, "", errors.New("invalid exact publication")
	}
	j, err := openLocalJournal(dir, t.journal())
	if err != nil {
		return ResultPublicationStatus{}, "", err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return ResultPublicationStatus{}, "", err
	}
	status, _ := publicationLoad(j.dir, t, digest)
	if status.State == "none" || status.State == "blocked" || status.PayloadDigest != payloadDigest {
		return status, "", errors.New("exact publication unavailable")
	}
	b, err := publicationRead(j.dir, digest+".intent.json")
	if err != nil {
		return status, "", err
	}
	var rec publicationRecord
	if err = json.Unmarshal(b, &rec); err != nil {
		return status, "", err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return status, "", err
	}
	defer r.Close()
	return publicationInspect(r, j.dir, t, digest), string(rec.Raw), nil
}

func PrepareCapturePublicationBytes(dir string, t ResultPublicationTarget, id string, raw []byte) (CapturePublicationStatus, error) {
	if !t.valid() || !isValidHexLen(id, 64) {
		return CapturePublicationStatus{}, errors.New("invalid capture identity")
	}
	var request CapturePreparationRequest
	if _, err := decodeStrictJSONObject(raw, composeLimit, &request); err != nil || request.Context.Target != t || request.IgnoredExclusions == nil {
		return CapturePublicationStatus{}, errors.New("invalid capture options")
	}
	j, err := openLocalJournal(dir, captureJournalName(t))
	if err != nil {
		return CapturePublicationStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return CapturePublicationStatus{}, err
	}
	status, _, _, _ := captureLoad(j.dir, t, id)
	if status.State != "none" {
		return status, errors.New("capture already started; inspect only")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return status, err
	}
	defer r.Close()
	if err = prepareCapturePublicationLocked(r, j, t, id, request); err != nil {
		return status, err
	}
	return captureInspect(r, j.dir, t, id), nil
}

func ReadCapturePublicationBytes(dir string, t ResultPublicationTarget, id, payloadDigest string) (CapturePublicationStatus, string, error) {
	if !t.valid() || !isValidHexLen(id, 64) || !isValidHexLen(payloadDigest, 64) {
		return CapturePublicationStatus{}, "", errors.New("invalid exact capture")
	}
	j, err := openLocalJournal(dir, captureJournalName(t))
	if err != nil {
		return CapturePublicationStatus{}, "", err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return CapturePublicationStatus{}, "", err
	}
	status, _, _, _ := captureLoad(j.dir, t, id)
	if status.State == "none" || status.State == "blocked" || status.PayloadDigest != payloadDigest {
		return status, "", errors.New("exact capture unavailable")
	}
	b, err := publicationRead(j.dir, id+".intent.json")
	if err != nil {
		return status, "", err
	}
	var record publicationRecord
	if err = json.Unmarshal(b, &record); err != nil {
		return status, "", err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return status, "", err
	}
	defer r.Close()
	return captureInspect(r, j.dir, t, id), string(record.Raw), nil
}
