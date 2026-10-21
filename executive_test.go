package arboreal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

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
func (b *ctxCapturingBehavior) Select(transcript AnnotatedMessages) AnnotatedMessages {
	return LastUserMessage(transcript)
}
func (b *ctxCapturingBehavior) Open(direction AnnotatedMessage, transcript AnnotatedMessages) AnnotatedMessages {
	return AnnotatedMessages{direction}
}

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
	exec.SummaryModel = "anthropic:summary"

	copied, ok := exec.Copy().(*TodoListExecutive)
	if !ok {
		t.Fatalf("Copy() returned %T, want *TodoListExecutive", exec.Copy())
	}
	if copied.PlannerModel != exec.PlannerModel || copied.RepairModel != exec.RepairModel || copied.SummaryModel != exec.SummaryModel {
		t.Fatalf("Copy() = (PlannerModel %q, RepairModel %q, SummaryModel %q), want (%q, %q, %q)",
			copied.PlannerModel, copied.RepairModel, copied.SummaryModel, exec.PlannerModel, exec.RepairModel, exec.SummaryModel)
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

// toolTree is a tree whose first state appends one executed tool call the
// way LLMCompletionState's loop does (an assistant message carrying the
// call, then a function-role message carrying the result), then replies
// with text and finishes, or pauses when wait is true.
func toolTree(id, tool string, args map[string]any, result, text string, wait bool) *BehaviorTree {
	return toolTreeFrom(id, tool, func(AnnotatedMessages) (map[string]any, string) { return args, result }, text, wait)
}

// echoTree is toolTree with the call's one argument and its result taken
// from the last message the state was given, so each turn's record is
// distinct and a test can tell whose turn a record came from.
func echoTree(id, tool, text string, wait bool) *BehaviorTree {
	return toolTreeFrom(id, tool, func(history AnnotatedMessages) (map[string]any, string) {
		said := history.LastMessage().Content
		return map[string]any{"said": said}, "heard " + said
	}, text, wait)
}

// toolTreeFrom is toolTree's shape with the call's arguments and result
// computed from the history the first state is given.
func toolTreeFrom(id, tool string, call func(AnnotatedMessages) (map[string]any, string), text string, wait bool) *BehaviorTree {
	tree := CreateBehaviorTreeWithId(id, "calls "+tool+" and replies with "+text, "example", id)
	first := &BehaviorState{
		HashId:    id + "-call",
		StateName: "call",
		Lambda: func(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal) {
			args, result := call(history)
			return append(history,
				AnnotatedMessage{ChatCompletionMessage: llm.ChatCompletionMessage{
					Role:      llm.ChatMessageRoleAssistant,
					ToolCalls: []llm.ToolCall{{Name: tool, Arguments: args}},
				}},
				AnnotatedMessage{ChatCompletionMessage: llm.ChatCompletionMessage{
					Role:    llm.ChatMessageRoleFunction,
					Name:    tool,
					Content: result,
				}},
			), nil
		},
	}
	reply := CannedResponseState(text)
	reply.HashId = id + "-reply"
	reply.StateName = "reply"
	tree.AddTransition(first, reply)
	if wait {
		pause := PauseState("wait for the user")
		pause.HashId = id + "-wait"
		pause.StateName = "wait"
		tree.AddTransition(reply, &pause)
	}
	return &tree
}

// delayed makes tree's first state sleep for d before it runs, so a test
// that fans out two steps can pin which one completes last. The seeded copy
// shares the state by pointer, so wrapping the original is enough.
func delayed(tree *BehaviorTree, d time.Duration) *BehaviorTree {
	first := tree.Graph.Initial().(*BehaviorState)
	inner := first.Lambda
	first.Lambda = func(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal) {
		time.Sleep(d)
		return inner(ctx, history)
	}
	return tree
}

// replyExecutive is an executive over the given trees, with the preamble and
// the out-of-bounds handler every executive should set.
func replyExecutive(trees ...*BehaviorTree) *TodoListExecutive {
	var behaviors []Step
	for _, tree := range trees {
		behaviors = append(behaviors, tree)
	}
	exec := CreateTodoListExecutiveWithId("reply exec", "exercises the reply path", "exec-reply", behaviors...)
	exec.Preamble = "You are a test executive."
	exec.ClientID = "client-reply"
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
	if n := len(exec.plan); n != 0 {
		t.Fatalf("plan holds %d steps after a failed handler, want 0", n)
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
	if n := len(exec.ToolCalls); n != 0 {
		t.Fatalf("ToolCalls holds %d records, want none: the handler called no tool", n)
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
	if end.ClientID != exec.ClientID {
		t.Fatalf("the summarize end_call has client id %q, want the executive's %q", end.ClientID, exec.ClientID)
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

// summaryErrorMessage runs Execute on an executive whose seeded steps make
// the summarizer run and returns the message of the *ErrorSignal it comes
// back with. Every case below configures a model that cannot produce a
// provider, so reaching the summarizer's model call at all is the assertion.
func summaryErrorMessage(t *testing.T, exec *TodoListExecutive, ctx context.Context) string {
	t.Helper()
	sig := exec.Execute(ctx, userTurn())
	es, ok := sig.(*ErrorSignal)
	if !ok {
		t.Fatalf("Execute returned %T (%v), want the summarizer's *ErrorSignal for an unresolvable model", sig, sig)
	}
	return es.ErrorMessage
}

func TestExecuteSummarizerUsesConfiguredModel(t *testing.T) {
	const clusterErr = "unknown model type: cluster"
	const anthropicErr = "ANTHROPIC_TOKEN environment variable not set"

	t.Run("context default reaches the summarizer", func(t *testing.T) {
		noModel(t)
		ctx := WithDefaultModel(context.Background(), "cluster:ctx-probe")
		if got := summaryErrorMessage(t, twoFinishingSteps(t), ctx); !strings.Contains(got, clusterErr) {
			t.Fatalf("error = %q, want it to contain %q (context default was not used)", got, clusterErr)
		}
	})

	t.Run("SummaryModel reaches the summarizer", func(t *testing.T) {
		noModel(t)
		exec := twoFinishingSteps(t)
		exec.SummaryModel = "cluster:summary-probe"
		if got := summaryErrorMessage(t, exec, context.Background()); !strings.Contains(got, clusterErr) {
			t.Fatalf("error = %q, want it to contain %q (SummaryModel was not used)", got, clusterErr)
		}
	})

	t.Run("SummaryModel takes precedence over the context default", func(t *testing.T) {
		noModel(t)
		exec := twoFinishingSteps(t)
		exec.SummaryModel = "anthropic:summary-probe"
		ctx := WithDefaultModel(context.Background(), "cluster:ctx-probe")
		got := summaryErrorMessage(t, exec, ctx)
		if !strings.Contains(got, anthropicErr) {
			t.Fatalf("error = %q, want it to contain %q (SummaryModel did not win)", got, anthropicErr)
		}
		if strings.Contains(got, clusterErr) {
			t.Fatalf("error = %q mentions the context default; SummaryModel should have taken precedence", got)
		}
	})

	t.Run("PlannerModel and RepairModel do not reach the summarizer", func(t *testing.T) {
		noModel(t)
		exec := twoFinishingSteps(t)
		exec.PlannerModel = "cluster:planner-probe"
		exec.RepairModel = "cluster:repair-probe"
		ctx := WithDefaultModel(context.Background(), "anthropic:ctx-probe")
		got := summaryErrorMessage(t, exec, ctx)
		if !strings.Contains(got, anthropicErr) {
			t.Fatalf("error = %q, want it to contain %q (the summarizer should use the context default, not inherit the planner's model)", got, anthropicErr)
		}
		if strings.Contains(got, clusterErr) {
			t.Fatalf("error = %q mentions the planner's model; the summarizer must not inherit it", got)
		}
	})
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

// toolCall is an assistant message carrying one tool call per name given,
// each with {"q": name} as its arguments.
func toolCall(tools ...string) AnnotatedMessage {
	var calls []llm.ToolCall
	for _, tool := range tools {
		calls = append(calls, llm.ToolCall{Name: tool, Arguments: map[string]any{"q": tool}})
	}
	return AnnotatedMessage{ChatCompletionMessage: llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleAssistant, ToolCalls: calls,
	}}
}

// toolResult is the function-role message LLMCompletionState's loop appends
// after executing a call to tool.
func toolResult(tool string) AnnotatedMessage {
	return AnnotatedMessage{ChatCompletionMessage: llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleFunction, Name: tool, Content: "result of " + tool,
	}}
}

func TestRecordToolCallsPairsResultsWithTheirCalls(t *testing.T) {
	text := AnnotatedMessage{ChatCompletionMessage: llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleAssistant, Content: "a reply",
	}}
	record := func(tool string) ToolCallRecord {
		return ToolCallRecord{Step: "step", Tool: tool, Arguments: map[string]any{"q": tool}, Result: "result of " + tool}
	}

	cases := []struct {
		name string
		part AnnotatedMessages
		want []ToolCallRecord
	}{
		{
			name: "one pair",
			part: AnnotatedMessages{toolCall("a"), toolResult("a")},
			want: []ToolCallRecord{record("a")},
		},
		{
			name: "two pairs, in order",
			part: AnnotatedMessages{toolCall("a"), toolResult("a"), toolCall("b"), toolResult("b")},
			want: []ToolCallRecord{record("a"), record("b")},
		},
		{
			name: "a result whose predecessor names another tool is skipped",
			part: AnnotatedMessages{toolCall("a"), toolResult("b")},
		},
		{
			name: "a result at the start of the part is skipped",
			part: AnnotatedMessages{toolResult("a"), text},
		},
		{
			name: "two calls in one reply and one result: the first call",
			part: AnnotatedMessages{toolCall("a", "b"), toolResult("a")},
			want: []ToolCallRecord{record("a")},
		},
		{
			name: "a call with no result records nothing",
			part: AnnotatedMessages{toolCall("a"), text},
		},
		{
			name: "a second result after one call is not paired",
			part: AnnotatedMessages{toolCall("a"), toolResult("a"), toolResult("a")},
			want: []ToolCallRecord{record("a")},
		},
		{
			name: "a result naming a call that is not the first is skipped",
			part: AnnotatedMessages{toolCall("a", "b"), toolResult("b")},
		},
		{
			name: "an empty part",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recordToolCalls("step", tc.part)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("recordToolCalls = %#v, want %#v", got, tc.want)
			}
		})
	}

	// The record's Arguments is a copy of the call's map, not the map: the
	// step's list and the record must not share one.
	part := AnnotatedMessages{toolCall("a"), toolResult("a")}
	got := recordToolCalls("step", part)
	got[0].Arguments["q"] = "changed"
	if part[0].ToolCalls[0].Arguments["q"] != "a" {
		t.Fatal("the record shares the call's Arguments map; it must be a copy")
	}
}

func TestExecuteRecordsALoneFinishedStepsCalls(t *testing.T) {
	noModel(t)
	args := map[string]any{"title": "Dune"}
	exec := replyExecutive(toolTree("tree-a", "lookup", args, "1 in stock", "We have one copy.", false))
	seedPlan(t, exec, "tree-a")

	sig := exec.Execute(context.Background(), userTurn())

	if sig != nil {
		t.Fatalf("Execute returned %v, want nil", sig)
	}
	if exec.Output != "We have one copy." {
		t.Fatalf("Output = %q, want the step's reply as written", exec.Output)
	}
	if n := len(exec.plan); n != 0 {
		t.Fatalf("plan holds %d steps after a finished turn, want 0", n)
	}
	want := []ToolCallRecord{{Step: "tree-a", Tool: "lookup", Arguments: args, Result: "1 in stock"}}
	if !reflect.DeepEqual(exec.ToolCalls, want) {
		t.Fatalf("ToolCalls = %+v, want %+v", exec.ToolCalls, want)
	}
}

func TestExecuteRecordsEveryStepInPlanOrder(t *testing.T) {
	noModel(t)
	a := delayed(toolTree("tree-a", "lookup", map[string]any{"title": "Dune"}, "1 in stock", "A done", false), 20*time.Millisecond)
	b := toolTree("tree-b", "notify", map[string]any{"to": "staff"}, "sent", "B done", false)
	exec := replyExecutive(a, b)
	seedPlan(t, exec, "tree-a", "tree-b")
	in := userTurn()

	// Two steps ran, so the summarizer is called and, with no token, fails:
	// this is the failed-turn test too. tree-a sleeps, so it completes
	// second; the record is in plan order regardless.
	out, sig := exec.Call(modelCtx(), in)

	if _, ok := sig.(*ErrorSignal); !ok {
		t.Fatalf("Call returned %T (%v), want the summarizer's *ErrorSignal", sig, sig)
	}
	if len(out) != len(in) {
		t.Fatalf("Call returned %d messages, want the %d it was given: a failed turn appends no reply", len(out), len(in))
	}
	want := []ToolCallRecord{
		{Step: "tree-a", Tool: "lookup", Arguments: map[string]any{"title": "Dune"}, Result: "1 in stock"},
		{Step: "tree-b", Tool: "notify", Arguments: map[string]any{"to": "staff"}, Result: "sent"},
	}
	if !reflect.DeepEqual(exec.ToolCalls, want) {
		t.Fatalf("ToolCalls = %+v, want %+v, in plan order", exec.ToolCalls, want)
	}
}

func TestExecuteRecordsTheOutOfBoundsHandlersCalls(t *testing.T) {
	noModel(t)
	exec := replyExecutive(cannedTree("tree-a", "A"))
	exec.OutOfBoundsHandler = &BehaviorState{
		HashId:    "state-tool-handler",
		StateName: "tool handler",
		Lambda: func(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal) {
			return append(history,
				AnnotatedMessage{ChatCompletionMessage: llm.ChatCompletionMessage{
					Role:      llm.ChatMessageRoleAssistant,
					ToolCalls: []llm.ToolCall{{Name: "file_feedback", Arguments: map[string]any{"text": "Hello"}}},
				}},
				AnnotatedMessage{ChatCompletionMessage: llm.ChatCompletionMessage{
					Role: llm.ChatMessageRoleFunction, Name: "file_feedback", Content: "issue 42",
				}},
				AnnotatedMessage{ChatCompletionMessage: llm.ChatCompletionMessage{
					Role: llm.ChatMessageRoleAssistant, Content: "Filed as issue 42.",
				}},
			), nil
		},
	}
	// No plan seeded: Execute goes straight to the handler.

	sig := exec.Execute(context.Background(), userTurn())

	if sig != nil {
		t.Fatalf("Execute returned %v, want nil", sig)
	}
	if exec.Output != "Filed as issue 42." {
		t.Fatalf("Output = %q, want the handler's reply", exec.Output)
	}
	want := []ToolCallRecord{{Step: "tool handler", Tool: "file_feedback", Arguments: map[string]any{"text": "Hello"}, Result: "issue 42"}}
	if !reflect.DeepEqual(exec.ToolCalls, want) {
		t.Fatalf("ToolCalls = %+v, want %+v with the handler's Name() as Step", exec.ToolCalls, want)
	}
}

func TestExecuteRecordsAFailedHandlersCalls(t *testing.T) {
	noModel(t)
	exec := replyExecutive(cannedTree("tree-a", "A"))
	exec.OutOfBoundsHandler = &BehaviorState{
		HashId:    "state-failing-tool-handler",
		StateName: "failing tool handler",
		Lambda: func(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal) {
			return append(history,
				AnnotatedMessage{ChatCompletionMessage: llm.ChatCompletionMessage{
					Role:      llm.ChatMessageRoleAssistant,
					ToolCalls: []llm.ToolCall{{Name: "file_feedback", Arguments: map[string]any{"text": "Hello"}}},
				}},
				AnnotatedMessage{ChatCompletionMessage: llm.ChatCompletionMessage{
					Role: llm.ChatMessageRoleFunction, Name: "file_feedback", Content: "issue 42",
				}},
			), &ErrorSignal{ErrorMessage: "reply failed", ErrorType: StateErrorTypeUnknown}
		},
	}

	sig := exec.Execute(context.Background(), userTurn())

	// The tool ran before the handler failed. The framework has no undo for
	// it, so the record says that it did, on the field, while Output stays
	// empty.
	es, ok := sig.(*ErrorSignal)
	if !ok || es.ErrorMessage != "reply failed" {
		t.Fatalf("Execute returned %T (%v), want the handler's *ErrorSignal", sig, sig)
	}
	if exec.Output != "" {
		t.Fatalf("Output = %q, want empty", exec.Output)
	}
	want := []ToolCallRecord{{Step: "failing tool handler", Tool: "file_feedback", Arguments: map[string]any{"text": "Hello"}, Result: "issue 42"}}
	if !reflect.DeepEqual(exec.ToolCalls, want) {
		t.Fatalf("ToolCalls = %+v, want %+v: a failed handler's calls are recorded too", exec.ToolCalls, want)
	}
}

// recordOn decodes the tool-call record on m, failing the test if m carries
// none or it does not decode.
func recordOn(t *testing.T, m *AnnotatedMessage) []ToolCallRecord {
	t.Helper()
	a, ok := m.Annotations[AnnotationToolCalls]
	if !ok {
		t.Fatalf("the message %q carries no %q annotation; it has %v", m.Content, AnnotationToolCalls, m.Annotations)
	}
	got, err := ToolCallsFromAnnotation(&a)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCallAttachesTheRecordToTheReply(t *testing.T) {
	noModel(t)
	args := map[string]any{"title": "Dune"}
	exec := replyExecutive(toolTree("tree-a", "lookup", args, "1 in stock", "We have one copy.", false))
	seedPlan(t, exec, "tree-a")
	in := userTurn()

	out, sig := exec.Call(context.Background(), in)

	if sig != nil {
		t.Fatalf("Call returned %v, want nil", sig)
	}
	if len(out) != len(in)+1 || out.LastMessage().Content != "We have one copy." {
		t.Fatalf("Call returned %d messages ending %q, want the input plus the reply", len(out), out.LastMessage().Content)
	}
	want := []ToolCallRecord{{Step: "tree-a", Tool: "lookup", Arguments: args, Result: "1 in stock"}}
	if got := recordOn(t, out.LastMessage()); !reflect.DeepEqual(got, want) {
		t.Fatalf("the reply's record = %+v, want %+v", got, want)
	}
	for i, m := range out[:len(in)] {
		if _, ok := m.Annotations[AnnotationToolCalls]; ok {
			t.Fatalf("input message %d gained a %q annotation; only the reply carries it", i, AnnotationToolCalls)
		}
	}
}

func TestCallAttachesNoRecordWithoutCalls(t *testing.T) {
	noModel(t)
	exec := replyExecutive(cannedTree("tree-a", "the reply, as written"))
	seedPlan(t, exec, "tree-a")

	out, sig := exec.Call(context.Background(), userTurn())

	if sig != nil {
		t.Fatalf("Call returned %v, want nil", sig)
	}
	if _, ok := out.LastMessage().Annotations[AnnotationToolCalls]; ok {
		t.Fatalf("the reply carries a %q annotation on a turn with no tool call; it must be absent, not empty", AnnotationToolCalls)
	}
	if n := len(exec.ToolCalls); n != 0 {
		t.Fatalf("ToolCalls holds %d records, want none", n)
	}
}

func TestAPausedStepsReplyRecordsThisTurnOnly(t *testing.T) {
	noModel(t)
	exec := replyExecutive(echoTree("tree-a", "lookup", "Want it held?", true))
	seedPlan(t, exec, "tree-a")

	one, sig := exec.Call(context.Background(), userTurn())
	if sig != nil {
		t.Fatalf("turn one: Call returned %v, want nil", sig)
	}
	wantOne := []ToolCallRecord{{Step: "tree-a", Tool: "lookup", Arguments: map[string]any{"said": "Hello"}, Result: "heard Hello"}}
	if !reflect.DeepEqual(exec.ToolCalls, wantOne) {
		t.Fatalf("turn one recorded %+v, want %+v", exec.ToolCalls, wantOne)
	}
	firstReply := len(one) - 1

	// The step is paused and kept; its list now holds turn one's call. The
	// tree runs from its first state again on resume and calls the tool
	// once more, so the list holds two calls, and the record must hold
	// only the second.
	two, sig := exec.Call(context.Background(), AppendToMessages(one, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Yes please",
	}))
	if sig != nil {
		t.Fatalf("turn two: Call returned %v, want nil", sig)
	}
	wantTwo := []ToolCallRecord{{Step: "tree-a", Tool: "lookup", Arguments: map[string]any{"said": "Yes please"}, Result: "heard Yes please"}}
	if !reflect.DeepEqual(exec.ToolCalls, wantTwo) {
		t.Fatalf("turn two recorded %+v, want %+v: the step's earlier turn must not be re-read", exec.ToolCalls, wantTwo)
	}
	if n := len(two); n != len(one)+2 {
		t.Fatalf("turn two returned %d messages, want %d: the new user message and the reply", n, len(one)+2)
	}
	if got := recordOn(t, two.LastMessage()); !reflect.DeepEqual(got, wantTwo) {
		t.Fatalf("turn two's reply carries %+v, want %+v", got, wantTwo)
	}
	if got := recordOn(t, &two[firstReply]); !reflect.DeepEqual(got, wantOne) {
		t.Fatalf("turn one's reply carries %+v after turn two, want its own %+v", got, wantOne)
	}
}

func TestARecordAfterRestoreHoldsOnlyTheNewTurn(t *testing.T) {
	noModel(t)
	build := func() *TodoListExecutive {
		return replyExecutive(echoTree("tree-a", "lookup", "Want it held?", true))
	}
	first := build()
	seedPlan(t, first, "tree-a")

	one, sig := first.Call(context.Background(), userTurn())
	if sig != nil {
		t.Fatalf("turn one: Call returned %v, want nil", sig)
	}

	// Between the turns: snapshot the paused executive, round-trip it
	// through JSON as a store would, and restore into a fresh one built the
	// same way: chapter 13's pattern, with the JSON leg a store adds.
	snap, err := TakeSnapshot(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var stored Snapshot
	if err := json.Unmarshal(b, &stored); err != nil {
		t.Fatal(err)
	}
	second := build()
	if err := stored.Restore(second); err != nil {
		t.Fatal(err)
	}

	two, sig := second.Call(context.Background(), AppendToMessages(one, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Yes please",
	}))
	if sig != nil {
		t.Fatalf("turn two: Call returned %v, want nil", sig)
	}
	want := []ToolCallRecord{{Step: "tree-a", Tool: "lookup", Arguments: map[string]any{"said": "Yes please"}, Result: "heard Yes please"}}
	if !reflect.DeepEqual(second.ToolCalls, want) {
		t.Fatalf("turn two after a restore recorded %+v, want %+v: the restored step's earlier turn must not be re-read", second.ToolCalls, want)
	}
	if got := recordOn(t, two.LastMessage()); !reflect.DeepEqual(got, want) {
		t.Fatalf("the reply's record = %+v, want %+v", got, want)
	}
}

func TestRunLoopAttachesTheRecord(t *testing.T) {
	noModel(t)
	args := map[string]any{"title": "Dune"}
	exec := replyExecutive(toolTree("tree-a", "lookup", args, "1 in stock", "We have one copy.", false))
	seedPlan(t, exec, "tree-a")

	ch := &oneShotChannel{}
	err := exec.RunLoop(context.Background(), ch)

	// RunLoop returns on the channel's EOF, not on a failed turn.
	if err == nil || err.Error() != "eof" {
		t.Fatalf("RunLoop returned %v, want the channel's eof", err)
	}
	if ch.sends != 1 {
		t.Fatalf("RunLoop sent %d replies, want 1", ch.sends)
	}
	last := exec.History.LastMessage()
	if last == nil || last.Role != llm.ChatMessageRoleAssistant || last.Content != "We have one copy." {
		t.Fatalf("History ends on %+v, want the reply", last)
	}
	want := []ToolCallRecord{{Step: "tree-a", Tool: "lookup", Arguments: args, Result: "1 in stock"}}
	if got := recordOn(t, last); !reflect.DeepEqual(got, want) {
		t.Fatalf("History's reply carries %+v, want %+v", got, want)
	}
}

func TestRestoreRefusesAPlanStepThatIsNotAStep(t *testing.T) {
	exec := replyExecutive(cannedTree("tree-a", "A"))
	// cannedTree pins its one state's hash to "tree-a-reply". A plan skeleton
	// that refers to the state instead of the tree asks Restore to make a
	// bare state a plan step.
	b, err := json.Marshal(map[string]any{exec.Hash(): map[string]any{"plan": []map[string]any{{
		"ref":              "tree-a-reply",
		"snapshot":         map[string]any{},
		"messages":         AnnotatedMessages{},
		"replan_tombstone": false,
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}

	err = snap.Restore(exec)

	if err == nil {
		t.Fatal("Restore accepted a state as a plan step, want an error")
	}
	if !strings.Contains(err.Error(), "tree-a-reply") {
		t.Fatalf("error = %q, want it to name the ref", err)
	}
	if n := len(exec.plan); n != 0 {
		t.Fatalf("plan holds %d steps after a refused restore, want 0", n)
	}
}

func TestRestoreRefusesAnUnknownRef(t *testing.T) {
	exec := replyExecutive(cannedTree("tree-a", "A"))
	b, err := json.Marshal(map[string]any{exec.Hash(): map[string]any{"plan": []map[string]any{{
		"ref":              "tree-missing",
		"snapshot":         map[string]any{},
		"messages":         AnnotatedMessages{},
		"replan_tombstone": false,
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}

	err = snap.Restore(exec)

	if err == nil {
		t.Fatal("Restore accepted a plan step whose ref names no behavior, want an error")
	}
	if !strings.Contains(err.Error(), "tree-missing") {
		t.Fatalf("error = %q, want it to name the ref", err)
	}
	if n := len(exec.plan); n != 0 {
		t.Fatalf("plan holds %d steps after a refused restore, want 0", n)
	}
}

func TestRestoreLeavesTheExecutiveUntouchedWhenAStepIsRefused(t *testing.T) {
	exec := replyExecutive(cannedTree("tree-a", "A"), cannedTree("tree-b", "B"))
	// A history and a valid step come before the step whose ref names no
	// behavior. None of it may be installed.
	b, err := json.Marshal(map[string]any{exec.Hash(): map[string]any{
		"history": AppendToMessages(nil, llm.ChatCompletionMessage{Role: llm.ChatMessageRoleUser, Content: "Hello"}),
		"plan": []map[string]any{
			{"ref": "tree-a", "snapshot": map[string]any{}, "messages": AnnotatedMessages{}, "replan_tombstone": false},
			{"ref": "tree-missing", "snapshot": map[string]any{}, "messages": AnnotatedMessages{}, "replan_tombstone": false},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}

	err = snap.Restore(exec)

	if err == nil {
		t.Fatal("Restore accepted a plan with an unknown ref, want an error")
	}
	if n := len(exec.plan); n != 0 {
		t.Fatalf("plan holds %d steps after a refused restore, want 0: the valid step before the bad one must not be installed", n)
	}
	if n := len(exec.History); n != 0 {
		t.Fatalf("History holds %d messages after a refused restore, want 0", n)
	}
}

// stepFor is the pending step on the tree with the given id. Kept steps are
// stored in completion order, so a test with two steps looks them up.
func stepFor(t *testing.T, exec *TodoListExecutive, treeID string) *ExecGeneratedStep {
	t.Helper()
	for _, step := range exec.plan {
		if !step.ReplanTombstone && step.Behavior.Hash() == treeID {
			return step
		}
	}
	t.Fatalf("no pending step on %s", treeID)
	return nil
}

func TestCallResumesEachStepWithWhatItSelects(t *testing.T) {
	noModel(t)
	last := pausingTree("tree-last", "last asks")
	all := pausingTree("tree-all", "all asks")
	all.Intake = TrailingUserMessages
	exec := replyExecutive(last, all)
	seedPlan(t, exec, "tree-last", "tree-all")

	// Two user messages arrived since the agent last spoke.
	messages := transcript("assistant", "welcome", "user", "first email", "user", "second email")
	_, sig := exec.Call(modelCtx(), messages)

	// Both steps paused, so the summarizer ran and failed without a token;
	// the plan keeps both, and each step's list records what it was
	// resumed with: its direction, its selection, then its own reply.
	if _, ok := sig.(*ErrorSignal); !ok {
		t.Fatalf("Call returned %v, want the summarizer's failure", sig)
	}
	if n := len(exec.plan); n != 2 {
		t.Fatalf("plan holds %d steps, want the 2 paused ones", n)
	}
	got := contents(stepFor(t, exec, "tree-last").Messages)
	want := []string{"direction for tree-last", "second email", "last asks"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the default step was resumed with %v, want %v", got, want)
	}
	got = contents(stepFor(t, exec, "tree-all").Messages)
	want = []string{"direction for tree-all", "first email", "second email", "all asks"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the trailing step was resumed with %v, want %v", got, want)
	}
}

func TestCallDefaultIntakeDoesNotRedeliverWhenAnotherMessageLanded(t *testing.T) {
	noModel(t)
	exec := replyExecutive(pausingTree("tree-a", "A asks"), pausingTree("tree-b", "B asks"))
	seedPlan(t, exec, "tree-a", "tree-b")

	// Turn 1: both steps pause, the summarizer fails, and the transcript
	// comes back without a reply while the steps keep what they received.
	messages := transcript("assistant", "welcome", "user", "first")
	messages, sig := exec.Call(modelCtx(), messages)
	if _, ok := sig.(*ErrorSignal); !ok {
		t.Fatalf("turn 1 returned %v, want the summarizer's failure", sig)
	}

	// Turn 2 on the same executive with one more message. The transcript
	// still ends on "first", so "since the agent last spoke" covers both
	// messages, but the default selects only the newest and "first" is not
	// delivered twice. TrailingUserMessages would deliver it again; its doc
	// comment says so.
	messages = AppendToMessages(messages, llm.ChatCompletionMessage{Role: llm.ChatMessageRoleUser, Content: "second"})
	exec.Call(modelCtx(), messages)

	got := contents(stepFor(t, exec, "tree-a").Messages)
	want := []string{"direction for tree-a", "first", "A asks", "second", "A asks"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after a failed turn and a retry the step holds %v, want %v", got, want)
	}
}

func TestCallTrailingIntakeRedeliversAfterAFailedTurn(t *testing.T) {
	noModel(t)
	a := pausingTree("tree-a", "A asks")
	a.Intake = TrailingUserMessages
	exec := replyExecutive(a, pausingTree("tree-b", "B asks"))
	seedPlan(t, exec, "tree-a", "tree-b")

	messages := transcript("assistant", "welcome", "user", "first")
	messages, sig := exec.Call(modelCtx(), messages)
	if _, ok := sig.(*ErrorSignal); !ok {
		t.Fatalf("turn 1 returned %v, want the summarizer's failure", sig)
	}

	// The failed turn left no reply on the transcript, so "since the agent
	// last spoke" still covers "first", and the trailing rule hands it to
	// the step a second time. This pins what TrailingUserMessages' doc
	// comment says. A driver that retries from the snapshot taken before
	// the failure never sees it.
	messages = AppendToMessages(messages, llm.ChatCompletionMessage{Role: llm.ChatMessageRoleUser, Content: "second"})
	exec.Call(modelCtx(), messages)

	got := contents(stepFor(t, exec, "tree-a").Messages)
	want := []string{"direction for tree-a", "first", "A asks", "first", "second", "A asks"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after a failed turn and a retry the trailing step holds %v, want %v", got, want)
	}
}

func TestCallLeavesTheTombstoneAloneOnResume(t *testing.T) {
	noModel(t)
	exec := replyExecutive(pausingTree("tree-a", "the question, as written"))
	seedPlan(t, exec, "tree-a")
	exec.plan = append(exec.plan, &ExecGeneratedStep{ReplanTombstone: true, Messages: AnnotatedMessages{}})

	_, sig := exec.Call(context.Background(), transcript("assistant", "welcome", "user", "a"))

	if sig != nil {
		t.Fatalf("Call returned %v, want nil", sig)
	}
	if n := len(exec.plan); n != 2 || !exec.plan[1].ReplanTombstone {
		t.Fatalf("kept plan = %d steps, want the paused step and the carried tombstone", n)
	}
	if n := len(exec.plan[1].Messages); n != 0 {
		t.Fatalf("the tombstone took %d messages on resume, want none: it has no behavior to select", n)
	}
	if got := contents(exec.plan[0].Messages); !reflect.DeepEqual(got, []string{"direction for tree-a", "a", "the question, as written"}) {
		t.Fatalf("the paused step was resumed with %v", got)
	}
}

func TestCallResumesWithNothingWhenTheStepSelectsNothing(t *testing.T) {
	noModel(t)
	exec := replyExecutive(pausingTree("tree-a", "the question"))
	seedPlan(t, exec, "tree-a")

	// The transcript ends on the assistant, so the default selects nothing.
	// The step still runs, on its previous messages alone. The old loop
	// would have appended the assistant's message.
	_, sig := exec.Call(context.Background(), transcript("user", "a", "assistant", "welcome"))

	if sig != nil {
		t.Fatalf("Call returned %v, want nil", sig)
	}
	got := contents(stepFor(t, exec, "tree-a").Messages)
	want := []string{"direction for tree-a", "the question"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the step was resumed with %v, want %v: nothing was selected", got, want)
	}
}

func TestRunLoopResumesWithWhatTheStepSelects(t *testing.T) {
	noModel(t)
	tree := pausingTree("tree-a", "the question")
	tree.Intake = TrailingUserMessages
	exec := replyExecutive(tree)
	seedPlan(t, exec, "tree-a")
	// One message is already on History when the channel delivers "Hello".
	exec.History = transcript("assistant", "welcome", "user", "first")

	// The channel delivers one message and then EOF, which ends the loop.
	_ = exec.RunLoop(context.Background(), &oneShotChannel{})

	got := contents(stepFor(t, exec, "tree-a").Messages)
	want := []string{"direction for tree-a", "first", "Hello", "the question"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RunLoop resumed the step with %v, want %v", got, want)
	}
}

// The tests below drive openStep, the one place Plan builds a behavior's step, without
// the planner: Plan runs its model through LLMCompletionState, which cannot
// be faked, so the opening logic is tested on its own.

func TestOpenStepDefaultsToTheDirection(t *testing.T) {
	tree := cannedTree("tree-a", "A")
	messages := transcript("user", "first", "user", "second")
	messages[0].Annotations["$context"] = Annotation{Name: "$context", Data: "the context"}

	step := openStep(tree, "the direction", messages)

	if step.ReplanTombstone {
		t.Fatal("a planned behavior is not a tombstone")
	}
	if step.Behavior.(*BehaviorTree) == tree {
		t.Fatal("the step runs on the tree itself, not on a copy")
	}
	if got := step.Behavior.Hash(); got != "tree-a" {
		t.Fatalf("the step's behavior is %q, want the copy of tree-a", got)
	}
	if got := contents(step.Messages); !reflect.DeepEqual(got, []string{"the direction"}) {
		t.Fatalf("the step opened with %v, want the direction alone", got)
	}
	first := step.Messages[0]
	if first.Role != llm.ChatMessageRoleUser {
		t.Fatalf("the direction has role %q, want user", first.Role)
	}
	if got := first.Annotations["raw_history"].Data; !reflect.DeepEqual(got, messages) {
		t.Fatalf("raw_history holds %v, want the transcript at planning time", got)
	}
	if got := first.Annotations["$context"].Data; !reflect.DeepEqual(got, messages.GetAnnotation("$context")) {
		t.Fatalf("$context holds %v, want what the transcript's GetAnnotation returned", got)
	}
	if n := len(first.Annotations); n != 2 {
		t.Fatalf("the direction carries %d annotations, want the two pins only", n)
	}
}

func TestOpenStepHandsTheOpenerTheDirectionAndTheTranscript(t *testing.T) {
	tree := cannedTree("tree-a", "A")
	var gotDirection AnnotatedMessage
	var gotTranscript AnnotatedMessages
	tree.Opening = func(d AnnotatedMessage, tr AnnotatedMessages) AnnotatedMessages {
		gotDirection, gotTranscript = d, tr
		return AnnotatedMessages{d}
	}
	messages := transcript("assistant", "hi", "user", "the words")

	openStep(tree, "the direction", messages)

	if gotDirection.Role != llm.ChatMessageRoleUser || gotDirection.Content != "the direction" {
		t.Fatalf("the opener was handed %+v, want a user-role message holding the direction", gotDirection.ChatCompletionMessage)
	}
	if gotDirection.Annotations == nil || len(gotDirection.Annotations) != 0 {
		t.Fatalf("the direction reached the opener with annotations %v, want an empty, non-nil map: the pins land after the opening", gotDirection.Annotations)
	}
	if !reflect.DeepEqual(gotTranscript, messages) {
		t.Fatalf("the opener was handed %v, want the transcript", contents(gotTranscript))
	}
}

func TestOpenStepGivesBorrowedMessagesTheirOwnMaps(t *testing.T) {
	tree := cannedTree("tree-a", "A")
	tree.Opening = func(d AnnotatedMessage, tr AnnotatedMessages) AnnotatedMessages {
		return append(TrailingUserMessages(tr), d)
	}
	messages := transcript("user", "first", "user", "second")
	messages[0].Annotations["pref"] = Annotation{Name: "pref", Data: "paperback"}
	messages[1].Annotations = nil

	step := openStep(tree, "the direction", messages)

	if got := contents(step.Messages); !reflect.DeepEqual(got, []string{"first", "second", "the direction"}) {
		t.Fatalf("the step opened with %v, want the user's words then the direction", got)
	}
	first := step.Messages[0]
	if _, ok := first.Annotations["raw_history"]; !ok {
		t.Fatal("raw_history is not on the step's first message")
	}
	if _, ok := first.Annotations["$context"]; !ok {
		t.Fatal("$context is not on the step's first message")
	}
	if first.Annotations["pref"].Data != "paperback" {
		t.Fatal("the transcript message's own annotation did not come along")
	}
	for i, m := range step.Messages[1:] {
		if _, ok := m.Annotations["raw_history"]; ok {
			t.Fatalf("raw_history is pinned on message %d too; it belongs on the first only", i+1)
		}
		if _, ok := m.Annotations["$context"]; ok {
			t.Fatalf("$context is pinned on message %d too; it belongs on the first only", i+1)
		}
	}

	// A state that pins an annotation on any of the step's messages writes
	// the step's own map; the transcript must not see it.
	for i := range step.Messages {
		step.Messages[i].Annotations["pinned"] = Annotation{Name: "pinned"}
	}
	if got := len(messages[0].Annotations); got != 1 {
		t.Fatalf("the transcript's first message now holds %d annotations, want only pref: the step shares its map", got)
	}
	if messages[1].Annotations != nil {
		t.Fatal("the transcript's second message gained a map: the step wrote through to the transcript")
	}
}

func TestOpenStepFallsBackToTheDirectionWhenTheOpeningIsEmpty(t *testing.T) {
	tree := cannedTree("tree-a", "A")
	tree.Opening = func(AnnotatedMessage, AnnotatedMessages) AnnotatedMessages { return nil }
	messages := transcript("user", "first", "user", "second")

	got := openStep(tree, "the direction", messages)
	want := openStep(cannedTree("tree-a", "A"), "the direction", messages)

	if !reflect.DeepEqual(got.Messages, want.Messages) {
		t.Fatalf("an empty opening produced %v, want the default's %v", contents(got.Messages), contents(want.Messages))
	}
	if n := len(got.Messages[0].Annotations); n != 2 {
		t.Fatalf("the fallback direction carries %d annotations, want the two pins only", n)
	}
}
