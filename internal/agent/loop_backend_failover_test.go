package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/egv/yolo-runner/v2/internal/contracts"
)

func TestBuildPromptReviewContainsStructuredVerdictContract(t *testing.T) {
	task := contracts.Task{ID: "t-1", Title: "demo"}
	prompt := buildPrompt(task, contracts.RunnerModeReview, false)

	for _, marker := range []string{
		"REVIEW_VERDICT: pass",
		"REVIEW_VERDICT: fail",
		"REVIEW_FAIL_FEEDBACK:",
	} {
		if !strings.Contains(prompt, marker) {
			t.Fatalf("expected review prompt to contain %q, got:\n%s", marker, prompt)
		}
	}
}

func TestBuildReviewVerdictPromptContainsStructuredContract(t *testing.T) {
	task := contracts.Task{ID: "t-1", Title: "demo"}
	prompt := buildReviewVerdictPrompt(task)
	for _, marker := range []string{
		"REVIEW_VERDICT: pass",
		"REVIEW_VERDICT: fail",
		"REVIEW_FAIL_FEEDBACK:",
	} {
		if !strings.Contains(prompt, marker) {
			t.Fatalf("expected verdict-retry prompt to contain %q, got:\n%s", marker, prompt)
		}
	}
}

func TestIsRecoverableProviderFailureReason(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   bool
	}{
		{name: "empty", reason: "", want: false},
		{name: "rate limit", reason: "rate limit exceeded", want: true},
		{name: "quota", reason: "provider error: quota exceeded", want: true},
		{name: "timeout-y", reason: "tool timed out after 30s", want: true},
		{name: "generic error", reason: "runner exited with status 1", want: true},
		{name: "review reject", reason: "review rejected: missing /metrics", want: false},
		{name: "review verdict fail", reason: "review verdict returned fail", want: false},
		{name: "review feedback", reason: "review feedback indicates regression", want: false},
		{name: "acceptance criteria", reason: "failing acceptance criteria", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isRecoverableProviderFailureReason(tc.reason)
			if got != tc.want {
				t.Fatalf("isRecoverableProviderFailureReason(%q) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}

func TestShouldUseBackendFallbackForFailure(t *testing.T) {
	failed := func(reason string) contracts.RunnerResult {
		return contracts.RunnerResult{Status: contracts.RunnerResultFailed, Reason: reason}
	}
	if !shouldUseBackendFallbackForFailure(failed("rate limit"), "claude", "codex-cli") {
		t.Fatalf("expected switch on rate limit between distinct backends")
	}
	if shouldUseBackendFallbackForFailure(failed("rate limit"), "claude", "claude") {
		t.Fatalf("must not switch when primary == fallback")
	}
	if shouldUseBackendFallbackForFailure(failed("rate limit"), "", "codex-cli") {
		t.Fatalf("must not switch when primary backend is empty")
	}
	if shouldUseBackendFallbackForFailure(failed("review rejected"), "claude", "codex-cli") {
		t.Fatalf("review-content failures must not trigger backend switch")
	}
	completed := contracts.RunnerResult{Status: contracts.RunnerResultCompleted}
	if shouldUseBackendFallbackForFailure(completed, "claude", "codex-cli") {
		t.Fatalf("must not switch on success")
	}
}

func TestLoopRetriesImplementWithBackendFallback(t *testing.T) {
	mgr := newFakeTaskManager(contracts.Task{ID: "t-1", Title: "Task 1", Status: contracts.TaskStatusOpen})

	primary := &fakeRunner{results: []contracts.RunnerResult{
		{Status: contracts.RunnerResultFailed, Reason: "provider error: 429 too many requests"},
	}}
	fallback := &fakeRunner{results: []contracts.RunnerResult{
		{Status: contracts.RunnerResultCompleted},
	}}

	loop := NewLoop(mgr, primary, nil, LoopOptions{
		ParentID:        "root",
		Backend:         "claude",
		Model:           "claude-opus",
		FallbackBackend: "codex-cli",
		FallbackModel:   "gpt-5.3-codex",
		BackendRunners: map[string]contracts.AgentRunner{
			"claude":    primary,
			"codex-cli": fallback,
		},
	})

	summary, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("loop failed: %v", err)
	}
	if summary.Completed != 1 {
		t.Fatalf("expected task to complete after backend failover, got %#v", summary)
	}
	if len(primary.requests) != 1 {
		t.Fatalf("expected primary backend hit once, got %d", len(primary.requests))
	}
	if len(fallback.requests) != 1 {
		t.Fatalf("expected fallback backend hit once, got %d", len(fallback.requests))
	}
	if fallback.requests[0].Model != "gpt-5.3-codex" {
		t.Fatalf("expected fallback model to be substituted, got %q", fallback.requests[0].Model)
	}
}

func TestLoopRetriesTransientProviderFailureBeforeFallback(t *testing.T) {
	mgr := newFakeTaskManager(contracts.Task{ID: "t-1", Title: "Task 1", Status: contracts.TaskStatusOpen})

	primary := &fakeRunner{results: []contracts.RunnerResult{
		{Status: contracts.RunnerResultFailed, Reason: "API Error: The socket connection was closed unexpectedly"},
		{Status: contracts.RunnerResultCompleted},
	}}
	fallback := &fakeRunner{results: []contracts.RunnerResult{
		{Status: contracts.RunnerResultCompleted},
	}}

	loop := NewLoop(mgr, primary, nil, LoopOptions{
		ParentID:            "root",
		Backend:             "claude",
		Model:               "claude-opus",
		FallbackBackend:     "codex-cli",
		FallbackModel:       "gpt-5.3-codex",
		ProviderRetryBudget: 3,
		BackendRunners: map[string]contracts.AgentRunner{
			"claude":    primary,
			"codex-cli": fallback,
		},
	})

	summary, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("loop failed: %v", err)
	}
	if summary.Completed != 1 {
		t.Fatalf("expected task to complete after same-backend retry, got %#v", summary)
	}
	if len(primary.requests) != 2 {
		t.Fatalf("expected primary backend hit twice, got %d", len(primary.requests))
	}
	if len(fallback.requests) != 0 {
		t.Fatalf("expected fallback backend not to run, got %d requests", len(fallback.requests))
	}
}

func TestLoopFallsBackAfterTransientProviderRetryBudget(t *testing.T) {
	mgr := newFakeTaskManager(contracts.Task{ID: "t-1", Title: "Task 1", Status: contracts.TaskStatusOpen})

	primary := &fakeRunner{results: []contracts.RunnerResult{
		{Status: contracts.RunnerResultFailed, Reason: "API Error: The socket connection was closed unexpectedly"},
		{Status: contracts.RunnerResultFailed, Reason: "API Error: The socket connection was closed unexpectedly"},
	}}
	fallback := &fakeRunner{results: []contracts.RunnerResult{
		{Status: contracts.RunnerResultCompleted},
	}}

	loop := NewLoop(mgr, primary, nil, LoopOptions{
		ParentID:            "root",
		Backend:             "claude",
		Model:               "claude-opus",
		FallbackBackend:     "codex-cli",
		FallbackModel:       "gpt-5.3-codex",
		ProviderRetryBudget: 1,
		BackendRunners: map[string]contracts.AgentRunner{
			"claude":    primary,
			"codex-cli": fallback,
		},
	})

	summary, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("loop failed: %v", err)
	}
	if summary.Completed != 1 {
		t.Fatalf("expected task to complete after backend failover, got %#v", summary)
	}
	if len(primary.requests) != 2 {
		t.Fatalf("expected initial primary run plus one retry, got %d", len(primary.requests))
	}
	if len(fallback.requests) != 1 {
		t.Fatalf("expected fallback backend hit once, got %d", len(fallback.requests))
	}
}

func TestLoopReviewUsesDedicatedReviewBackend(t *testing.T) {
	mgr := newFakeTaskManager(contracts.Task{ID: "t-1", Title: "Task 1", Status: contracts.TaskStatusOpen})

	primary := &fakeRunner{results: []contracts.RunnerResult{
		{Status: contracts.RunnerResultCompleted},
	}}
	reviewer := &fakeRunner{results: []contracts.RunnerResult{
		{Status: contracts.RunnerResultCompleted, ReviewReady: true, Artifacts: map[string]string{"review_verdict": "pass"}},
	}}

	loop := NewLoop(mgr, primary, nil, LoopOptions{
		ParentID:      "root",
		Backend:       "claude",
		Model:         "claude-opus",
		ReviewBackend: "codex-cli",
		ReviewModel:   "gpt-5.3-codex",
		RequireReview: true,
		BackendRunners: map[string]contracts.AgentRunner{
			"claude":    primary,
			"codex-cli": reviewer,
		},
	})

	summary, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("loop failed: %v", err)
	}
	if summary.Completed != 1 && summary.Blocked != 1 {
		t.Fatalf("expected loop to terminate (completed or blocked landing without VCS): %#v", summary)
	}
	if len(primary.requests) == 0 {
		t.Fatalf("expected at least one implement request on primary backend")
	}
	if len(reviewer.requests) == 0 {
		t.Fatalf("expected review request on review backend")
	}
	gotReviewMode := false
	for _, r := range reviewer.requests {
		if r.Mode == contracts.RunnerModeReview {
			gotReviewMode = true
			if r.Model != "gpt-5.3-codex" {
				t.Fatalf("review request used model %q, want gpt-5.3-codex", r.Model)
			}
		}
	}
	if !gotReviewMode {
		t.Fatalf("review runner never received a review-mode request: %#v", reviewer.requests)
	}
	for _, r := range primary.requests {
		if r.Mode == contracts.RunnerModeReview {
			t.Fatalf("primary backend received a review-mode request; review should route to ReviewBackend")
		}
	}
}

