package policy

// NativeAcceptanceObservation describes an exact local action. The pure evaluator
// neither authenticates this observation nor changes canonical or private state.
type NativeAcceptanceObservation struct {
	ActorID, Action, TaskID, ActionDigest, PolicyRevision string
	Allowed, ApprovalRequired, Approved, Stopped          bool
}
type NativeAcceptanceAssessment struct {
	Verdict     Verdict
	Assurance   string
	Binding     NativeAcceptanceObservation
	Reasons     []Reason
	Obligations []Obligation
}

func EvaluateNativeAcceptance(in NativeAcceptanceObservation) NativeAcceptanceAssessment {
	out := NativeAcceptanceAssessment{Binding: in, Assurance: "observed-advisory", Verdict: Deny, Reasons: []Reason{}, Obligations: []Obligation{CurrentAuthorityConsumption, ExactProjectedPayload}}
	switch {
	case in.Action != "native-task-accept" || in.ActorID == "" || in.TaskID == "" || in.ActionDigest == "" || in.PolicyRevision == "":
		out.Reasons = append(out.Reasons, Reason{Code: InvalidRequest, Detail: "exact native action binding required"})
	case in.Stopped || !in.Allowed:
		out.Reasons = append(out.Reasons, Reason{Code: ReasonCode("native-policy-denied"), Detail: "owner policy denies native acceptance"})
	case in.ApprovalRequired && !in.Approved:
		out.Verdict = NeedsApproval
		out.Reasons = append(out.Reasons, Reason{Code: ReasonCode("native-approval-required"), Detail: "exact recorded owner approval required"})
	default:
		out.Verdict = Allow
		out.Reasons = append(out.Reasons, Reason{Code: ReasonCode("native-policy-allows"), Detail: "native acceptance policy allows exact local action"})
	}
	return out
}
