package arboreal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zenful-ai/arboreal/llm"
)

// ctxCapturingBehavior is a Behavior that records the context it was called
// with, so a test can assert how the executive threads context down into the
// behavior tree. It returns a CollectUserInputSignal, which makes
// TodoListExecutive.Execute take its early-return path and avoids any LLM call.
type ctxCapturingBehavior struct {
	called      bool
	receivedCtx context.Context
}

func (b *ctxCapturingBehavior) Hash() string        { return "ctx-capturing-behavior" }
func (b *ctxCapturingBehavior) Name() string        { return "ctx-capturing-behavior" }
func (b *ctxCapturingBehavior) Description() string { return "records the context it is called with" }
func (b *ctxCapturingBehavior) Copy() Behavior      { return b }

func (b *ctxCapturingBehavior) Call(ctx context.Context, messages AnnotatedMessages) (AnnotatedMessages, Signal) {
	b.called = true
	b.receivedCtx = ctx
	return messages, &CollectUserInputSignal{}
}

// oneShotChannel delivers a single user message and then reports EOF, so
// RunLoop processes exactly one turn and then exits its loop.
type oneShotChannel struct {
	sent bool
}

func (c *oneShotChannel) AllocateID() string         { return "id" }
func (c *oneShotChannel) Send(*ChannelMessage) error { return nil }

func (c *oneShotChannel) Receive() (*ChannelMessage, error) {
	if c.sent {
		return nil, errors.New("eof")
	}
	c.sent = true
	return &ChannelMessage{Id: "1", Content: "Hello"}, nil
}

// TestRunLoopThreadsCallerSuppliedContext is the regression test for the
// nil-context panic. The RunLoop used to call e.Execute(nil, ...), so the
// behavior tree — and ultimately the LLM provider's select on ctx.Done() —
// received a nil context and crashed with a SIGSEGV on the very first user turn.
//
// We pre-seed the executive's plan with a behavior that captures the context it
// receives, drive a single turn through the real RunLoop, and assert the
// caller's own context (identified by a sentinel value) reaches the behavior —
// not merely that some non-nil context does. Pre-seeding the plan also means
// RunLoop skips the planning LLM call, and the behavior's CollectUserInputSignal
// makes Execute return before the summarizer LLM call — so the test is hermetic
// (no network required).
func TestRunLoopThreadsCallerSuppliedContext(t *testing.T) {
	behavior := &ctxCapturingBehavior{}

	exec := CreateTodoListExecutive("test exec", "exercises context threading", behavior)
	exec.plan = []*ExecGeneratedStep{
		{Behavior: behavior, Messages: AnnotatedMessages{}},
	}

	// Supply a caller-owned context carrying a sentinel value, and assert the
	// SAME context (not merely some non-nil one) is threaded to the behavior.
	type ctxKey string
	const sentinelKey ctxKey = "runloop-test-sentinel"
	ctx := context.WithValue(context.Background(), sentinelKey, "threaded")

	// RunLoop exits by returning the channel's terminating error.
	_ = exec.RunLoop(ctx, &oneShotChannel{})

	if !behavior.called {
		t.Fatal("behavior was never called; RunLoop did not reach the plan step")
	}
	if behavior.receivedCtx == nil {
		t.Fatal("behavior received a nil context: RunLoop is passing nil to Execute")
	}
	if got := behavior.receivedCtx.Value(sentinelKey); got != "threaded" {
		t.Fatalf("behavior did not receive the caller's context: Value(sentinelKey) = %v, want %q", got, "threaded")
	}
}

func TestModelURIs(t *testing.T) {
	cases := []struct {
		name         string
		plannerModel string
		repairModel  string
		ctxDefault   string
		wantPlanner  string
		wantRepair   string
	}{
		{
			name:        "nothing configured falls back for both",
			wantPlanner: fallbackModelURI,
			wantRepair:  fallbackModelURI,
		},
		{
			name:        "context default used for both",
			ctxDefault:  "anthropic:ctx",
			wantPlanner: "anthropic:ctx",
			wantRepair:  "anthropic:ctx",
		},
		{
			name:         "PlannerModel wins over context and repair inherits it",
			plannerModel: "anthropic:planner",
			ctxDefault:   "anthropic:ctx",
			wantPlanner:  "anthropic:planner",
			wantRepair:   "anthropic:planner",
		},
		{
			name:         "RepairModel overrides the inherited planner model",
			plannerModel: "anthropic:planner",
			repairModel:  "openai:repair",
			ctxDefault:   "anthropic:ctx",
			wantPlanner:  "anthropic:planner",
			wantRepair:   "openai:repair",
		},
		{
			name:        "RepairModel alone does not affect the planner",
			repairModel: "openai:repair",
			ctxDefault:  "anthropic:ctx",
			wantPlanner: "anthropic:ctx",
			wantRepair:  "openai:repair",
		},
		{
			name:        "RepairModel alone with no context: planner falls back, repair diverges",
			repairModel: "openai:repair",
			wantPlanner: fallbackModelURI,
			wantRepair:  "openai:repair",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t) // silence and isolate the fallback warning

			exec := CreateTodoListExecutive("test exec", "resolves models")
			exec.PlannerModel = tc.plannerModel
			exec.RepairModel = tc.repairModel

			ctx := context.Background()
			if tc.ctxDefault != "" {
				ctx = WithDefaultModel(ctx, tc.ctxDefault)
			}

			gotPlanner, gotRepair := exec.modelURIs(ctx)
			if gotPlanner != tc.wantPlanner || gotRepair != tc.wantRepair {
				t.Fatalf("modelURIs = (%q, %q), want (%q, %q)", gotPlanner, gotRepair, tc.wantPlanner, tc.wantRepair)
			}
		})
	}
}

