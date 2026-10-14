package arboreal

import (
	"context"
	"time"
)

type Behavior interface {
	Hashable
	Name() string
	Description() string
	Call(ctx context.Context, messages AnnotatedMessages) (AnnotatedMessages, Signal)
	Copy() Behavior
}

// A TranscriptSelector has a say over what it takes from the transcript at
// the start of a turn. The executive calls Select on every pending step
// when it resumes, and appends the result to the step's own messages.
type TranscriptSelector interface {
	Select(transcript AnnotatedMessages) AnnotatedMessages
}

// Step is what the executive plans over and resumes: a behavior that also
// selects its input. In the framework only *BehaviorTree satisfies it,
// which is also what the planner prompt requires. It is the type of a
// planned step's behavior, not the planned step itself; that is
// ExecGeneratedStep.
type Step interface {
	Behavior
	TranscriptSelector
}

type BehaviorTree struct {
	BehaviorName        string          `json:"name"`
	BehaviorDescription string          `json:"description"`
	Example             string          `json:"example"`
	Graph               Graph[Behavior] `json:"graph"`
	State               Stack[Behavior] `json:"state"`
	Traversed           map[string]bool `json:"traversed"`
	ClientID            string          `json:"client_id"`

	// Intake selects, from the transcript, the messages appended to this
	// tree's step when the executive resumes it. Nil selects the last user
	// message since the agent last spoke, or nothing (see LastUserMessage);
	// TrailingUserMessages selects every user message since then. It is
	// code, not state: snapshots do not carry it, and Copy hands the same
	// func to the copy, so it must not capture the tree it is set on.
	Intake func(transcript AnnotatedMessages) AnnotatedMessages `json:"-"`

	stateLookupMap map[any]int
	hash           string
}

func CreateBehaviorTree(name, description, example string) BehaviorTree {
	h, _ := GenerateStringIdentifier("id-", 16)
	return BehaviorTree{
		BehaviorName:        name,
		BehaviorDescription: description,
		Example:             example,
		stateLookupMap:      make(map[any]int),
		hash:                h,
	}
}

func CreateBehaviorTreeWithId(name, description, example, id string) BehaviorTree {
	return BehaviorTree{
		BehaviorName:        name,
		BehaviorDescription: description,
		Example:             example,
		hash:                id,
	}
}

func (b *BehaviorTree) Hash() string {
	return b.hash
}

func (b *BehaviorTree) Name() string {
	return b.BehaviorName
}

func (b *BehaviorTree) Description() string {
	return b.BehaviorDescription
}

// Select is the tree's Intake, or the last user message when none is set.
func (b *BehaviorTree) Select(transcript AnnotatedMessages) AnnotatedMessages {
	if b.Intake == nil {
		return LastUserMessage(transcript)
	}
	return b.Intake(transcript)
}

func (b *BehaviorTree) Copy() Behavior {
	var t BehaviorTree

	t.BehaviorName = b.BehaviorName
	t.BehaviorDescription = b.BehaviorDescription
	t.ClientID = b.ClientID
	t.Intake = b.Intake
	t.Example = b.Example
	t.Graph = b.Graph
	t.State = Stack[Behavior]{}
	t.Traversed = make(map[string]bool)

	t.hash = b.hash

	return &t
}

func (b *BehaviorTree) AddState(s Behavior) {
	b.Graph.AddNode(s)
}

func (b *BehaviorTree) AddTransition(from, to Behavior) {
	b.Graph.AddTransition(from, to)
}

func (b *BehaviorTree) Call(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal) {
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
		ID:       b.Hash(),
		ClientID: b.ClientID,
		Name:     b.Name(),
		Message:  "entering behavior tree",
		Telemetry: &TraceTelemetry{
			Start: telem.Start,
		},
	})

	if b.State.IsEmpty() {
		b.State.Push(b.Graph.Initial())
		b.Traversed = make(map[string]bool)
	}

	var sig Signal

	for !b.State.IsEmpty() {
		state := b.State.Pop()

		if !b.Traversed[state.Hash()] {
			b.Traversed[state.Hash()] = true

			history, sig = state.Call(ctx, history)
			if e, ok := sig.(*ErrorSignal); ok {
				b.State.Items = []Behavior{}
				now := time.Now()
				telem.End = &now
				trace.Send(&TraceMessage{
					Type:      TraceMessageTypeCallEnd,
					ID:        b.Hash(),
					ClientID:  b.ClientID,
					Name:      b.Name(),
					Message:   "leaving behavior tree",
					Error:     e,
					Telemetry: &telem,
					Signal:    TraceForSignal(sig),
				})
				return history, e
			}

			children := b.Graph.Children(state)

			switch sig.(type) {
			case *TerminalSignal:
				b.State.Items = []Behavior{}
				b.Traversed = nil
				sig = nil
				goto done
			case *SkipSignal:
				continue
			case *CollectUserInputSignal:
				for _, child := range children {
					if !b.Traversed[child.Hash()] {
						b.State.Push(child)
					}
				}
				goto done
			}

			for i := len(children) - 1; i >= 0; i-- {
				child := children[i]
				if !b.Traversed[child.Hash()] {
					b.State.Push(child)
				}
			}
		}
	}

	b.Traversed = nil

done:

	now := time.Now()
	telem.End = &now
	trace.Send(&TraceMessage{
		Type:      TraceMessageTypeCallEnd,
		ID:        b.Hash(),
		ClientID:  b.ClientID,
		Name:      b.Name(),
		Message:   "leaving behavior tree",
		Telemetry: &telem,
		Signal:    TraceForSignal(sig),
	})

	return history, sig
}
