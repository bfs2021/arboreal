package arboreal

import (
	"context"
	"encoding/json"
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
	sent  bool
	sends int // replies RunLoop sent; a failed turn must send none
}

func (c *oneShotChannel) AllocateID() string { return "id" }

func (c *oneShotChannel) Send(*ChannelMessage) error {
	c.sends++
	return nil
}

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

// noModel makes the test hermetic: with both tokens empty, any model call
// the framework makes fails, and a failed reply call is returned as a
// signal rather than swallowed. captureLog silences the fallback-model
// warning that a state reached with no default model on the context
// would log.
func noModel(t *testing.T) {
	t.Helper()
	t.Setenv("OPENAI_TOKEN", "")
	t.Setenv("ANTHROPIC_TOKEN", "")
	captureLog(t)
}

// userTurn is a fresh one-message transcript ending on the user.
func userTurn() AnnotatedMessages {
	return AppendToMessages(nil, llm.ChatCompletionMessage{Role: llm.ChatMessageRoleUser, Content: "Hello"})
}

// cannedTree is a one-state tree that replies with text and finishes.
func cannedTree(id, text string) *BehaviorTree {
	tree := CreateBehaviorTreeWithId(id, "replies with "+text, "example", id)
	reply := CannedResponseState(text)
	reply.HashId = id + "-reply"
	reply.StateName = "reply"
	tree.AddState(reply)
	return &tree
}

// pausingTree replies with text and then pauses.
func pausingTree(id, text string) *BehaviorTree {
	tree := CreateBehaviorTreeWithId(id, "replies with "+text+" and waits", "example", id)
	reply := CannedResponseState(text)
	reply.HashId = id + "-reply"
	reply.StateName = "reply"
	wait := PauseState("wait for the user")
	wait.HashId = id + "-wait"
	wait.StateName = "wait"
	tree.AddTransition(reply, &wait)
	return &tree
}

// replyExecutive is an executive over the given trees, with the preamble and
// the out-of-bounds handler every executive should set.
func replyExecutive(trees ...*BehaviorTree) *TodoListExecutive {
	var behaviors []Behavior
	for _, tree := range trees {
		behaviors = append(behaviors, tree)
	}
	exec := CreateTodoListExecutiveWithId("reply exec", "exercises the reply path", "exec-reply", behaviors...)
	exec.Preamble = "You are a test executive."
	exec.OutOfBoundsHandler = CannedResponseState("out of bounds")
	return exec
}

// seedPlan puts one step per tree into exec's plan through Restore, the way
// a planned turn would have, without a planner. Each step's one message is
// a user-role direction, as Plan writes it. The messages are marshalled from
// real values so the field names match the framework's; only the skeleton's
// keys are typed by hand.
func seedPlan(t *testing.T, exec *TodoListExecutive, treeIDs ...string) {
	t.Helper()
	var plan []map[string]any
	for _, id := range treeIDs {
		plan = append(plan, map[string]any{
			"ref":      id,
			"snapshot": map[string]any{},
			"messages": AppendToMessages(nil, llm.ChatCompletionMessage{
				Role: llm.ChatMessageRoleUser, Content: "direction for " + id,
			}),
			"replan_tombstone": false,
		})
	}
	b, err := json.Marshal(map[string]any{exec.Hash(): map[string]any{"plan": plan}})
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	if err := snap.Restore(exec); err != nil {
		t.Fatal(err)
	}
	if n := len(exec.plan); n != len(treeIDs) {
		t.Fatalf("seeded %d steps, want %d: the executive's hash or the plan key is wrong", n, len(treeIDs))
	}
}

// modelCtx carries an explicit default model, so a state whose Model is
// empty resolves to the OpenAI provider and, with no token, fails there,
// whatever becomes of the transitional fallback model.
func modelCtx() context.Context {
	return WithDefaultModel(context.Background(), llm.GPT4oMini)
}

