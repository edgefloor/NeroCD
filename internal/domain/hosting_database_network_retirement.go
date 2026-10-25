package domain

import "slices"

// HostingDatabaseNetworkRetirementProof is transient retained evidence of one
// exact endpoint's attachment, durable retirement intent and observed absence.
// It never authorizes another Docker effect or replaces current authorization.
type HostingDatabaseNetworkRetirementProof struct {
	Attached     HostingDatabaseNetworkMemberProof
	RetirePlan   HostingDatabaseBindingEndpointPlan
	RetireIntent HostingDatabaseBindingEndpointReceipt
	Detached     HostingDatabaseBindingEndpointReceipt
}

func databaseNetworkAttachedMember(proof HostingDatabaseNetworkMemberProof) (HostingDatabaseNetworkMember, error) {
	b, ep, app := proof.Binding, proof.EndpointPlan, proof.ApplicationHealthy
	if ValidateHostingDatabasePlan(proof.DatabasePlan) != nil || b.ValidateTargets(proof.ApplicationProjectID, app.Snapshot, proof.DatabaseProjectID, proof.DatabasePlan) != nil || ep.ValidateApplication(b, app) != nil || ep.Action != "attach" || ep.Target.DaemonID != proof.DatabasePlan.Release.DaemonID {
		return HostingDatabaseNetworkMember{}, ErrInvalidHostingDatabaseNetworkPlan
	}
	endpoint, receiptID, receiptHash, ok := HostingDatabaseMemberEndpoint(proof)
	if !ok {
		return HostingDatabaseNetworkMember{}, ErrInvalidHostingDatabaseNetworkPlan
	}
	return HostingDatabaseNetworkMember{BindingID: b.ID, BindingHash: b.Hash, EndpointPlanHash: ep.Hash, ReceiptID: receiptID, ReceiptHash: receiptHash, Endpoint: endpoint}, nil
}

// ValidateHostingDatabaseNetworkRetirement checks complete historical evidence
// against the exact baseline member and revision range. Revalidating the same
// proof against the same retained baseline is deterministic, not live authority.
// Contextual callers must also match the captured database plan to their retained
// transition target and initialization provenance.
func ValidateHostingDatabaseNetworkRetirement(proof HostingDatabaseNetworkRetirementProof, member HostingDatabaseNetworkMember, priorRevision, resultingRevision int64) error {
	attached, err := databaseNetworkAttachedMember(proof.Attached)
	b, p, intent, detached := proof.Attached.Binding, proof.RetirePlan, proof.RetireIntent, proof.Detached
	if err != nil || attached != member || priorRevision < 1 || resultingRevision <= priorRevision || proof.Attached.EndpointPlan.MembershipRevision > priorRevision || p.Validate(b) != nil || p.Action != "retire" || p.Target != member.Endpoint.Target || p.ExpectedEndpointID != member.Endpoint.EndpointID || p.OperationID == proof.Attached.EndpointPlan.OperationID || p.OperationID == member.Endpoint.Target.ContainerOperationID || p.MembershipRevision <= proof.Attached.EndpointPlan.MembershipRevision || p.MembershipRevision <= priorRevision || p.MembershipRevision > resultingRevision {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	if intent.Phase != "retire_intent" || detached.Phase != "detached" || intent.ReceiptID == member.ReceiptID || detached.ReceiptID == member.ReceiptID || intent.ValidateTransition(nil, p, b) != nil || detached.ValidateTransition(&intent, p, b) != nil {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	return nil
}

func databaseNetworkRetirementContext(captured, current HostingApplyPlan, origin HostingDatabaseNetworkOrigin) bool {
	if !databaseNetworkSameTarget(captured, current) || captured.Snapshot.ResourceGeneration > current.Snapshot.ResourceGeneration {
		return false
	}
	creationPlan := captured.Hash
	if captured.PreviousHealthy != nil {
		if captured.PreviousHealthy.Storage == nil {
			return false
		}
		creationPlan = captured.PreviousHealthy.Storage.CreationPlanHash
	}
	return creationPlan == origin.OriginPlan
}

func validateDatabaseNetworkRetirements(database HostingApplyPlan, origin HostingDatabaseNetworkOrigin, prior, resulting []HostingDatabaseNetworkMember, priorRevision, resultingRevision int64, retired []HostingDatabaseNetworkRetirementProof) error {
	if len(retired) > 2 {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	removed := make(map[HostingDatabaseNetworkMember]bool)
	receipts := make(map[string]bool)
	operations := make(map[string]bool)
	for _, member := range prior {
		receipts[member.ReceiptID] = true
		if !slices.Contains(resulting, member) {
			removed[member] = true
		}
	}
	if len(removed) != len(retired) {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	for _, member := range resulting {
		receipts[member.ReceiptID] = true
	}
	for _, proof := range retired {
		member, err := databaseNetworkAttachedMember(proof.Attached)
		if err != nil || !databaseNetworkRetirementContext(proof.Attached.DatabasePlan, database, origin) || !removed[member] || ValidateHostingDatabaseNetworkRetirement(proof, member, priorRevision, resultingRevision) != nil || operations[proof.RetirePlan.OperationID] {
			return ErrInvalidHostingDatabaseNetworkPlan
		}
		for _, current := range resulting {
			if current.Endpoint.EndpointID == member.Endpoint.EndpointID {
				return ErrInvalidHostingDatabaseNetworkPlan
			}
		}
		for _, id := range []string{proof.RetireIntent.ReceiptID, proof.Detached.ReceiptID} {
			if receipts[id] {
				return ErrInvalidHostingDatabaseNetworkPlan
			}
			receipts[id] = true
		}
		operations[proof.RetirePlan.OperationID] = true
		delete(removed, member)
	}
	return nil
}