// planErrorMessage runs Plan with the given executive and context and returns
// the message of the *ErrorSignal it panics with. It fails the test if Plan
// does not panic with an *ErrorSignal, because every case below configures a
// model that cannot produce a provider — so reaching the model call at all is
// the assertion.
func planErrorMessage(t *testing.T, exec *TodoListExecutive, ctx context.Context) string {
	t.Helper()
	var msg string
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("Plan did not panic; want an *ErrorSignal for an unresolvable model")
			}
			sig, ok := r.(*ErrorSignal)
			if !ok {
				t.Fatalf("Plan panicked with %T (%v), want *ErrorSignal", r, r)
			}
			msg = sig.ErrorMessage
		}()
		exec.Plan(ctx, AnnotatedMessages{
			{ChatCompletionMessage: llm.ChatCompletionMessage{Role: llm.ChatMessageRoleUser, Content: "hello"}},
		})
	}()
	return msg
}

// newProbeExecutive builds the smallest executive Plan can run: the planner
// prompt template indexes .Behaviors, so at least one *BehaviorTree is required.
func newProbeExecutive() *TodoListExecutive {
	tree := CreateBehaviorTree("probe", "a probe behavior", "example")
	return CreateTodoListExecutive("probe exec", "probes model routing", &tree)
}

func TestPlanUsesConfiguredModel(t *testing.T) {
	const clusterErr = "unknown model type: cluster"
	const anthropicErr = "ANTHROPIC_TOKEN environment variable not set"

	t.Run("context default reaches the planner", func(t *testing.T) {
		captureLog(t)
		ctx := WithDefaultModel(context.Background(), "cluster:ctx-probe")
		if got := planErrorMessage(t, newProbeExecutive(), ctx); !strings.Contains(got, clusterErr) {
			t.Fatalf("error = %q, want it to contain %q (context default was not used)", got, clusterErr)
		}
	})

	t.Run("PlannerModel reaches the planner", func(t *testing.T) {
		captureLog(t)
		exec := newProbeExecutive()
		exec.PlannerModel = "cluster:planner-probe"
		if got := planErrorMessage(t, exec, context.Background()); !strings.Contains(got, clusterErr) {
			t.Fatalf("error = %q, want it to contain %q (PlannerModel was not used)", got, clusterErr)
		}
	})

	t.Run("PlannerModel takes precedence over the context default", func(t *testing.T) {
		captureLog(t)
		t.Setenv("ANTHROPIC_TOKEN", "")
		exec := newProbeExecutive()
		exec.PlannerModel = "anthropic:planner-probe"
		ctx := WithDefaultModel(context.Background(), "cluster:ctx-probe")
		got := planErrorMessage(t, exec, ctx)
		if !strings.Contains(got, anthropicErr) {
			t.Fatalf("error = %q, want it to contain %q (PlannerModel did not win)", got, anthropicErr)
		}
		if strings.Contains(got, clusterErr) {
			t.Fatalf("error = %q mentions the context default; PlannerModel should have taken precedence", got)
		}
	})

	t.Run("RepairModel does not affect the planner call", func(t *testing.T) {
		captureLog(t)
		exec := newProbeExecutive()
		exec.RepairModel = "cluster:repair-probe"
		ctx := WithDefaultModel(context.Background(), "anthropic:ctx-probe")
		t.Setenv("ANTHROPIC_TOKEN", "")
		got := planErrorMessage(t, exec, ctx)
		if !strings.Contains(got, anthropicErr) {
			t.Fatalf("error = %q, want it to contain %q (planner should use the context default)", got, anthropicErr)
		}
	})
}