// drainTrace runs fn with a trace channel and the default model on the
// context and returns every message the turn sent, in order.
func drainTrace(fn func(ctx context.Context)) []*TraceMessage {
	trace := make(Trace)
	var got []*TraceMessage
	done := make(chan struct{})
	go func() {
		for m := range trace {
			got = append(got, m)
		}
		close(done)
	}()
	ctx := context.WithValue(modelCtx(), "arboreal_trace", trace) //nolint:staticcheck // the framework reads this string key
	fn(ctx)
	close(trace)
	<-done
	return got
}

// endCall is the end_call trace message of the named sender, or nil.
func endCall(messages []*TraceMessage, name string) *TraceMessage {
	for _, m := range messages {
		if m.Type == TraceMessageTypeCallEnd && m.Name == name {
			return m
		}
	}
	return nil
}

func TestExecuteOutOfBoundsFailureSurfaces(t *testing.T) {
	noModel(t)
	exec := replyExecutive(cannedTree("tree-a", "A"))
	exec.OutOfBoundsHandler = &BehaviorState{
		HashId:    "state-failing-handler",
		StateName: "failing handler",
		Lambda: func(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal) {
			return history, &ErrorSignal{ErrorMessage: "handler failed", ErrorType: StateErrorTypeUnknown}
		},
	}
	// No plan seeded: Execute goes straight to the handler.

	exec.planDepth = 1 // as if a re-plan had led here
	sig := exec.Execute(context.Background(), userTurn())

	es, ok := sig.(*ErrorSignal)
	if !ok {
		t.Fatalf("Execute returned %T (%v), want *ErrorSignal", sig, sig)
	}
	if es.ErrorMessage != "handler failed" {
		t.Fatalf("ErrorMessage = %q, want the handler's", es.ErrorMessage)
	}
	if exec.Output != "" {
		t.Fatalf("Output = %q, want empty: a failed handler must not reply with the user's words", exec.Output)
	}
	if exec.planDepth != 0 {
		t.Fatalf("planDepth = %d after the turn, want 0", exec.planDepth)
	}
}

func TestExecuteOutOfBoundsHandlerReplies(t *testing.T) {
	noModel(t)
	exec := replyExecutive(cannedTree("tree-a", "A"))

	sig := exec.Execute(context.Background(), userTurn())

	if sig != nil {
		t.Fatalf("Execute returned %v, want nil", sig)
	}
	if exec.Output != "out of bounds" {
		t.Fatalf("Output = %q, want the handler's canned reply", exec.Output)
	}
}

// twoFinishingSteps seeds a plan whose two steps both finish, so the reply
// has to be merged and the summarizer runs.
func twoFinishingSteps(t *testing.T) *TodoListExecutive {
	t.Helper()
	exec := replyExecutive(cannedTree("tree-a", "A finished"), cannedTree("tree-b", "B finished"))
	seedPlan(t, exec, "tree-a", "tree-b")
	return exec
}

func TestExecuteSummarizerFailureSurfaces(t *testing.T) {
	noModel(t)
	exec := twoFinishingSteps(t)

	var sig Signal
	traced := drainTrace(func(ctx context.Context) {
		sig = exec.Execute(ctx, userTurn())
	})

	es, ok := sig.(*ErrorSignal)
	if !ok {
		t.Fatalf("Execute returned %T (%v), want *ErrorSignal from the summarizer", sig, sig)
	}
	if !strings.Contains(es.ErrorMessage, "OPENAI_TOKEN") {
		t.Fatalf("ErrorMessage = %q, want the missing-token error", es.ErrorMessage)
	}
	if exec.Output != "" {
		t.Fatalf("Output = %q, want empty: a failed summarizer must not reply with the user's words", exec.Output)
	}
	if n := len(exec.plan); n != 0 {
		t.Fatalf("plan holds %d steps, want 0: both steps finished", n)
	}

	end := endCall(traced, "summarize")
	if end == nil {
		t.Fatal("no end_call named summarize on the trace: the summarizer is not called through the traced wrapper")
	}
	if end.ID != exec.Hash()+"-summarize" {
		t.Fatalf("the summarize end_call has id %q, want the executive's with -summarize appended", end.ID)
	}
	if end.Error == nil {
		t.Fatal("the summarize end_call carries no Error")
	}
	if end.Signal == nil || end.Signal.Type != "error" {
		t.Fatalf("the summarize end_call signal = %+v, want type error", end.Signal)
	}
}

