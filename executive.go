package arboreal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/zenful-ai/arboreal/llm"
	"github.com/zenful-ai/arboreal/util"
)

const (
	DefaultMaxPlanDepth = 3
)

type ExecGeneratedStep struct {
	Behavior        Step
	Messages        AnnotatedMessages
	ReplanTombstone bool
}

// copyStep copies a step's behavior. Copy returns a Behavior; a Step's copy
// is a Step, and anything else is a programming error.
func copyStep(s Step) Step {
	return s.Copy().(Step)
}

// openStep plans one step of b: a copy of the behavior, opened with the
// planner's direction as a user-role message whose annotations carry
// raw_history, the transcript at planning time, and $context.
func openStep(b Step, direction string, transcript AnnotatedMessages) *ExecGeneratedStep {
	annotations := make(map[string]Annotation)

	annotations["raw_history"] = Annotation{
		Data: transcript,
	}
	annotations["$context"] = Annotation{
		Data: transcript.GetAnnotation("$context"),
	}

	return &ExecGeneratedStep{
		Behavior: copyStep(b),
		Messages: AnnotatedMessages{
			{
				ChatCompletionMessage: llm.ChatCompletionMessage{
					Role:    llm.ChatMessageRoleUser,
					Content: direction,
				},
				Annotations: annotations,
			},
		},
	}
}

type PlanResult struct {
	Messages AnnotatedMessages  `json:"messages"`
	Step     *ExecGeneratedStep `json:"step"`
	Signal   Signal             `json:"signal"`
}

