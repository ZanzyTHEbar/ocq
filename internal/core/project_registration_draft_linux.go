package core

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/ZanzyTHEbar/faults-go"
)

// DraftLocalProjectRegistration exchanges private bounded form input/output.
// Only rendezvous metadata and a payload-free result leave this transport. The
// journal lock serializes transport use but no intent, claim or attempt is made.
func DraftLocalProjectRegistration(stateDir, launch string) (ProjectIntakeStatus, error) {
	return draftLocalProjectIntake(stateDir, "registration", launch)
}

func DraftLocalProjectImplementation(stateDir, launch string) (ProjectIntakeStatus, error) {
	return draftLocalProjectIntake(stateDir, "implementation", launch)
}

func DraftLocalProjectReview(stateDir, launch string) (ProjectIntakeStatus, error) {
	return draftLocalProjectIntake(stateDir, "review", launch)
}

// The three concrete draft routes share only bounded private framing; no
// dispatch, journal mutation or arbitrary operation is selected by the peer.
func draftLocalProjectIntake(stateDir, kind, launch string) (ProjectIntakeStatus, error) {
	if !isValidHexLen(launch, 64) {
		return ProjectIntakeStatus{}, faults.New(faults.CodeValidationFailed, "invalid registration draft launch")
	}
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ProjectIntakeStatus{}, err
	}
	err = j.withComposeConnection(launch, func(conn net.Conn) error {
		// ASCII length permits native UTF-8 sockets, with a registration-sized
		// bound before allocation. Reject noncanonical sizes and excess digits.
		limit := maxRegistrationBytes
		if kind != "registration" {
			limit = maxEnvelopeBytes
		}
		digits := ""
		var b [1]byte
		for {
			if _, err := io.ReadFull(conn, b[:]); err != nil {
				return err
			}
			if b[0] == '\n' {
				break
			}
			if b[0] < '0' || b[0] > '9' || len(digits) >= 6 {
				return fmt.Errorf("invalid draft frame")
			}
			digits += string(b[:])
		}
		n, err := strconv.Atoi(digits)
		if err != nil || strconv.Itoa(n) != digits || n < 1 || n > limit {
			return fmt.Errorf("invalid draft size")
		}
		input := make([]byte, n)
		if _, err := io.ReadFull(conn, input); err != nil {
			return err
		}
		var raw []byte
		if kind == "registration" {
			var draft ProjectRegistrationDraft
			if _, err = decodeStrictJSONObject(input, limit, &draft); err != nil {
				return fmt.Errorf("invalid draft fields")
			}
			raw, err = BuildProjectRegistrationDraft(draft)
		} else {
			reader, e := OpenReader(stateDir)
			if e != nil {
				return e
			}
			defer reader.Close()
			if kind == "implementation" {
				var draft ProjectImplementationDraft
				if _, e := decodeStrictJSONObject(input, limit, &draft); e != nil {
					return fmt.Errorf("invalid implementation fields")
				}
				raw, err = reader.BuildProjectImplementationDraft(draft)
			} else {
				var draft ProjectReviewDraft
				if _, e := decodeStrictJSONObject(input, limit, &draft); e != nil {
					return fmt.Errorf("invalid review fields")
				}
				raw, err = reader.BuildProjectReviewDraft(draft)
			}
		}
		if err != nil {
			return fmt.Errorf("invalid registration draft")
		}
		previewKind := "registration"
		if kind != "registration" {
			previewKind = "submission"
		}
		projection, err := projectIntakeReview(previewKind, raw)
		if err != nil || len(projection) > 8*composeLimit {
			return fmt.Errorf("invalid draft preview")
		}
		var header [5]byte
		header[0] = 'R'
		if kind != "registration" {
			header[0] = 'S'
		}
		binary.BigEndian.PutUint32(header[1:], uint32(len(projection)))
		if _, err := conn.Write(header[:]); err != nil {
			return err
		}
		_, err = conn.Write(projection)
		return err
	})
	if err != nil {
		return ProjectIntakeStatus{}, faults.New(faults.CodeValidationFailed, "project draft unavailable; check required fields, exact binding/target and size")
	}
	return ProjectIntakeStatus{State: "draft"}, nil
}
