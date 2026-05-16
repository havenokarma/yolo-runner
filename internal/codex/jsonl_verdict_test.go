package codex

import "testing"

func TestLastStructuredVerdictLine_PlainText(t *testing.T) {
	got, ok := lastStructuredVerdictLine("some chatter\nREVIEW_VERDICT: pass\n")
	if !ok || got != "pass" {
		t.Fatalf("plain-text verdict not parsed, got %q ok=%v", got, ok)
	}
}

func TestLastStructuredVerdictLine_CodexJSONL(t *testing.T) {
	// codex-cli writes JSONL where the agent reply lives inside item.text with
	// embedded \n. The verdict marker is only matchable after splitting that
	// inner text.
	content := `{"type":"thread.started"}
{"type":"item.completed","item":{"id":"item_4","type":"agent_message","text":"REVIEW_FAIL_FEEDBACK: AC #4 not met — /metrics missing\nREVIEW_VERDICT: fail"}}
{"type":"turn.completed"}
`
	got, ok := lastStructuredVerdictLine(content)
	if !ok || got != "fail" {
		t.Fatalf("expected fail verdict from JSONL agent_message, got %q ok=%v", got, ok)
	}

	feedback, ok := lastStructuredReviewFailFeedbackLine(content)
	if !ok {
		t.Fatalf("expected feedback to be parsed from JSONL agent_message")
	}
	want := "AC #4 not met — /metrics missing"
	if feedback != want {
		t.Fatalf("feedback mismatch:\n got: %q\nwant: %q", feedback, want)
	}
}

func TestLastStructuredVerdictLine_PassInsideAgentMessage(t *testing.T) {
	content := `{"type":"item.completed","item":{"type":"agent_message","text":"all checks ok\nREVIEW_VERDICT: pass"}}`
	got, ok := lastStructuredVerdictLine(content)
	if !ok || got != "pass" {
		t.Fatalf("expected pass verdict, got %q ok=%v", got, ok)
	}
}

func TestLastStructuredVerdictLine_IgnoresNonAgentMessageItems(t *testing.T) {
	content := `{"type":"item.completed","item":{"type":"command_execution","text":"REVIEW_VERDICT: pass"}}`
	if _, ok := lastStructuredVerdictLine(content); ok {
		t.Fatalf("must not pick verdict from non-agent_message items (would be too permissive)")
	}
}