// ToolCallRecord is one executed tool call: the step that made it, the tool,
// the arguments the model gave, and the text the tool answered with (the
// Content of the function-role message the state appended, see state.go).
type ToolCallRecord struct {
	Step      string         `json:"step"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Result    string         `json:"result"`
}

// AnnotationToolCalls names the annotation the executive puts on the reply it
// appends: a []ToolCallRecord of this turn's executed tool calls, in plan
// order. Reserved, like raw_history and $context.
const AnnotationToolCalls = "tool_calls"

type TodoListExecutive struct {
	ExecName           string
	ExecDescription    string
	Preamble           string
	Behaviors          []Step
	OutOfBoundsHandler Behavior
	MaxPlanDepth       int
	History            AnnotatedMessages
	ClientID           string

	// PlannerModel is the model URI for the planning call only, e.g.
	// "anthropic:claude-sonnet-4-20250514". Empty falls back to the context
	// default set with WithDefaultModel. It does not apply to the executive's
	// other LLM calls: the turn summary in Execute has SummaryModel, and the
	// states inside its behaviors resolve their own Model, then the context
	// default.
	PlannerModel string

	// RepairModel is the model URI for repairing malformed planner JSON.
	// Empty inherits the resolved PlannerModel, so repair never runs on a
	// model the caller did not name.
	RepairModel string

	// SummaryModel is the model URI for the turn summary in Execute, the
	// call that writes the reply when more than one step ran, or a lone
	// step failed. Empty falls back to the context default set with
	// WithDefaultModel, like a behavior state's Model. It does not inherit
	// PlannerModel: the summary is prose for the user, not the routing
	// decision, so naming a planner leaves the reply's voice on the default.
	SummaryModel string

	// FIXME: This shouldn't work this way
	Output string

	// ToolCalls is this turn's executed tool calls, in plan order, set by
	// Execute alongside Output and attached by Call and RunLoop to the reply
	// they append. Empty when no tool ran. It shares Output's side-channel
	// shape, and its FIXME. The reply's annotation holds this same slice,
	// not a copy.
	ToolCalls []ToolCallRecord

	plan      []*ExecGeneratedStep
	planDepth int
	hash      string
}

func CreateTodoListExecutive(name, description string, behaviors ...Step) *TodoListExecutive {
	h, err := GenerateStringIdentifier("id-", 16)
	if err != nil {
		panic(err)
	}

	return &TodoListExecutive{
		ExecName:        name,
		ExecDescription: description,
		Behaviors:       behaviors,
		hash:            h,
		MaxPlanDepth:    DefaultMaxPlanDepth,
	}
}

func CreateTodoListExecutiveWithId(name, description, id string, behaviors ...Step) *TodoListExecutive {
	return &TodoListExecutive{
		ExecName:        name,
		ExecDescription: description,
		Behaviors:       behaviors,
		hash:            id,
		MaxPlanDepth:    DefaultMaxPlanDepth,
	}
}

// modelURIs resolves the planner and repair model URIs once, up front, so a
// single plan makes exactly one resolution decision. Repair inherits the
// resolved planner model rather than falling to the context default on its
// own; because that inherited value is never empty, repair can never trigger
// the fallback independently.
func (e *TodoListExecutive) modelURIs(ctx context.Context) (planner, repair string) {
	planner = modelURIFor(ctx, e.PlannerModel)
	repair = e.RepairModel
	if repair == "" {
		repair = planner
	}
	return planner, repair
}

func fixJSON(ctx context.Context, j, modelURI string) (string, error) {
	provider, err := llm.CreateModelProvider(modelURI, llm.ProviderOpenAI)
	if err != nil {
		return "", err
	}

	response, err := provider.CreateChatCompletion(ctx, &llm.ChatCompletionRequest{
		Model: modelURI,
		Messages: []llm.ChatCompletionMessage{
			{
				Role:    llm.ChatMessageRoleSystem,
				Content: "Respond only in valid JSON.",
			},
			{
				Role:    llm.ChatMessageRoleUser,
				Content: fmt.Sprintf("The following JSON is invalid. Please fix it, returning *only* valid JSON:\n\n%s", j),
			},
		},
	})
	if err != nil {
		return "", err
	}

	return response.Message.Content, nil
}

func (e *TodoListExecutive) interpolatedPreamble(messages AnnotatedMessages) string {
	var buf bytes.Buffer
	var tmpl AnnotationTemplate

	_, err := tmpl.Parse(e.Preamble)
	if err != nil {
		panic(err)
	}
	err = tmpl.Execute(&buf, messages)
	if err != nil {
		panic(err)
	}

	return buf.String()
}

var executivePlannerPrompt = `{{ .Preamble }}

Your job is to plan a series of steps to accomplish a goal given to you by a user. 
The steps available to you are the following:

Re-plan: If a plan requires further planning to be complete, end it with this step
{{ range .Behaviors }}{{ .BehaviorName }}: {{ .BehaviorDescription }}
{{ end }}

Return your response as a JSON array of one or more step names to execute in order to accomplish the user's goal. 
Each step should consist of the name of the step, as well as extra "direction" or context to accomplish the step accurately given the user's request.
A simple example response could be:

[
   { 
      "name": "{{ (index .Behaviors 0).BehaviorName }}",
      "direction": "{{ (index .Behaviors 0).Example }}"
   }
]
`

func (e *TodoListExecutive) Plan(ctx context.Context, messages AnnotatedMessages) {
	var data = struct {
		Preamble  string
		Behaviors []Step
	}{
		Behaviors: e.Behaviors,
		Preamble:  e.interpolatedPreamble(messages),
	}

	t := template.Must(template.New("planner").Parse(executivePlannerPrompt))

	var buf bytes.Buffer
	err := t.Execute(&buf, data)
	if err != nil {
		panic(err)
	}

	e.plan = []*ExecGeneratedStep{}

	history := AnnotatedMessages{
		{
			ChatCompletionMessage: messages.LastMessage().ChatCompletionMessage,
			Annotations:           make(map[string]Annotation),
		},
	}

	extraContext := "\n\nPrevious chat history:\n\n"
	for idx, message := range messages {
		if len(messages)-3 < 0 {
			break
		}

		if idx < len(messages)-1 && idx >= len(messages)-4 {
			extraContext += message.Content + "\n\n"
		}
	}

	plannerModel, repairModel := e.modelURIs(ctx)

	history, s := LLMCompletionState(LLMCompletionOptions{
		System:     buf.String() + extraContext,
		Annotation: "plan",
		Model:      plannerModel,
	}).Lambda(ctx, history)

	if e, ok := s.(*ErrorSignal); ok {
		panic(e)
	}

	result := history.GetAnnotation("plan")
	if result != nil {
		planData, ok := result.Data.(string)
		if ok {
			var steps []struct {
				Name      string `json:"name"`
				Direction string `json:"direction"`
			}

			err = json.Unmarshal([]byte(planData), &steps)
			if err != nil {
				retryErr := util.RetryWithBackoff(func() error {
					planData, err = fixJSON(ctx, planData, repairModel)
					if err != nil {
						return fmt.Errorf("repair call: %w", err)
					}

					err = json.Unmarshal([]byte(planData), &steps)
					if err != nil {
						return fmt.Errorf("repair output still invalid: %w", err)
					}

					return nil
				}, 3)
				if retryErr != nil {
					// Mitigation only: Plan has no return value and its only
					// error convention is panic, so the executive proceeds with
					// whatever steps parsed and callers cannot detect it.
					log.Printf("arboreal: planner JSON repair failed after retries; the plan may be empty or incomplete: %v", retryErr)
				}
			}

			behaviorLookup := make(map[string]Step)
			for _, b := range e.Behaviors {
				behaviorLookup[b.Name()] = b
			}

			for _, step := range steps {
				if b, ok := behaviorLookup[step.Name]; ok {
					e.plan = append(e.plan, openStep(b, step.Direction, messages))
				} else {
					if strings.ToLower(step.Name) == "re-plan" {
						e.plan = append(e.plan, &ExecGeneratedStep{
							Behavior:        nil,
							ReplanTombstone: true,
							Messages: AnnotatedMessages{
								{
									ChatCompletionMessage: llm.ChatCompletionMessage{
										Role:    llm.ChatMessageRoleUser,
										Content: step.Direction,
									},
									Annotations: make(map[string]Annotation),
								},
							},
						})
					} else {
						panic(fmt.Sprintf("No plan named %s found!", step.Name))
					}
				}
			}
		} else {
			// FIXME
			panic("could not put a plan together!")
		}
	} else {
		// FIXME
		panic("no valid plan annotation!")
	}

	// A re-plan with no other steps is not allowed
	if len(e.plan) == 1 && e.plan[0].ReplanTombstone {
		e.plan = []*ExecGeneratedStep{}
	}
}

// recordToolCalls reads the executed tool calls in part, in order: every
// function-role message paired with the assistant message before it. That
// pair is exactly what LLMCompletionState's loop appends; the shape is the
// contract, so a pair a lambda state appended by hand is recorded too. A
// reply with several ToolCalls yields one record, for the first entry, since
// only it was executed and only it has a result.
func recordToolCalls(step string, part AnnotatedMessages) []ToolCallRecord {
	var out []ToolCallRecord
	for i := 1; i < len(part); i++ {
		result := part[i]
		if result.Role != llm.ChatMessageRoleFunction {
			continue
		}
		call := part[i-1]
		if call.Role != llm.ChatMessageRoleAssistant || len(call.ToolCalls) == 0 || call.ToolCalls[0].Name != result.Name {
			continue
		}
		out = append(out, ToolCallRecord{
			Step:      step,
			Tool:      result.Name,
			Arguments: maps.Clone(call.ToolCalls[0].Arguments),
			Result:    result.Content,
		})
	}
	return out
}

// addedAfter is what a behavior appended to a list that held before
// messages when it was called: this turn's part of a step, or the handler's
// list past the one message it was given. A behavior that returned fewer
// messages than it was given added nothing.
func addedAfter(messages AnnotatedMessages, before int) AnnotatedMessages {
	if len(messages) < before {
		return nil
	}
	return messages[before:]
}

func (e *TodoListExecutive) executePlan(ctx context.Context, plan []*ExecGeneratedStep) []PlanResult {
	var wg sync.WaitGroup

	var results = make(chan *PlanResult, len(plan)+1)

	// before[i] is how long step i's list was when its goroutine started.
	// Everything the behavior appends is at that index and beyond, so a
	// resumed step's earlier turns are never re-read.
	before := make([]int, len(plan))

	for i, step := range plan {
		if step.ReplanTombstone {
			continue
		}

		before[i] = len(step.Messages)
		wg.Add(1)

		go func() {
			defer wg.Done()

			var signal Signal
			step.Messages, signal = step.Behavior.Call(ctx, step.Messages)

			results <- &PlanResult{
				Messages: step.Messages,
				Step:     step,
				Signal:   signal,
			}
		}()
	}

	wg.Wait()

	// Every goroutine has returned, so each step's list is final. The
	// records are built in plan order, not in the completion order the
	// results channel holds, and whatever signal the step ended with.
	for i, step := range plan {
		if step.ReplanTombstone {
			continue
		}
		e.ToolCalls = append(e.ToolCalls, recordToolCalls(step.Behavior.Name(), addedAfter(step.Messages, before[i]))...)
	}

	results <- nil

	var result []PlanResult
	for {
		select {
		case r := <-results:
			if r == nil {
				goto done
			}

			result = append(result, *r)
		}
	}

done:
	return result
}

// turnOutcome is one turn's results sorted: the steps kept for the next
// turn, what the summarizer would merge, and whether one step's last message
// can stand as the reply on its own.
type turnOutcome struct {
	// Kept is the paused steps, in result order.
	Kept []*ExecGeneratedStep
	// Summaries is the last message of every finished, failed or paused
	// step, finished steps first in completion order and then the paused
	// ones; a failed step contributes "Error occurred: " and its error.
	Summaries []string
	// Verbatim is the reply when exactly one step ran and it ended with nil
	// or a pause; HasReply says whether it is set.
	Verbatim string
	HasReply bool
}

// triage sorts the results of the steps that ran this turn. A nil signal is
// a finished step and an *ErrorSignal a failed one: both contribute a
// summary and neither is kept. A *CollectUserInputSignal is a paused step:
// kept, and its last message is a summary too. Any other signal is a leaked
// skip and contributes nothing. The reply is decided by what ran, not by
// how many steps are kept: when exactly one step ran and it finished or
// paused, its last message is the reply as written.
func triage(results []PlanResult) turnOutcome {
	var out turnOutcome
	var paused []PlanResult
	for _, result := range results {
		switch t := result.Signal.(type) {
		case *ErrorSignal:
			out.Summaries = append(out.Summaries, "Error occurred: "+t.Error())
		case *CollectUserInputSignal:
			paused = append(paused, result)
		case nil:
			out.Summaries = append(out.Summaries, result.Messages.LastMessage().Content)
		}
	}
	for _, result := range paused {
		out.Kept = append(out.Kept, result.Step)
		out.Summaries = append(out.Summaries, result.Messages.LastMessage().Content)
	}
	if len(results) == 1 {
		switch results[0].Signal.(type) {
		case nil, *CollectUserInputSignal:
			out.Verbatim = results[0].Messages.LastMessage().Content
			out.HasReply = true
		}
	}
	return out
}

var executiveSummarizerPrompt = `{{ .Preamble }}

Given the following chat transcript:

{{ .Transcript }}

And the following generated information:

{{ range $index, $element := .Summaries }}{{ $element }}

{{ end }}

Craft a response to the user message below using available information. No need to add filler pleasantries.
`

var workInProcessSummarizerPrompt = `{{ .Preamble }}

Rephrase the following statements or questions as a single question or statement:

{{ range $index, $element := .Summaries }}{{ $element }}

{{ end }}
`

// Execute runs the plan and decides the turn's reply. It writes e.Output,
// e.ToolCalls and e.plan (and, on a re-plan, rewrites the last message of
// messages with the direction and the summaries) and returns a Signal: nil
// when Output holds the reply, the *ErrorSignal of the out-of-bounds handler
// or of the summarizer when the call that was to write the reply failed. On
// failure Output is empty and e.plan holds the kept steps as a successful
// turn would have stored them; ToolCalls holds whatever ran, on a failed
// turn as on a successful one.
func (e *TodoListExecutive) Execute(ctx context.Context, messages AnnotatedMessages) Signal {
	// One turn, one record: cleared here and nowhere else, so a re-plan's
	// first round keeps its calls.
	e.ToolCalls = nil
	return e.execute(ctx, messages)
}

// execute is Execute's body. The re-plan recursion calls it, not Execute, so
// the first round's tool-call records survive into the second round's.
func (e *TodoListExecutive) execute(ctx context.Context, messages AnnotatedMessages) Signal {
	if len(e.plan) == 0 {
		// A turn that ends here ends like any other: the re-plan depth
		// starts fresh next turn.
		e.planDepth = 0

		if e.OutOfBoundsHandler == nil {
			e.Output = "Please set an out-of-bounds handler, this request was unable to be planned."
			return nil
		}

		m, sig := e.OutOfBoundsHandler.Call(ctx, AnnotatedMessages{
			{
				ChatCompletionMessage: messages.LastMessage().ChatCompletionMessage,
				Annotations:           make(map[string]Annotation),
			},
		})
		// The handler was given one message; whatever it added past it is
		// read for tool calls like a step's part, whatever its signal.
		e.ToolCalls = append(e.ToolCalls, recordToolCalls(e.OutOfBoundsHandler.Name(), addedAfter(m, 1))...)
		if err, ok := sig.(*ErrorSignal); ok {
			e.Output = ""
			return err
		}
		e.Output = m.LastMessage().Content
		return nil
	}

	var info = struct {
		Transcript string
		Summaries  []string
		Preamble   string
	}{
		Preamble: e.interpolatedPreamble(messages),
	}

	for _, m := range messages {
		info.Transcript += m.Role + ": " + m.Content + "\n"
	}

	outcome := triage(e.executePlan(ctx, e.plan))
	info.Summaries = outcome.Summaries
	plan := outcome.Kept

	if e.plan[len(e.plan)-1].ReplanTombstone {
		if len(plan) > 0 {
			plan = append(plan, e.plan[len(e.plan)-1])
		} else {
			direction := e.plan[len(e.plan)-1].Messages.LastMessage().Content

			if direction == "" {
				direction = messages.LastMessage().Content
			}

			messages[len(messages)-1].Content = fmt.Sprintf("%s\n\n%s", direction, strings.Join(info.Summaries, "\n\n"))
			e.plan = []*ExecGeneratedStep{}

			if e.planDepth >= e.MaxPlanDepth {
				goto final
			}

			e.planDepth += 1

			e.Plan(ctx, messages)
			return e.execute(ctx, messages)
		}
	}

final:

	e.plan = plan

	// One step ran and it finished or paused: its last message is the reply
	// as written, and no model is called. A carried Re-plan tombstone is not
	// a step that ran. Otherwise the summarizer merges the summaries.
	if outcome.HasReply {
		e.Output = outcome.Verbatim
		e.planDepth = 0
		return nil
	}

	prompt := executiveSummarizerPrompt
	if len(e.plan) > 0 {
		prompt = workInProcessSummarizerPrompt
	}

	var buf bytes.Buffer
	t := template.Must(template.New("summarizer").Parse(prompt))
	t.Execute(&buf, info)

	// The summarizer is the executive's own state, called through the
	// traced wrapper so its begin and end ride the trace like any state's.
	// Its model is resolved here into a local, the way Plan resolves the
	// planner's: SummaryModel if set, else the context default, else the
	// fallback. It does not inherit PlannerModel.
	summaryModel := modelURIFor(ctx, e.SummaryModel)
	summarize := LLMCompletionState(LLMCompletionOptions{
		Id:          e.Hash() + "-summarize",
		Name:        "summarize",
		Description: "Writes the turn's reply from the steps' results",
		ClientID:    e.ClientID,
		System:      buf.String(),
		Model:       summaryModel,
	})
	m, sig := summarize.Call(ctx, AnnotatedMessages{
		*messages.LastMessage(),
	})
	if err, ok := sig.(*ErrorSignal); ok {
		e.Output = ""
		e.planDepth = 0
		return err
	}

	last := m.LastMessage()
	if last == nil {
		panic("empty last message")
	}

	e.Output = last.Content
	e.planDepth = 0
	return nil
}

func (e *TodoListExecutive) Hash() string {
	return e.hash
}

func (e *TodoListExecutive) Name() string {
	return e.ExecName
}

func (e *TodoListExecutive) Description() string {
	return e.ExecDescription
}

func (e *TodoListExecutive) Copy() Behavior {
	var t TodoListExecutive

	t.ExecName = e.ExecName
	t.ExecDescription = e.ExecDescription
	t.Preamble = e.Preamble
	t.MaxPlanDepth = e.MaxPlanDepth
	t.hash = e.hash
	t.ClientID = e.ClientID
	t.PlannerModel = e.PlannerModel
	t.RepairModel = e.RepairModel
	t.SummaryModel = e.SummaryModel

	t.planDepth = e.planDepth
	t.OutOfBoundsHandler = e.OutOfBoundsHandler.Copy()

	for _, b := range e.Behaviors {
		t.Behaviors = append(t.Behaviors, copyStep(b))
	}

	return &t
}

// annotateReply pins this turn's tool-call record onto the reply the caller
// just appended, when any tool ran. The reply's map is fresh from
// AppendToMessages, so there is nothing to collide with and no nil to guard;
// on a turn with no calls the annotation is absent, not empty.
func (e *TodoListExecutive) annotateReply(reply *AnnotatedMessage) {
	if len(e.ToolCalls) == 0 {
		return
	}
	reply.Annotations[AnnotationToolCalls] = Annotation{
		Name:        AnnotationToolCalls,
		Data:        e.ToolCalls,
		Explanation: "the tool calls this turn's steps made, in plan order",
	}
}

// resume hands every pending step the part of the transcript its behavior
// selects, appended to the step's own messages. A Re-plan tombstone has no
// behavior and takes nothing.
func (e *TodoListExecutive) resume(transcript AnnotatedMessages) {
	for _, step := range e.plan {
		if step.ReplanTombstone {
			continue
		}
		step.Messages = append(step.Messages, step.Behavior.Select(transcript)...)
	}
}

func (e *TodoListExecutive) Call(ctx context.Context, messages AnnotatedMessages) (AnnotatedMessages, Signal) {
	var trace Trace

	// Check for a trace channel in the context
	if ctx != nil && ctx.Value("arboreal_trace") != nil {
		trace, _ = ctx.Value("arboreal_trace").(Trace)
	}

	telem := TraceTelemetry{
		Start: time.Now(),
	}

	trace.Send(&TraceMessage{
		Type:     TraceMessageTypeCallBegin,
		ID:       e.Hash(),
		ClientID: e.ClientID,
		Name:     e.Name(),
		Message:  "entering planner state",
		Telemetry: &TraceTelemetry{
			Start: telem.Start,
		},
	})

	if len(e.plan) <= 0 {
		e.Plan(ctx, messages)
	} else {
		e.resume(messages)
	}

	sig := e.Execute(ctx, messages)

	// A turn that produced a reply appends it, with the turn's tool-call
	// record pinned on when any tool ran; a turn that did not returns the
	// list it was given, unchanged, and the failure, and the record of what
	// ran before the failure is on e.ToolCalls only. A pause inside is still
	// not reported: the snapshot is how a caller finds out.
	var err error
	if es, ok := sig.(*ErrorSignal); ok {
		err = es
	} else {
		messages = AppendToMessages(messages, llm.ChatCompletionMessage{
			Role:    llm.ChatMessageRoleAssistant,
			Content: e.Output,
		})
		e.annotateReply(messages.LastMessage())
	}

	now := time.Now()
	telem.End = &now
	trace.Send(&TraceMessage{
		Type:      TraceMessageTypeCallEnd,
		ID:        e.Hash(),
		ClientID:  e.ClientID,
		Name:      e.Name(),
		Error:     err,
		Message:   "leaving planner state",
		Telemetry: &telem,
		Signal:    TraceForSignal(sig),
	})

	return messages, sig
}

func (e *TodoListExecutive) RunLoop(ctx context.Context, c Channel) error {
	for {
		cm, err := c.Receive()
		if err != nil {
			return err
		}

		e.History = AppendToMessages(e.History, llm.ChatCompletionMessage{
			Role:    llm.ChatMessageRoleUser,
			Content: cm.Content,
		})

		if len(e.plan) == 0 {
			e.Plan(ctx, e.History)
		} else {
			e.resume(e.History)
		}

		if sig, ok := e.Execute(ctx, e.History).(*ErrorSignal); ok {
			return fmt.Errorf("arboreal: the turn produced no reply: %w", sig)
		}

		e.History = AppendToMessages(e.History, llm.ChatCompletionMessage{
			Role:    llm.ChatMessageRoleAssistant,
			Content: e.Output,
		})
		e.annotateReply(e.History.LastMessage())

		err = c.Send(&ChannelMessage{
			Id:      cm.Id,
			Content: e.Output,
		})
		if err != nil {
			return err
		}
	}
}
