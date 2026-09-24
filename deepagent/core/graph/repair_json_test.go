package graph

import (
	"encoding/json"
	"testing"
)

func TestRepairToolArgumentsOnlyUnambiguousSyntax(t *testing.T) {
	for _, s := range []string{"```json\n{\"path\":\"x\",}\n```", `{"a":[1,2,],"literal":",}"}`} {
		out, e := repairToolArguments(s)
		if e != nil || !json.Valid([]byte(out)) {
			t.Fatal(out, e)
		}
	}
	for _, s := range []string{`{path:"x"}`, `{"path":`, "text {\"a\":1}"} {
		if _, e := repairToolArguments(s); e == nil {
			t.Fatal("invented missing JSON", s)
		}
	}
}
