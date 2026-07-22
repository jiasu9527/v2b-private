package cliententry

import (
	"encoding/json"
	"testing"
)

func TestNormalizeConditionsRejectsNegativeAndInvalidUserIDs(t *testing.T) {
	for _, condition := range []Condition{
		{Field: "registration_days", Operator: "lte", Value: json.RawMessage("-1")},
		{Field: "user_id", Operator: "in", Values: []json.RawMessage{json.RawMessage("0")}},
		{Field: "plan_id", Operator: "between", Min: json.RawMessage("5"), Max: json.RawMessage("2")},
	} {
		if _, err := NormalizeConditions([]Condition{condition}); err == nil {
			t.Fatalf("expected condition %#v to be rejected", condition)
		}
	}
}

func TestMatchAllUsesANDAndUAExclusionVeto(t *testing.T) {
	conditions, err := NormalizeConditions([]Condition{
		{Field: "registration_days", Operator: "lte", Value: json.RawMessage("30")},
		{Field: "ua", Operator: "contains_any", Values: []json.RawMessage{json.RawMessage(`"Clash"`), json.RawMessage(`"Mihomo"`)}},
		{Field: "ua", Operator: "excludes_any", Values: []json.RawMessage{json.RawMessage(`"Bad"`)}},
	})
	if err != nil {
		t.Fatalf("normalize conditions: %v", err)
	}
	if !MatchAll(conditions, Subject{RegistrationDays: 7, UA: "ClashMeta/1.0"}) {
		t.Fatal("expected all conditions to match")
	}
	if MatchAll(conditions, Subject{RegistrationDays: 7, UA: "Bad ClashMeta/1.0"}) {
		t.Fatal("excluded UA must veto the rule")
	}
	if MatchAll(conditions, Subject{RegistrationDays: 40, UA: "ClashMeta/1.0"}) {
		t.Fatal("numeric condition must be combined with UA by AND")
	}
}

func TestNormalizeHostRejectsRetiredDSL(t *testing.T) {
	if host, err := NormalizeHost("VIP.Example.com."); err != nil || host != "vip.example.com" {
		t.Fatalf("normalize host = %q, %v", host, err)
	}
	for _, value := range []string{
		"default.example.com,clash.example.com(UClash)",
		"https://example.com",
		"example.com:443",
	} {
		if _, err := NormalizeHost(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestEmailConditionIsIndependentFromUserID(t *testing.T) {
	conditions, err := NormalizeConditions([]Condition{{
		Field: "email", Operator: "in", Values: []json.RawMessage{json.RawMessage(`"VIP@Example.com"`)},
	}})
	if err != nil {
		t.Fatalf("normalize email condition: %v", err)
	}
	if !MatchAll(conditions, Subject{UserID: 99, Email: "vip@example.com"}) {
		t.Fatal("email condition must match independently from user ID")
	}
	if MatchAll(conditions, Subject{UserID: 99, Email: "other@example.com"}) {
		t.Fatal("email condition must not match another email sharing the same ID-style context")
	}
}
