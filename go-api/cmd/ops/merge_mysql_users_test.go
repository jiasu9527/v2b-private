package main

import (
	"regexp"
	"testing"
)

func TestParseMergeMySQLPlanMap(t *testing.T) {
	planMap, err := parseMergeMySQLPlanMap("1:10, 2:20\n3:30")
	if err != nil {
		t.Fatalf("parseMergeMySQLPlanMap: %v", err)
	}

	if len(planMap) != 3 {
		t.Fatalf("expected 3 mappings, got %#v", planMap)
	}
	if planMap[1] != 10 || planMap[2] != 20 || planMap[3] != 30 {
		t.Fatalf("unexpected mappings: %#v", planMap)
	}
}

func TestParseMergeMySQLPlanMapRejectsInvalidToken(t *testing.T) {
	if _, err := parseMergeMySQLPlanMap("1:10,broken"); err == nil {
		t.Fatal("expected invalid mapping to fail")
	}
}

func TestEnsureUniqueMergeInviteCodeKeepsAvailableCode(t *testing.T) {
	used := map[string]struct{}{
		"OTHER999": {},
	}

	got := ensureUniqueMergeInviteCode("ABC888", "user@example.com", used)
	if got != "ABC888" {
		t.Fatalf("expected original code kept, got %q", got)
	}
}

func TestEnsureUniqueMergeInviteCodeRenamesConflictingCode(t *testing.T) {
	used := map[string]struct{}{
		"ABC888":       {},
		"ABC8889A4B2C": {},
	}

	got := ensureUniqueMergeInviteCode("ABC888", "user@example.com", used)
	if got == "ABC888" {
		t.Fatalf("expected conflicting code to be renamed, got %q", got)
	}
	if len(got) > 32 {
		t.Fatalf("expected invite code length <= 32, got %d (%q)", len(got), got)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(got) {
		t.Fatalf("expected invite code to stay alphanumeric, got %q", got)
	}
}
