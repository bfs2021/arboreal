package arboreal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	Behavior        Behavior
	Messages        AnnotatedMessages
	ReplanTombstone bool
}

type PlanResult struct {
	Messages AnnotatedMessages  `json:"messages"`
	Step     *ExecGeneratedStep `json:"step"`
	Signal   Signal             `json:"signal"`
}

type TodoListExecutive struct {
	ExecName           string
	ExecDescription    string
	Preamble           string
	Behaviors          []Behavior
	OutOfBoundsHandler Behavior
	MaxPlanDepth       int
	History            AnnotatedMessages
	ClientID           string

	// PlannerModel is the model URI for the planning call only, e.g.
	// "anthropic:claude-sonnet-4-20250514". Empty falls back to the context
	// default set with WithDefaultModel. It does not apply to the executive's
	// other LLM calls: the turn summary in Execute and the states inside its
	// behaviors resolve their own Model, then the context default.
	PlannerModel string

	// RepairModel is the model URI for repairing malformed planner JSON.
	// Empty inherits the resolved PlannerModel, so repair never runs on a
	// model the caller did not name.
	RepairModel string

	// FIXME: This shouldn't work this way
	Output string

	plan      []*ExecGeneratedStep
	planDepth int
	hash      string
}

func CreateTodoListExecutive(name, description string, behaviors ...Behavior) *TodoListExecutive {
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

func CreateTodoListExecutiveWithId(name, description, id string, behaviors ...Behavior) *TodoListExecutive {
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
		Behaviors []Behavior
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

			behaviorLookup := make(map[string]Behavior)
			for _, b := range e.Behaviors {
				behaviorLookup[b.Name()] = b
			}

			for _, step := range steps {
				if b, ok := behaviorLookup[step.Name]; ok {
					annotations := make(map[string]Annotation)

					annotations["raw_history"] = Annotation{
						Data: messages,
					}
					annotations["$context"] = Annotation{
						Data: messages.GetAnnotation("$context"),
					}

					e.plan = append(e.plan, &ExecGeneratedStep{
						Behavior: b.Copy(),
						Messages: AnnotatedMessages{
							{
								ChatCompletionMessage: llm.ChatCompletionMessage{
									Role:    llm.ChatMessageRoleUser,
									Content: step.Direction,
								},
								Annotations: annotations,
							},
						},
					})
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

func (e *TodoListExecutive) executePlan(ctx context.Context, plan []*ExecGeneratedStep) []PlanResult {
	var wg sync.WaitGroup

	var results = make(chan *PlanResult, len(plan)+1)

	for _, step := range plan {
		if step.ReplanTombstone {
			continue
		}

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
	// Summaries is every step's last message, finished steps first in
	// completion order and then the paused ones; a failed step contributes
	// "Error occurred: " and its error.
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

// Execute runs the plan and decides the turn's reply. It writes e.Output
// and e.plan and returns a Signal: nil when Output holds the reply, the
// *ErrorSignal of the out-of-bounds handler or of the summarizer when the
// call that was to write the reply failed. On failure Output is empty and
// e.plan holds the kept steps as a successful turn would have stored them.
func (e *TodoListExecutive) Execute(ctx context.Context, messages AnnotatedMessages) Signal {
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

	var plan []*ExecGeneratedStep

	var behaviorsToRetry []*ExecGeneratedStep
	var collectResults []PlanResult

	results := e.executePlan(ctx, e.plan)
	for _, result := range results {
		switch t := result.Signal.(type) {
		case *ErrorSignal:
			info.Summaries = append(info.Summaries, "Error occurred: "+t.Error())
		case *CollectUserInputSignal:
			collectResults = append(collectResults, result)
		case nil:
			info.Summaries = append(info.Summaries, result.Messages.LastMessage().Content)
		}
	}

	if len(behaviorsToRetry) > 0 {
		// TODO: Retry!
		fmt.Println("would retry something here!")
	}

	if len(collectResults) > 0 {
		for _, result := range collectResults {
			plan = append(plan, result.Step)

			info.Summaries = append(info.Summaries, result.Messages.LastMessage().Content)
		}
	}

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
			return e.Execute(ctx, messages)
		}
	}

final:

	e.plan = plan

	var prompt = executiveSummarizerPrompt
	if len(e.plan) > 0 {
		prompt = workInProcessSummarizerPrompt

		if len(e.plan) == 1 {
			e.Output = e.plan[0].Messages.LastMessage().Content
			e.planDepth = 0
			return nil
		}
	}

	var buf bytes.Buffer
	t := template.Must(template.New("summarizer").Parse(prompt))
	t.Execute(&buf, info)

	m, _ := LLMCompletionState(LLMCompletionOptions{
		System: buf.String(),
	}).Lambda(ctx, AnnotatedMessages{
		*messages.LastMessage(),
	})

	last := m.LastMessage()
	if last == nil {
		panic("empty last message")
	}

	e.Output = m.LastMessage().Content
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

	t.planDepth = e.planDepth
	t.OutOfBoundsHandler = e.OutOfBoundsHandler.Copy()

	for _, b := range e.Behaviors {
		t.Behaviors = append(t.Behaviors, b.Copy())
	}

	return &t
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
		// FIXME: This may need to be tailored to each behavior
		for _, p := range e.plan {
			p.Messages = append(p.Messages, *messages.LastMessage())
		}
	}

	e.Execute(ctx, messages)

	messages = AppendToMessages(messages, llm.ChatCompletionMessage{
		Role:    llm.ChatMessageRoleAssistant,
		Content: e.Output,
	})

	now := time.Now()
	telem.End = &now
	trace.Send(&TraceMessage{
		Type:      TraceMessageTypeCallEnd,
		ID:        e.Hash(),
		ClientID:  e.ClientID,
		Name:      e.Name(),
		Message:   "leaving planner state",
		Telemetry: &telem,
	})

	return messages, nil
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
			for _, step := range e.plan {
				step.Messages = append(step.Messages, *e.History.LastMessage())
			}
		}

		e.Execute(ctx, e.History)

		e.History = AppendToMessages(e.History, llm.ChatCompletionMessage{
			Role:    llm.ChatMessageRoleAssistant,
			Content: e.Output,
		})

		err = c.Send(&ChannelMessage{
			Id:      cm.Id,
			Content: e.Output,
		})
		if err != nil {
			return err
		}
	}
}
