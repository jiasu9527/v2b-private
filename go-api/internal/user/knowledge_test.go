package user

import "testing"

func TestReplaceKnowledgeAccessSections(t *testing.T) {
	body := "before <!--access start-->secret<!--access end--> after"
	got := replaceKnowledgeAccessSections(body)

	if got == body {
		t.Fatalf("expected access section to be replaced")
	}
	if got != "before <div class=\"forest-no-access\">You must have a valid subscription to view content in this area</div> after" {
		t.Fatalf("unexpected formatted body: %q", got)
	}
}

func TestApplyKnowledgeTemplate(t *testing.T) {
	body := "{{siteName}}|{{subscribeUrl}}|{{urlEncodeSubscribeUrl}}|{{safeBase64SubscribeUrl}}|{{subscribeToken}}"
	got := applyKnowledgeTemplate(body, "Forest", "https://example.com/sub?token=abc+/=", "abc+/=")

	want := "Forest|https://example.com/sub?token=abc+/=|https%3A%2F%2Fexample.com%2Fsub%3Ftoken%3Dabc%2B%2F%3D|aHR0cHM6Ly9leGFtcGxlLmNvbS9zdWI_dG9rZW49YWJjKy89|abc+/="
	if got != want {
		t.Fatalf("unexpected template body: %q", got)
	}
}