// TestCopyPreservesModelFields pins that Copy carries the model
// configuration across, like the rest of the executive's configuration.
// Copy is hand-maintained field by field, so a new field is easy to miss.
func TestCopyPreservesModelFields(t *testing.T) {
	exec := newProbeExecutive()
	exec.OutOfBoundsHandler = exec.Behaviors[0]
	exec.PlannerModel = "anthropic:planner"
	exec.RepairModel = "openai:repair"

	copied, ok := exec.Copy().(*TodoListExecutive)
	if !ok {
		t.Fatalf("Copy() returned %T, want *TodoListExecutive", exec.Copy())
	}
	if copied.PlannerModel != exec.PlannerModel || copied.RepairModel != exec.RepairModel {
		t.Fatalf("Copy() = (PlannerModel %q, RepairModel %q), want (%q, %q)",
			copied.PlannerModel, copied.RepairModel, exec.PlannerModel, exec.RepairModel)
	}
}

// TestFixJSONUsesTheGivenModel pins that repair routes on the model URI it is
// handed. Repair only runs on malformed planner output, which needs a real
// model to produce, so this is as close as the loop can be closed hermetically.
func TestFixJSONUsesTheGivenModel(t *testing.T) {
	_, err := fixJSON(context.Background(), "{not json", "cluster:repair-probe")
	if err == nil || !strings.Contains(err.Error(), "unknown model type: cluster") {
		t.Fatalf("err = %v, want it to route on the given model URI", err)
	}
}

// planResult is one step's result for triage: the signal it ended with and
// the last message its behavior left.
func planResult(sig Signal, last string) PlanResult {
	return PlanResult{
		Step: &ExecGeneratedStep{},
		Messages: AnnotatedMessages{{ChatCompletionMessage: llm.ChatCompletionMessage{
			Role: llm.ChatMessageRoleAssistant, Content: last,
		}}},
		Signal: sig,
	}
}

func TestTriage(t *testing.T) {
	finished := func(last string) PlanResult { return planResult(nil, last) }
	paused := func(last string) PlanResult { return planResult(&CollectUserInputSignal{Reason: "wait"}, last) }
	failed := func(last string) PlanResult { return planResult(&ErrorSignal{ErrorMessage: "boom"}, last) }
	skipped := func(last string) PlanResult { return planResult(&SkipSignal{Reason: "leaked"}, last) }

	cases := []struct {
		name          string
		results       []PlanResult
		wantKept      int
		wantSummaries []string
		wantReply     string
		wantHasReply  bool
	}{
		{
			name:          "one finished step replies as written",
			results:       []PlanResult{finished("A")},
			wantSummaries: []string{"A"},
			wantReply:     "A",
			wantHasReply:  true,
		},
		{
			name:          "one paused step replies as written and is kept",
			results:       []PlanResult{paused("A?")},
			wantKept:      1,
			wantSummaries: []string{"A?"},
			wantReply:     "A?",
			wantHasReply:  true,
		},
		{
			name:          "one failed step is summarized, not replied",
			results:       []PlanResult{failed("A")},
			wantSummaries: []string{"Error occurred: boom"},
		},
		{
			name:          "a finished step beside a paused one: both summarized, paused kept",
			results:       []PlanResult{finished("A"), paused("B?")},
			wantKept:      1,
			wantSummaries: []string{"A", "B?"},
		},
		{
			name:          "a paused step before a finished one: finished is summarized first",
			results:       []PlanResult{paused("A?"), finished("B")},
			wantKept:      1,
			wantSummaries: []string{"B", "A?"},
		},
		{
			name:          "a paused step before a failed one: the error is summarized first",
			results:       []PlanResult{paused("A?"), failed("B")},
			wantKept:      1,
			wantSummaries: []string{"Error occurred: boom", "A?"},
		},
		{
			name:          "two paused steps are both kept",
			results:       []PlanResult{paused("A?"), paused("B?")},
			wantKept:      2,
			wantSummaries: []string{"A?", "B?"},
		},
		{
			name:          "two finished steps are both summarized, none kept",
			results:       []PlanResult{finished("A"), finished("B")},
			wantSummaries: []string{"A", "B"},
		},
		{
			name:    "a leaked skip contributes nothing",
			results: []PlanResult{skipped("A")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := triage(tc.results)
			if len(got.Kept) != tc.wantKept {
				t.Fatalf("kept %d steps, want %d", len(got.Kept), tc.wantKept)
			}
			if strings.Join(got.Summaries, "|") != strings.Join(tc.wantSummaries, "|") {
				t.Fatalf("summaries = %q, want %q", got.Summaries, tc.wantSummaries)
			}
			if got.HasReply != tc.wantHasReply || got.Verbatim != tc.wantReply {
				t.Fatalf("reply = (%q, %v), want (%q, %v)", got.Verbatim, got.HasReply, tc.wantReply, tc.wantHasReply)
			}
		})
	}
}
