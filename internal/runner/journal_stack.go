package runner

import (
	"bytes"
	"encoding/json"
	"errors"

	"nerocd/internal/domain"
)

func hostingJournalUsesStack(entry JournalHosting) bool {
	if entry.StackStopPlan != nil {
		return true
	}
	return entry.Plan != nil && (entry.Plan.Stack != nil || entry.Plan.Snapshot.Stack != (domain.HostingStackRef{})) || entry.Prepared != nil && (entry.Prepared.Release.Stack != (domain.HostingStackReleaseRef{}) || len(entry.Prepared.StackImages) > 0) || entry.Result != nil && entry.Result.Result.Stack != nil
}
func journalStateUsesStack(state journalState) bool {
	for _, entry := range state.Hosting {
		if hostingJournalUsesStack(entry) {
			return true
		}
	}
	return false
}

// Version 23 wraps the existing feature version so older binaries cannot replay
// a whole-stack intent as a single-container operation. Mixed history retains
// its existing codecs and validation without changing their version semantics.
func decodeStackJournal(raw []byte, state *journalState) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(state) != nil || state.Version != 23 || state.StackBaseVersion < 2 || state.StackBaseVersion > 22 || !journalStateUsesStack(*state) {
		return nil, errors.New("invalid managed stack journal version")
	}
	state.Version = state.StackBaseVersion
	state.StackBaseVersion = 0
	return json.Marshal(state)
}

func validateHostingStackJournal(entry JournalHosting) error {
	if !hostingJournalUsesStack(entry) {
		return nil
	}
	if entry.CleanupPlan != nil || entry.LocalBuild || HostingJournalUsesDatabase(entry) || HostingJournalUsesStorage(entry) {
		return errors.New("stack journal execution family is mixed")
	}
	if entry.Plan != nil && !domain.HostingPlanFamilyMatches(entry.Plan.Snapshot, *entry.Plan) {
		return errors.New("stack journal plan changed")
	}
	if entry.Prepared != nil {
		images := entry.Prepared.StackImages
		if len(images) == 0 || len(images) > 16 || entry.Prepared.Release.Stack.Hash != domain.HostingDocumentHash(images) {
			return errors.New("stack journal image set changed")
		}
		for i, image := range images {
			if domain.ValidateHostingRelease(image.Release) != nil || image.Release.Stack != (domain.HostingStackReleaseRef{}) || image.Release.DaemonID != entry.Prepared.Release.DaemonID || i > 0 && images[i-1].Component >= image.Component {
				return errors.New("invalid stack journal component release")
			}
		}
	}
	return nil
}
