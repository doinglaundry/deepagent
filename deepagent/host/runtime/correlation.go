package runtime

import "eino-cli/deepagent/protocol"

// eventTracker follows execution generations from durable facts, never pubsub.
// A full model response seals that response ID against delayed token delivery.
type eventTracker struct {
	messageID, runID string
	kind             protocol.InputKind
	seen             map[string]bool
	complete         map[string]bool
	retired          map[string]bool
}

func newEventTracker(messageID, runID string, kind protocol.InputKind) *eventTracker {
	return &eventTracker{messageID: messageID, runID: runID, kind: kind, seen: map[string]bool{}, complete: map[string]bool{}, retired: map[string]bool{}}
}
func (t *eventTracker) accept(e protocol.Event) bool {
	if t.seen[e.ID] {
		return false
	}
	related := false
	for _, id := range e.MessageIDs {
		if id == t.messageID {
			related = true
			break
		}
	}
	if related && t.kind == protocol.InputCompact && e.Kind == protocol.EventCompacted {
		t.seen[e.ID] = true
		return true
	}
	if e.Kind == protocol.EventRunCancelled && len(e.MessageIDs) > 0 && !related {
		return false
	}
	authority := e.Durable() && (e.Kind == protocol.EventRunStarted || e.Kind == protocol.EventInputConsumed || t.runID == "")
	if related && authority && !t.retired[e.RunID] && e.RunID != "" {
		if t.runID != "" && t.runID != e.RunID {
			t.retired[t.runID] = true
		}
		t.runID = e.RunID
	}
	if t.runID == "" || e.RunID != t.runID {
		return false
	}
	toolKey := "tool/" + e.RunID + "/" + e.ToolCallID
	if e.Kind == protocol.EventToolOutput && t.complete[toolKey] {
		return false
	}
	if e.Kind == protocol.EventToolCompleted {
		t.complete[toolKey] = true
	}
	responseKey := "response/" + e.RunID + "/" + e.ResponseID
	if e.Kind == protocol.EventTextDelta && t.complete[responseKey] {
		return false
	}
	if e.Kind == protocol.EventText {
		t.complete[responseKey] = true
	}
	t.seen[e.ID] = true
	return true
}
