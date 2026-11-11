package core

func InspectAgentSubmission(d string, s AgentSubmissionScope) (AgentSubmissionStatus, error) {
	return agentAdmission.inspect(d, s)
}
func PrepareAgentSubmission(d string, s AgentSubmissionScope, b []byte) (AgentSubmissionStatus, error) {
	return agentAdmission.prepare(d, s, b)
}
func ArmAgentSubmission(d string, s AgentSubmissionScope, id string) (AgentSubmissionStatus, error) {
	return agentAdmission.mark(d, s, id, "arm")
}
func CancelAgentSubmission(d string, s AgentSubmissionScope, id string) (AgentSubmissionStatus, error) {
	return agentAdmission.mark(d, s, id, "cancel")
}
func AttemptAgentSubmission(d string, s AgentSubmissionScope, id string) (AgentSubmissionStatus, error) {
	return agentAdmission.attempt(d, s, id)
}
func ReconcileAgentSubmission(d string, s AgentSubmissionScope, id string) (AgentSubmissionStatus, error) {
	return agentAdmission.reconcile(d, s, id)
}
func RetireAgentSubmission(d string, s AgentSubmissionScope, id string) (AgentSubmissionStatus, error) {
	return agentAdmission.retire(d, s, id)
}
func AgentSubmissionRendezvous(d string, s AgentSubmissionScope, l string) (ComposeRendezvous, error) {
	return agentAdmission.rendezvous(d, s, l)
}
func ExchangeAgentSubmission(d string, s AgentSubmissionScope, m, id, l string) (AgentSubmissionStatus, error) {
	return agentAdmission.exchange(d, s, m, id, l)
}

// These switches are fixed concrete adapters, not registered callbacks or an
// operation chosen by a payload. Command routes NEVER accept v2 agent bytes.
func (k standaloneAdmission) parse(raw []byte) (preparedCommandSubmission, error) {
	if k == agentAdmission {
		p, e := parsePreparedAgentSubmission(raw)
		return preparedCommandSubmission{Scope: p.Scope, Key: p.Key, Canonical: p.Canonical}, e
	}
	return parsePreparedCommandSubmission(raw)
}
func (k standaloneAdmission) review(raw []byte) ([]byte, error) {
	if k == agentAdmission {
		return agentSubmissionReview(raw)
	}
	return commandSubmissionReview(raw)
}
func (k standaloneAdmission) receipt(r *Reader, p preparedCommandSubmission) (QueueJobOutput, error) {
	if k == agentAdmission {
		return r.AgentSubmissionReceipt(p.Key, []byte(p.Canonical))
	}
	return r.CommandSubmissionReceipt(p.Key, []byte(p.Canonical))
}
func (k standaloneAdmission) draft(mode string, s CommandSubmissionScope, raw []byte) ([]byte, error) {
	if k == agentAdmission {
		if mode == "import" {
			return ImportAgentSubmissionDraft(s, raw)
		}
		var d AgentSubmissionDraft
		if _, e := decodeStrictJSONObject(raw, composeLimit, &d); e != nil {
			return nil, e
		}
		return BuildAgentSubmissionDraft(s, d)
	}
	var d CommandSubmissionDraft
	if _, e := decodeStrictJSONObject(raw, composeLimit, &d); e != nil {
		return nil, e
	}
	return BuildCommandSubmissionDraft(s, d)
}
