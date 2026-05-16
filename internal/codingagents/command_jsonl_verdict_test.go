package codingagents

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStructuredReviewVerdict_PlainText covers backwards compatibility with
// adapters that write the verdict marker as a plain text line.
func TestStructuredReviewVerdict_PlainText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.log")
	if err := os.WriteFile(path, []byte("some chatter\nREVIEW_VERDICT: pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := structuredReviewVerdict(path)
	if !ok || got != "pass" {
		t.Fatalf("plain-text verdict not parsed, got %q ok=%v", got, ok)
	}
}

// TestStructuredReviewVerdict_CodexCLIJSONL pins the parser against the
// real output codex-cli (adapter=command, args=`codex exec --json`) produces:
// each line is a JSON event, the agent reply lives inside `item.text` with
// embedded `\n`. The previous parser missed the verdict because it ran the
// regex over the raw JSON line, where the marker is escaped/inline.
func TestStructuredReviewVerdict_CodexCLIJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.jsonl")
	content := `{"type":"thread.started","thread_id":"abc"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"REVIEW_FAIL_FEEDBACK: AC #4 not met — /metrics missing\nREVIEW_VERDICT: fail"}}
{"type":"turn.completed","usage":{"input_tokens":1}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := structuredReviewVerdict(path)
	if !ok {
		t.Fatalf("verdict not parsed from JSONL agent_message")
	}
	if got != "fail" {
		t.Fatalf("verdict=%q want fail", got)
	}
}

func TestStructuredReviewVerdict_CodexCLIJSONLPass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex-pass.jsonl")
	content := `{"type":"thread.started","thread_id":"abc"}
{"type":"item.completed","item":{"type":"agent_message","text":"REVIEW_VERDICT: pass"}}
{"type":"turn.completed"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := structuredReviewVerdict(path)
	if !ok || got != "pass" {
		t.Fatalf("expected pass verdict, got %q ok=%v", got, ok)
	}
}

// TestStructuredReviewVerdict_IgnoresNonAgentMessageItems guards against false
// positives — verdict markers inside other item types (e.g. tool execution
// output) must NOT be picked up.
func TestStructuredReviewVerdict_IgnoresNonAgentMessageItems(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noise.jsonl")
	content := `{"type":"item.completed","item":{"type":"command_execution","text":"REVIEW_VERDICT: pass"}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := structuredReviewVerdict(path); ok {
		t.Fatalf("must not pick verdict from non-agent_message items")
	}
}
