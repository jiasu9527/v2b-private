package cliententry

import (
	"reflect"
	"testing"
)

func TestRuntimeConditionsSplitRetainsOnlyPerRequestUA(t *testing.T) {
	conditions, err := DecodeConditions(`[{"field":"user_id","operator":"between","min":100,"max":200},{"field":"ua","operator":"contains_any","values":["Forest"]},{"field":"ua","operator":"excludes_any","values":["curl"]}]`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(RuntimeConditions("standard", conditions), conditions) {
		t.Fatal("ordinary rule conditions changed")
	}
	runtime := RuntimeConditions("split", conditions)
	if len(runtime) != 2 || len(conditions) != 3 || conditions[0].Field != "user_id" {
		t.Fatal("runtime extraction lost UA or mutated source")
	}
	for _, tc := range []struct {
		ua   string
		want bool
	}{{"forest Android", true}, {"Forest curl", false}, {"Clash", false}, {"", false}} {
		if got := MatchAll(runtime, Subject{UserID: 999, UA: tc.ua}); got != tc.want {
			t.Fatalf("UA %q got %v want %v", tc.ua, got, tc.want)
		}
	}
	if !MatchAll(RuntimeConditions("split", conditions[:1]), Subject{UserID: 999}) {
		t.Fatal("old fixed snapshots incorrectly re-evaluated their original ID range")
	}
}

func TestRuntimeConditionsSplitPreservesEveryUAOperator(t *testing.T) {
	for _, tc := range []struct {
		name, condition, ua string
		want                bool
	}{
		{"empty matches", `{"field":"ua","operator":"empty"}`, "", true},
		{"empty rejects text", `{"field":"ua","operator":"empty"}`, "Forest", false},
		{"not empty matches", `{"field":"ua","operator":"not_empty"}`, "Forest", true},
		{"not empty rejects blank", `{"field":"ua","operator":"not_empty"}`, "", false},
		{"exclude preserves empty", `{"field":"ua","operator":"excludes_any","values":["curl"]}`, "", true},
		{"exclude rejects keyword", `{"field":"ua","operator":"excludes_any","values":["curl"]}`, "CURL/1", false},
		{"include ignores case", `{"field":"ua","operator":"contains_any","values":["Forest"]}`, "forest/1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conditions, err := DecodeConditions("[" + tc.condition + "]")
			if err != nil {
				t.Fatal(err)
			}
			if got := MatchAll(RuntimeConditions("split", conditions), Subject{UA: tc.ua}); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
