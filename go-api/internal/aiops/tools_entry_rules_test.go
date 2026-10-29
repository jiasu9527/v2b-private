package aiops

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"forest/go-api/internal/cliententry"
)

func TestAIEntryRuleConditionPaginationIsExplicit(t *testing.T) {
	c := cliententry.Condition{Field: "email", Operator: "in"}
	for i := 0; i < 150; i++ {
		raw, _ := json.Marshal(fmt.Sprintf("fixture-%d@example.test", i))
		c.Values = append(c.Values, raw)
	}
	out := projectEntryEditCondition(c, 3)
	if out["values_count"] != 150 || out["values_truncated"] != true || out["index"] != 3 {
		t.Fatalf("%+v", out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "fixture-20@example.test") || !strings.Contains(string(raw), "fixture-19@example.test") {
		t.Fatal(string(raw))
	}
	empty := projectEntryEditCondition(cliententry.Condition{Field: "email", Operator: "in"}, 0)
	raw, _ = json.Marshal(empty)
	if !strings.Contains(string(raw), `"values":[]`) || empty["values_truncated"] != false {
		t.Fatal("deny-all must remain explicit", string(raw))
	}
}
