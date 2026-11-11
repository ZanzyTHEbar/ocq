package core

// Private review projection, sent ONLY over authenticated retained preview.
// Never put this object on argv, standard stdout/status or ordinary stored views.
// Original embedded bytes come from the actual parser, not re-marshaled structs.
type intakeReviewPart struct {
	Raw    string `json:"raw"`
	Digest string `json:"sha256"`
}
type intakeReview struct {
	Raw        string             `json:"raw"`
	Digest     string             `json:"sha256"`
	Request    *intakeReviewPart  `json:"request,omitempty"`
	Brief      *intakeReviewPart  `json:"brief,omitempty"`
	Handoff    *intakeReviewPart  `json:"handoff,omitempty"`
	References []intakeReviewPart `json:"references,omitempty"`
}

func projectIntakeReview(kind string, raw []byte) ([]byte, error) {
	out := intakeReview{Raw: string(raw), Digest: sha256Hex(raw)}
	if kind == "registration" {
		_, handoff, _, err := parseRegistrationPayload(raw)
		if err != nil {
			return nil, err
		}
		out.Handoff = &intakeReviewPart{string(handoff.Canonical), handoff.Digest}
	}
	if kind == "submission" {
		parsed, err := parseSubmissionEnvelope(raw)
		if err != nil {
			return nil, err
		}
		out.Request = &intakeReviewPart{string(parsed.RequestBytes), parsed.RequestDigest}
		out.Brief = &intakeReviewPart{string(parsed.BriefManifest.Canonical), parsed.BriefManifest.Digest}
		out.References = []intakeReviewPart{}
		for _, ref := range parsed.ReferencedManifests {
			out.References = append(out.References, intakeReviewPart{string(ref.Canonical), ref.Digest})
		}
	}
	return marshalCanonicalManifestJSON(out)
}
