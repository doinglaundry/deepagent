package checkpointer

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// ValidateResume checks explicit targets against Eino's persisted interrupt
// addresses. No IDs means the existing before/after-node resume operation.
func ValidateResume(snapshot []byte, interruptIDs []string, resumeData map[string]any) error {
	if len(interruptIDs) == 0 && len(resumeData) == 0 {
		return nil
	}
	var checkpoint checkpointValue
	decodeErr := json.Unmarshal(snapshot, &checkpoint)
	if decodeErr != nil {
		return decodeErr
	}
	knownInterruptIDs := map[string]bool{}
	var collectInterruptIDs func(*checkpointValue)
	collectInterruptIDs = func(value *checkpointValue) {
		if value == nil {
			return
		}
		addresses := value.MapValues["InterruptID2Addr"]
		if addresses != nil {
			for key := range addresses.MapValues {
				knownInterruptIDs[key] = true
			}
		}
		children := value.MapValues["SubGraphs"]
		if children != nil {
			for _, child := range children.MapValues {
				collectInterruptIDs(child)
			}
		}
	}
	collectInterruptIDs(&checkpoint)
	validateInterruptID := func(id string) error {
		if id == "" || !knownInterruptIDs[strconv.Quote(id)] {
			return fmt.Errorf("resume interrupt %q not present in checkpoint", id)
		}
		return nil
	}
	for _, id := range interruptIDs {
		err := validateInterruptID(id)
		if err != nil {
			return err
		}
	}
	for id := range resumeData {
		err := validateInterruptID(id)
		if err != nil {
			return err
		}
	}
	return nil
}