func TestLoopReviewFallsBackOnProviderError(t *testing.T) {
	mgr := newFakeTaskManager(contracts.Task{ID: "t-1", Title: "Task 1", Status: contracts.TaskStatusOpen})

	implRunner := &fakeRunner{results: []contracts.RunnerResult{
		{Status: contracts.RunnerResultCompleted},
	}}
	reviewerPrimary := &fakeRunner{results: []contracts.RunnerResult{
		{Status: contracts.RunnerResultFailed, Reason: "provider error: 429 too many requests"},
	}}
	reviewerFallback := &fakeRunner{results: []contracts.RunnerResult{
		{Status: contracts.RunnerResultCompleted, ReviewReady: true, Artifacts: map[string]string{"review_verdict": "pass"}},
	}}

	loop := NewLoop(mgr, implRunner, nil, LoopOptions{
		ParentID:              "root",
		Backend:               "impl-be",
		Model:                 "impl-model",
		ReviewBackend:         "rev-be",
		ReviewModel:           "rev-model",
		ReviewFallbackBackend: "rev-fb-be",
		ReviewFallbackModel:   "rev-fb-model",
		RequireReview:         true,
		BackendRunners: map[string]contracts.AgentRunner{
			"impl-be":   implRunner,
			"rev-be":    reviewerPrimary,
			"rev-fb-be": reviewerFallback,
		},
	})

	_, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("loop failed: %v", err)
	}
	if len(reviewerPrimary.requests) != 1 {
		t.Fatalf("expected primary review backend hit once, got %d", len(reviewerPrimary.requests))
	}
	if len(reviewerFallback.requests) != 1 {
		t.Fatalf("expected review fallback backend hit once, got %d", len(reviewerFallback.requests))
	}
	if reviewerFallback.requests[0].Mode != contracts.RunnerModeReview {
		t.Fatalf("review fallback runner received non-review request: %v", reviewerFallback.requests[0].Mode)
	}
	if reviewerFallback.requests[0].Model != "rev-fb-model" {
		t.Fatalf("review fallback runner got model %q, want rev-fb-model", reviewerFallback.requests[0].Model)
	}
}
