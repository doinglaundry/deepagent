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
	if err := json.Unmarshal(snapshot, &cp); err != nil {
		return err
	}
	known := map[string]bool{}
	var collect func(*checkpointValue)
	collect = func(value *checkpointValue) {
		if value == nil {
			return
		}
		if addresses := value.MapValues["InterruptID2Addr"]; addresses != nil {
			for key := range addresses.MapValues {
				known[key] = true
			}
		}
		if children := value.MapValues["SubGraphs"]; children != nil {
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
		if err := check(id); err != nil {
			return err
		}
	}
	for id := range data {
		if err := check(id); err != nil {
			return err
		}
	}
	return nil
}
