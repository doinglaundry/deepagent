package repairjson

import (
	"encoding/json"
	"testing"
)

func TestRepairJSONOnlyUnambiguousSyntax(t *testing.T) {
	for _, s := range []string{"```json\n{\"path\":\"x\",}\n```", `{"a":[1,2,],"literal":",}"}`} {
		out, e := RepairJSON(s)
		if e != nil || !json.Valid([]byte(out)) {
			t.Fatal(out, e)
		}
	}
	for _, s := range []string{`{path:"x"}`, `{"path":`, "text {\"a\":1}"} {
		if _, e := RepairJSON(s); e == nil {
			t.Fatal("invented missing JSON", s)
		}
	}
}
