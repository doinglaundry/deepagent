package serialiser

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func ToString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func ParseNonzeroID(raw, field string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id == 0 {
		if err == nil {
			err = fmt.Errorf("must be nonzero")
		}
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	return id, nil
}

func WrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
