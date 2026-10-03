package checkpointer

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// ValidateResume checks explicit targets against Eino's persisted interrupt
// addresses. No IDs means the existing before/after-node resume operation.
func ValidateResume(snapshot []byte, ids []string, data map[string]any) error {
	if len(ids) == 0 && len(data) == 0 {
		return nil
	}
	var cp checkpointValue
	decodeErr := json.Unmarshal(snapshot, &cp)
	if decodeErr != nil {
		return decodeErr
	}
	known := map[string]bool{}
	var collect func(*checkpointValue)
	collect = func(value *checkpointValue) {
		if value == nil {
			return
		}
		addresses := value.MapValues["InterruptID2Addr"]
		if addresses != nil {
			for key := range addresses.MapValues {
				known[key] = true
			}
		}
		children := value.MapValues["SubGraphs"]
		if children != nil {
			for _, child := range children.MapValues {
				collect(child)
			}
		}
	}
	collect(&cp)
	check := func(id string) error {
		if id == "" || !known[strconv.Quote(id)] {
			return fmt.Errorf("resume interrupt %q not present in checkpoint", id)
		}
		return nil
	}
	for _, id := range ids {
		err := check(id)
		if err != nil {
			return err
		}
	}
	for id := range data {
		err := check(id)
		if err != nil {
			return err
		}
	}
	return nil
}