func TestExecuteSummarizerFailureKeepsThePausedSteps(t *testing.T) {
	noModel(t)
	exec := replyExecutive(pausingTree("tree-a", "A asks"), pausingTree("tree-b", "B asks"))
	seedPlan(t, exec, "tree-a", "tree-b")

	sig := exec.Execute(modelCtx(), userTurn())

	// Two steps paused, so the work-in-process summarizer runs and fails.
	// The failed turn still stores the paused steps for the next turn.
	if _, ok := sig.(*ErrorSignal); !ok {
		t.Fatalf("Execute returned %T (%v), want *ErrorSignal from the summarizer", sig, sig)
	}
	if exec.Output != "" {
		t.Fatalf("Output = %q, want empty", exec.Output)
	}
	if n := len(exec.plan); n != 2 {
		t.Fatalf("plan holds %d steps, want the 2 paused ones", n)
	}
}

func TestExecuteLoneFinishedStepRepliesAsWritten(t *testing.T) {
	noModel(t)
	exec := replyExecutive(cannedTree("tree-a", "the reply, as written"))
	seedPlan(t, exec, "tree-a")

	// No default model on the context, on purpose: this path must reach no
	// model, and if it did, the call would fail either way.
	sig := exec.Execute(context.Background(), userTurn())

	if sig != nil {
		t.Fatalf("Execute returned %v, want nil: one step ran, so no summarizer and no model", sig)
	}
	if exec.Output != "the reply, as written" {
		t.Fatalf("Output = %q, want the step's own last message", exec.Output)
	}
	if n := len(exec.plan); n != 0 {
		t.Fatalf("plan holds %d steps after a finished turn, want 0", n)
	}
}

func TestExecuteLonePausedStepRepliesAsWritten(t *testing.T) {
	noModel(t)
	exec := replyExecutive(pausingTree("tree-a", "the question, as written"))
	seedPlan(t, exec, "tree-a")

	sig := exec.Execute(context.Background(), userTurn())

	if sig != nil {
		t.Fatalf("Execute returned %v, want nil", sig)
	}
	if exec.Output != "the question, as written" {
		t.Fatalf("Output = %q, want the step's own last message", exec.Output)
	}
	if n := len(exec.plan); n != 1 {
		t.Fatalf("plan holds %d steps after a paused turn, want 1", n)
	}
}

func TestExecuteFinishedStepBesidePausedIsSummarized(t *testing.T) {
	noModel(t)
	exec := replyExecutive(cannedTree("tree-a", "A finished"), pausingTree("tree-b", "B asks"))
	seedPlan(t, exec, "tree-a", "tree-b")

	sig := exec.Execute(modelCtx(), userTurn())

	// Two steps ran, so the reply is merged by the summarizer, which fails
	// without a token. Before this change the paused step's text came back
	// alone and the finished step's output was dropped.
	es, ok := sig.(*ErrorSignal)
	if !ok {
		t.Fatalf("Execute returned %T (%v) with Output %q; want the summarizer's *ErrorSignal, not the paused step's text alone", sig, sig, exec.Output)
	}
	if !strings.Contains(es.ErrorMessage, "OPENAI_TOKEN") {
		t.Fatalf("ErrorMessage = %q, want the missing-token error", es.ErrorMessage)
	}
	if exec.Output != "" {
		t.Fatalf("Output = %q, want empty", exec.Output)
	}
	if n := len(exec.plan); n != 1 || exec.plan[0].Behavior.Hash() != "tree-b" {
		t.Fatalf("kept plan = %d steps, want the one paused step on tree-b", n)
	}
}

func TestExecuteLonePausedStepBesideTombstoneRepliesAsWritten(t *testing.T) {
	noModel(t)
	exec := replyExecutive(pausingTree("tree-a", "the question, as written"))
	seedPlan(t, exec, "tree-a")
	// A plan that ended in Re-plan carries a tombstone after its last step.
	// seedPlan cannot seed one, since a tombstone references no behavior,
	// so it is appended by hand.
	exec.plan = append(exec.plan, &ExecGeneratedStep{ReplanTombstone: true, Messages: AnnotatedMessages{}})

	sig := exec.Execute(context.Background(), userTurn())

	if sig != nil {
		t.Fatalf("Execute returned %v, want nil", sig)
	}
	if exec.Output != "the question, as written" {
		t.Fatalf("Output = %q, want the paused step's own last message: a carried tombstone is not a step that ran", exec.Output)
	}
	if n := len(exec.plan); n != 2 || !exec.plan[1].ReplanTombstone {
		t.Fatalf("kept plan = %d steps, want the paused step and the carried tombstone", n)
	}
}

func TestCallReturnsTheFailure(t *testing.T) {
	noModel(t)
	exec := twoFinishingSteps(t)
	in := userTurn()

	var out AnnotatedMessages
	var sig Signal
	traced := drainTrace(func(ctx context.Context) {
		out, sig = exec.Call(ctx, in)
	})

	if _, ok := sig.(*ErrorSignal); !ok {
		t.Fatalf("Call returned %T (%v), want the summarizer's *ErrorSignal", sig, sig)
	}
	if len(out) != len(in) {
		t.Fatalf("Call returned %d messages, want the %d it was given: no reply must be appended on failure", len(out), len(in))
	}

	end := endCall(traced, exec.Name())
	if end == nil {
		t.Fatal("no end_call for the executive on the trace")
	}
	if end.Error == nil {
		t.Fatal("the executive's end_call carries no Error")
	}
	if end.Signal == nil || end.Signal.Type != "error" {
		t.Fatalf("the executive's end_call signal = %+v, want type error", end.Signal)
	}
}

func TestCallAppendsTheReply(t *testing.T) {
	noModel(t)
	exec := replyExecutive(cannedTree("tree-a", "the reply, as written"))
	seedPlan(t, exec, "tree-a")
	in := userTurn()

	out, sig := exec.Call(context.Background(), in)

	if sig != nil {
		t.Fatalf("Call returned %v, want nil", sig)
	}
	if len(out) != len(in)+1 || out.LastMessage().Content != "the reply, as written" {
		t.Fatalf("Call returned %d messages ending %q, want the input plus the reply", len(out), out.LastMessage().Content)
	}
}

func TestRunLoopReturnsTheFailure(t *testing.T) {
	noModel(t)
	exec := twoFinishingSteps(t)

	ch := &oneShotChannel{}
	err := exec.RunLoop(modelCtx(), ch)

	if err == nil {
		t.Fatal("RunLoop returned nil, want the summarizer's failure")
	}
	if !strings.Contains(err.Error(), "OPENAI_TOKEN") {
		t.Fatalf("RunLoop error = %q, want it to name the missing token", err)
	}
	if n := len(exec.History); n != 1 || exec.History.LastMessage().Role != llm.ChatMessageRoleUser {
		t.Fatalf("History holds %d messages, want only the user's turn: no empty reply must be appended", n)
	}
	var es *ErrorSignal
	if !errors.As(err, &es) {
		t.Fatalf("RunLoop error %v does not wrap the turn's *ErrorSignal", err)
	}
	if ch.sends != 0 {
		t.Fatalf("RunLoop sent %d replies on a failed turn, want none", ch.sends)
	}
}
