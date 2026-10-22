# The executive

## Why this exists

One tree handles one kind of request: Chapter 7's trees greet and answer, or extract and look up. An assistant has to handle many kinds, and something has to decide, message by message, which of them applies. The `TodoListExecutive` in `executive.go` is that something. It holds a flat list of behaviors, asks a model which of them to run for the current message and with what instruction, runs the chosen ones concurrently on copies, and turns their results into one reply. It is also the object that owns the conversation across turns: `History` lives on it, and so does the list of steps still waiting on the user, which is why a later message can go to a paused tree instead of back to the planner. And it is itself a `Behavior`, so an executive can sit inside a tree, or, wrapped in a one-state tree, inside another executive's plan, with the limits Chapter 7 gave for nesting and a few of its own listed below.

## The struct

```go
type TodoListExecutive struct {
    ExecName, ExecDescription string
    Preamble           string             // template, prepended to the planner and summarizer prompts
    Behaviors          []Step             // what the planner may choose from: Behavior + TranscriptSelector, which only *BehaviorTree satisfies
    OutOfBoundsHandler Behavior           // runs when the plan is empty
    MaxPlanDepth       int                // re-plan recursion cap, default 3
    History            AnnotatedMessages  // the transcript
    ClientID           string
    Output             string             // this turn's reply (set by Execute)
    ToolCalls          []ToolCallRecord   // this turn's executed tool calls (set by Execute)
    Crossed            []Annotation       // this turn's crossed annotations (set by Execute)
    // unexported: plan []*ExecGeneratedStep, planDepth int, hash string
}
```

`CreateTodoListExecutive(name, description, behaviors...)` fills `ExecName`, `ExecDescription` and `Behaviors`, sets `MaxPlanDepth` to `DefaultMaxPlanDepth`, which is 3, and draws a random hash with `GenerateStringIdentifier`, as the state and tree factories do. `CreateTodoListExecutiveWithId` takes the hash as a third argument, before the behaviors, and pins it for snapshots (Part III). Everything else is set afterwards by assigning to the field, as `examples/poetry` does with `Preamble` and `OutOfBoundsHandler`. `ExecName` and `ExecDescription` back `Name()` and `Description()` and appear on trace messages; no prompt reads them (Chapter 5).

A plan step is an `ExecGeneratedStep{Behavior, Messages, ReplanTombstone}`: the `Copy()` of the chosen behavior, typed `Step`, the message list that copy is called with, and a flag marking the reserved `Re-plan` step, which has no behavior; an unexported `turnStart` beside them is the index where this turn's messages begin, stamped on resume, zero for a step planned this turn, and read when the turn's crossed annotations are collected. The unexported fields are what the struct carries between turns: `plan`, the steps still pending; `planDepth`, how many re-plans deep the current turn is; and `hash`.

## Run it

### One shot

`examples/oneshot` is the executive with no loop at all: one behavior, one `Call`, one printed line. `Call` plans, executes, and returns the history it was given with the reply appended as an assistant message.

```go
{{#include ../../../examples/oneshot/main.go}}
```

Run it with `go run ./examples/oneshot`; it needs `OPENAI_TOKEN` and exits on its own. One run printed:

```text
The capital of France is Paris.
```

Two model calls: the planner and the tree's one state. One step ran and finished, so its reply comes back as written; the summarizer runs only when there is something to merge: two or more steps, or a lone step that failed. Note what the example does not set: no `Preamble` and no `OutOfBoundsHandler`. Both omissions are sharp edges below.

### Choosing between behaviors

`examples/poetry` is the executive doing what it exists for: two trees, each a single `LLMCompletionState` with a system prompt, each with a name, a description and an example written for the planner rather than for you.

```go
{{#include ../../../examples/poetry/main.go:behaviors}}
```

The executive gets both trees, a `Preamble` and an `OutOfBoundsHandler`.

```go
{{#include ../../../examples/poetry/main.go:executive}}
```

The rest of `main` is one `RunLoop` call on a `TerminalChannel`, as in the quickstart.

```go
{{#include ../../../examples/poetry/main.go:run}}
```

Run it with `go run ./examples/poetry` and try the three prompts from the file's header comment. This is one session; the poems, and the planner's wording, will differ from run to run.

```text
[User Message]

A haiku about autumn rain
$

[Assistant Response]

Whispers of the chill,
Crimson leaves dance in the breeze,
Autumn's tears descend.

[User Message]

A haiku and a sonnet about the sea
$

[Assistant Response]

Waves kiss the soft shore,
Endless whispers of the deep,
Ocean's lullaby.

O mighty sea, thou vast and endless blue,
With waves that dance like spirits in the night,
Thy whispered secrets in the breeze anew,
Do charm the weary soul with pure delight.

Upon thy shores, the sands of time do drift,
Each grain a story of the tides' embrace,
In tempest's roar or gentle wave’s soft lift,
Thou art a canvas, nature's artful grace.

When storms arise, thy fury doth enthrall,
Yet in thy calm, a peace beyond compare,
Thy depths hold mysteries that beckon all,
A siren’s call, a lover’s breath of air.

So let me wander by thy rolling tide,
For in thy presence, I shall bide my pride.

[User Message]

What's the weather like?
$

[Assistant Response]

Sorry, I only write haikus and sonnets.

[User Message]
```

Each prompt took a different path through `Execute`. The first was a one-step plan, `write_haiku` with a direction restating the topic; the step finished with `nil`, so nothing was kept for the next turn, and because it was the only step that ran, the haiku you saw is the state's reply as written. The second became a two-step plan, `write_haiku` and `write_sonnet`, which ran at the same time in two goroutines on two tree copies; two steps ran, so the summarizer merged the two poems into one reply. The weather question produced an *empty plan*: the `Preamble` told the planner to answer `[]` for anything that is not a poetry request, `Plan` parsed that into no steps, and `Execute` handed the message to `OutOfBoundsHandler`, whose canned text is the reply. No tree ran for that turn, and no summarizer either. This is Chapter 7's out-of-tree selector: one tree per alternative, and `examples/tree-loop`'s hand loop replaced by `RunLoop` plus a planner that picks the tree.

## How it works

### `Plan`

`Plan(ctx, messages)` begins by rendering `executivePlannerPrompt`, a Go `text/template`, with two values: the interpolated `Preamble` and `Behaviors`. For `examples/poetry` the result, with the `Preamble` line left as its placeholder, is this:

```
{{ .Preamble }}

Your job is to plan a series of steps to accomplish a goal given to you by a user.
The steps available to you are the following:

Re-plan: If a plan requires further planning to be complete, end it with this step
write_haiku: Write a haiku (three lines, 5-7-5 syllables) on the topic the user asked for
write_sonnet: Write a Shakespearean sonnet (fourteen rhymed lines) on the topic the user asked for

Return your response as a JSON array of one or more step names to execute in order to accomplish the user's goal.
Each step should consist of the name of the step, as well as extra "direction" or context to accomplish the step accurately given the user's request.
A simple example response could be:

[
   {
      "name": "write_haiku",
      "direction": "Write a haiku about autumn rain"
   }
]

Previous chat history:

...
```

Read the middle. The list of steps is the reserved `Re-plan` followed by one `BehaviorName: BehaviorDescription` line per behavior, and the sample response is the first behavior's name and `Example`. So `BehaviorName` and `BehaviorDescription` are the whole interface between your trees and the planner, which never sees a tree's states; write them like tool descriptions. Only the **first** behavior's `Example` appears, once, as the sample direction; `write_sonnet`'s example, like that of every behavior after the first, is never shown to the planner. And because the template reads struct fields rather than calling `Name()`, `Behaviors` must hold `*BehaviorTree`s (see the sharp edges).

`Preamble` is rendered first, by `interpolatedPreamble`, as an `AnnotationTemplate` (Chapter 4) against the history `Plan` was given, so it may use `{{ $last_message }}` and any annotation on the transcript. A `Preamble` that fails to parse or render panics; there is no error path.

The prompt is then sent through an `LLMCompletionState` in annotation mode, `LLMCompletionOptions{System: prompt, Annotation: "plan"}`, whose `.Lambda` `Plan` invokes directly, bypassing `Call` and its trace envelope (Chapter 5). The history it is handed holds one message, a copy of the last message's `ChatCompletionMessage` with a fresh annotation map. The rest of the transcript reaches the planner only inside the system prompt: the heading `Previous chat history:` is always appended, and once `len(messages) >= 3` the `Content` of up to three messages before the last is listed under it, roles stripped. An `*ErrorSignal` from the call is a `panic(e)`.

`GetAnnotation("plan")` reads the answer back. Chapter 4 explained why it arrives as a raw string: an array does not unmarshal into an `Annotation`, so annotation mode takes its fallback path. `Plan` asserts `Data.(string)` and `json.Unmarshal`s the text into a `[]struct{Name, Direction}`. If that fails, the text goes to `fixJSON`, which asks `gpt-4o-mini` to return a corrected version, inside `util.RetryWithBackoff(…, 3)`: one attempt plus up to three retries with a jittered backoff. The retry's return value is discarded, so if every attempt fails `steps` stays empty, the plan is empty, and `Execute` hands the turn to the `OutOfBoundsHandler`; malformed JSON does not panic here.

Each step's `name` is then looked up in `behaviorLookup`, a map built from `Behaviors` and keyed by `Behavior.Name()`. A hit goes through `openStep`: `copyStep(b)`, the behavior's `Copy()` asserted back to `Step`, then `Open` on that copy, handed the direction as a user-role message with an empty annotation map and the whole `messages` list. What `Open` returns is the step's opening list, the direction alone unless the tree's `Opening` says otherwise, and an empty return falls back to the direction alone, since a step must never open empty. Every message on the list is then given its own annotation map, a copy of whatever it carried with every inherited `Cross` cleared (Chapter 4), so a state that later pins on a message the tree borrowed from the transcript writes the step's copy and not the transcript, and a crossed pin the borrowed message already carried is not crossed again on the step's first turn. Then the first message gets two pins: `raw_history`, the whole `messages` list at planning time, and `$context`, whatever `messages.GetAnnotation("$context")` returned. A miss whose name lowercases to `re-plan` becomes a `ReplanTombstone` with a `nil` `Behavior` and its direction as its one message. Any other miss is `panic("No plan named … found!")`. Last, a plan consisting only of a tombstone is emptied; there is nothing to re-plan from.

`Opening` is the tree's side of that call, and it has three forms. Leave it nil and the step opens with the planner's direction, which is the right input when the planner is decomposing: "a haiku and a sonnet about the sea" becomes two steps, each handed only its slice. Return a selection from the transcript and the step opens with the user's words alone. Return the selection with the direction appended and it opens with both. The selectors that serve `Intake` serve here too:

```go
// the user's words alone
tree.Opening = func(_ arboreal.AnnotatedMessage, t arboreal.AnnotatedMessages) arboreal.AnnotatedMessages {
	return arboreal.TrailingUserMessages(t)
}

// the user's words, then the planner's direction
tree.Opening = func(d arboreal.AnnotatedMessage, t arboreal.AnnotatedMessages) arboreal.AnnotatedMessages {
	return append(arboreal.TrailingUserMessages(t), d)
}
```

The direction goes last, for two reasons. The list then reads in time order, what the user said and then the planner's note about it, and the note sits where `resume` puts each turn's new messages: last, as the newest. And an annotation-mode `LLMCompletionState` pins on the last user-role message of the list, `AddTraceInformation` on the last message, so with the direction last those pins land on the planner's note, the same message they land on by default; with the words alone they land on the step's own copy of the user's message. The same rule decides what such a state reads: annotation mode sends the model only its system prompt and that last user-role message (Chapter 4), so an extractor in a tree that opens with the words and then the direction reads the direction, and a tree whose extractor needs the words opens with them alone. An opener that puts the direction first is not refused; it gets the list it asked for. Opening and resume are separate selections, so a tree may open with the whole transcript, `append(arboreal.AnnotatedMessages(nil), t...)` or `t` itself since `openStep` copies the list, and still resume through the default `Intake`. One turn to know about: on a re-plan round, `Execute` has already rewritten the transcript's last user message into the tombstone's direction plus the summaries before `Plan` runs again (step 4 below), so an opener that borrows the user's words on that round gets that rewritten text, not what the user typed. Like `Intake`, `Opening` is code rather than state: snapshots do not carry it, `Copy` hands the same func to every step's copy, and it must not capture the tree it is set on. Chapter 14's bookshelf sets it on the one tree that needs a title as the customer typed it.

### `Execute`

`Execute(ctx, messages)` reads the plan from `e.plan`, reports back by writing `e.Output`, `e.ToolCalls`, `e.Crossed` and `e.plan`, and returns a `Signal`: `nil` when `Output` holds the reply, an `*ErrorSignal` when the call that was to write it failed, with `Output` left empty. In order:

1. **Empty plan** → the `OutOfBoundsHandler` is called with the last user message and its last message becomes `Output`. With no handler, `Output` is the literal string `Please set an out-of-bounds handler, this request was unable to be planned.` The handler gets a one-message list, a copy of `messages.LastMessage()` with a fresh annotation map, so it starts from the user's words rather than a direction. If the handler returns an `*ErrorSignal`, `Execute` returns it and `Output` stays empty. Whatever the handler returned, the messages it added past the one it was given are read for tool calls, as step 5 describes for a step, under the handler's `Name()`; name a handler that calls tools, or its records carry an empty `Step`. The annotations it marked to cross are folded as step 5 describes too, over its whole list: the one message it was given was built here with an empty map, so anything marked on it is the handler's own. Either way `Execute` returns here.
2. **Fan out** — `executePlan` starts one goroutine per non-tombstone step and calls `step.Behavior.Call(ctx, step.Messages)`; results are collected through a buffered channel after `WaitGroup.Wait`. Each goroutine writes the returned list back into `step.Messages`, which is how a paused step keeps its own growing transcript, and pushes a `PlanResult{Messages, Step, Signal}` onto the channel. Results arrive in completion order, not plan order, so the summaries below are in whatever order the steps finished.
3. **Triage by signal** — `nil`: the step's last message joins the summaries; `*ErrorSignal`: `"Error occurred: …"` joins the summaries; `*CollectUserInputSignal`: the step is kept for the next turn *and* its last message joins the summaries. Any other signal (a leaked `*SkipSignal`) matches nothing: the step is neither summarized nor kept. (No retry ever happens, Chapter 6.)
4. **Re-plan** — if the last step is a tombstone and nothing is paused: the direction plus all summaries are written into the last user message, `planDepth` is incremented, and `Plan` runs again, then `Execute`'s body re-enters itself (the unexported `execute`, not `Execute`, so the field step 5 describes is not cleared) — until `MaxPlanDepth`; the second round's tool-call records are appended after the first round's, not in place of them, and the first round's crossed annotations keep their slots when the second round's are folded in (step 5). The write is literal: `messages[len(messages)-1].Content` becomes the tombstone's direction (or, if the planner left it empty, the user's message) followed by every summary, so the executive's `History` now ends with a rewritten user message, which the next planning call sees as the request. Once `planDepth` reaches `MaxPlanDepth` the rewrite still happens, but `Execute` jumps to the reply step with an empty kept plan. If some step *is* paused, the tombstone is carried into the kept plan behind the paused steps, to fire once they finish; by then `Call` or `RunLoop` will have appended each later user message to it too, so its "direction" is the latest user message.
5. **Reply** — the kept (paused) steps become the new `plan`. The reply is decided by what ran this turn, not by how many steps are kept. If exactly one step ran and it ended with `nil` or a pause, `Output` is its last message and no model is called; a carried `Re-plan` tombstone is not a step that ran and does not change this. Otherwise a summarizer prompt (`executiveSummarizerPrompt`, or `workInProcessSummarizerPrompt` when steps are still pending) merges the summaries into one reply with another `LLMCompletionState` call, made through a named state, `summarize`, whose id is the executive's with `-summarize` appended, so its begin and end ride the trace like any state's. That call runs on `SummaryModel`, or on the context default when the field is empty; it does not inherit `PlannerModel`. Both prompts open with the interpolated `Preamble`. The finished-plan prompt carries a transcript of `messages`, one `role: content` line per message, then the summaries; the work-in-process prompt asks only to rephrase the summaries as a single question or statement. The state is called on a one-message list, `*messages.LastMessage()`, so the model sees the system prompt and the user's last message. If that call fails, `Execute` leaves `Output` empty and returns the state's `*ErrorSignal`; it never substitutes the user's message for a reply. `Execute` returns `nil` whenever `Output` holds a reply. Beside `Output`, `Execute` reports `ToolCalls`, a `[]ToolCallRecord` assembled once every goroutine has returned: for each step in plan order, whatever signal it ended with, every `function`-role message the step added this turn that directly follows an assistant message whose first tool call names the same tool becomes one `ToolCallRecord{Step, Tool, Arguments, Result}`, where `Step` is the behavior's `Name()`, `Arguments` a shallow copy of the call's map and `Result` the string the tool answered with. "This turn" is literal: `executePlan` notes how long each step's list was before the fan-out and reads only what came after, so a paused step's earlier turns are never re-recorded; the handler's calls in step 1 are read the same way. The field is cleared at the start of every `Execute`, is empty on a turn where no tool ran, and on a failed turn still holds the calls that ran before the failure. Beside both, `Execute` reports `Crossed`, a `[]Annotation` folded once every goroutine has returned, in plan order: for each step, and for each name on this turn's part of its list, the newest pin of that name, read newest message first as `GetAnnotation` reads, is the step's value, and it crosses when its `Cross.Enabled` is set (Chapter 4), with its `Name` set to the map key it was stored under; an older pin under the same name is never read, marked or not; the five reserved names are skipped, whatever their `Cross`. This turn's part starts earlier than the record's, which reads only what the behavior appended: at the first message the resume branch appended, where a state pins on a resumed step, or at the head of the opening list for a step planned this turn (the direction alone unless `Opening` says otherwise), so a pin from an earlier turn is never crossed again, and the copy a step receives on resume has every inherited `Cross` cleared, so a mark the application put on its own transcript message is not crossed back either. Across steps the first in plan order to cross a name owns its slot, with its `Explanation`; a later step's value for the same name goes through the placed annotation's `Merge`, or is dropped when it has none, and the later step's own `Merge` is never consulted. `Crossed` is cleared where `ToolCalls` is, survives the re-plan recursion the same way, and on a failed turn holds what was resolved before the failure; every outcome of the fold is reported on `Call`'s `end_call` (Chapter 11).

### `Call` and `RunLoop` are twins

Both methods run the same turn: if `plan` is empty, call `Plan`, which opens each step through its tree's `Open`; otherwise `resume`: for each pending step, append what its tree's `Select` picks from the transcript — the last user message since the agent last spoke, or nothing when there is none, unless the tree's `Intake` field says otherwise, and `arboreal.TrailingUserMessages` for a tree that must see every user message since the agent last spoke — as the step's own copies, each with a cloned annotation map in which every inherited `Cross` is cleared, without planning. A carried `Re-plan` tombstone has no behavior and takes nothing. Then `Execute`. Then append `Output` to the transcript as an assistant-role message, and when any tool ran this turn, pin `ToolCalls` onto that reply as the `tool_calls` annotation (the constant `AnnotationToolCalls`), and every entry of `Crossed` under its own name; on a turn with no calls the record is absent, not empty. Apart from its trace envelope, that is the whole of `Call`: it works on the list it is given, never touches `History`, and returns the appended list and `nil`. When `Execute` reports a failure, `Call` returns the list it was given, with nothing appended, and that `*ErrorSignal`, and its `end_call` carries the error; the record of whatever ran before the failure is then on `ToolCalls` only, and what crossed on `Crossed` only, since no reply was appended to carry them; a pause inside is still not reported. It is the executive as a nested behavior, and it is what `examples/oneshot` and `examples/snapshot-simple` use, one turn per process. `RunLoop(ctx, channel)` wraps the same dance in `for { Receive; …; Send }` over a `Channel`: it appends what `Receive` returned to `e.History`, runs the turn on `e.History`, appends `Output` to it, and sends `Output` back with the inbound message's `Id`. It returns when `Receive` or `Send` returns an error, which `TerminalChannel` never does (Chapter 1), or when a turn produced no reply: its error wraps that failure (`errors.As` recovers the `*ErrorSignal`), `History` ends on the user's message as the turn left it, and nothing is sent, so a channel peer waiting on a reply to that message's `Id` is left waiting, and what to send it is the caller's decision.

`History`, then, is the executive's transcript: user messages and outputs, in order, each output carrying the turn's tool-call record when there was one and whatever the turn's steps marked to cross. Each step's `Messages` is the model's view: what its `Opening` chose at planning, the direction alone when it is nil, the tree's replies, and whatever its `Intake` selected on each resume — by default, each turn's last user message. These are Chapter 2's two conversations, kept apart by these two methods.

### `Preamble` as the steering wheel

`Preamble` is the only free-text control you have over the planner, and the examples in this book use it for three things. `examples/snapshot-simple` uses it to keep directions faithful, `When writing the "direction" for a step, restate the user's message faithfully in the third person, quoting it`, which is the fix for the drift Chapter 2 traced when a behavior wants the planner's gist; a behavior that wants the words themselves sets `Opening` instead (see `Plan`, above). `examples/poetry` uses it to give the assistant an identity, `You are a poetry service`, and to tell the planner when to return `[]`, which is the only deliberate way to reach the `OutOfBoundsHandler`; otherwise it fires only on a lone `Re-plan` or a plan `fixJSON` could not repair. Nothing in `executivePlannerPrompt` invites an empty answer, so without that sentence the planner forces every message onto some behavior. Because `interpolatedPreamble` is also the first line of both summarizer prompts, tone and identity instructions carry into the final reply without being repeated in every state's `System`.

## Coming from LangGraph

The closest LangGraph shape is a supervisor that routes to sub-graphs; the rows for `create_react_agent` and `Send` in Chapter 3 both point here. The difference is cadence. A supervisor picks one worker, waits, and picks again; the executive's planner emits the whole list at once, `executePlan` runs every step in parallel, the way `Send` fans out over a list, and a summarizer joins the results. There is no ReAct interleaving inside the executive: a tree cannot ask the planner for another step mid-run, and the only iterative element is `Re-plan`, which folds the summaries back into the request and plans again, bounded by `MaxPlanDepth`. Selection is prompt-and-parse, not function calling (Chapter 3), so a misspelt name is a panic, not a correction. The mapping breaks at the decision itself: in LangGraph your code decides how many branches a `Send` produces and which worker a supervisor calls, while here the planning model decides both, by writing more or fewer steps, and your only lever is the prompt.

## Sharp edges

```admonish warning title="Sharp edge"
`Behaviors` is a `[]Step`, and in the framework only `*BehaviorTree` satisfies `Step`, so a bare `*BehaviorState` or a nested `*TodoListExecutive` in it is a compile error. The reason the planner needs a tree is older than the type: the planner prompt is a `text/template` that renders `{{ .BehaviorName }}: {{ .BehaviorDescription }}` — the tree's struct fields, not the `Behavior` interface's `Name()`/`Description()` methods — and would panic on anything else (`can't evaluate field BehaviorName`). Wrap anything that is not a tree in a one-state tree. Through the Lua binding, `arboreal.planner` raises an argument error for a non-tree instead.
```

```admonish warning title="Sharp edge"
`Plan` panics on model misbehavior: a plan whose `data` field is not a string (`could not put a plan together!`), a missing annotation (`no valid plan annotation!`), a step naming a behavior that does not exist (`No plan named … found!`), and an error from the planning call. There is no `recover` in the framework, so one bad plan takes down the process. Until this is fixed, keep behavior names short and distinct, and put a `recover` in the goroutine that calls `RunLoop`.
```

```admonish warning title="Sharp edge"
Behaviors are matched by `Name()`, case-sensitively; `re-plan` is matched case-insensitively. Two behaviors with the same name silently shadow each other (last one wins), and a behavior named `Re-plan` silently disables re-planning (it wins the lookup before the tombstone check).
```

```admonish warning title="Sharp edge"
The planner, the JSON repair and the summarizer each resolve their model the way a state does: the executive's `PlannerModel`, `RepairModel` or `SummaryModel` if set, else the context default from `WithDefaultModel`, else the built-in `openai:gpt-4o-mini` fallback. An executive that names no model, on a context with no default, therefore needs `OPENAI_TOKEN` whatever its states use. `RepairModel` is the one field that inherits another: empty, it takes the resolved `PlannerModel` (Chapter 15).
```

```admonish warning title="Sharp edge"
`Copy()` on an executive dereferences `OutOfBoundsHandler` without a nil check and copies configuration only — not `History`, `plan`, `Output`, `ToolCalls` or `Crossed`. The Go core never calls it (only the Lua binding's `copy` can): `Behaviors` must hold trees, and `BehaviorTree.Copy` shares its nodes by pointer, so an executive wrapped in a tree is *shared* by every plan step and every turn, keeping its `plan` between them, and because `Call` never returns a pause signal (only `nil`, or an `*ErrorSignal` for a failed reply), a pause inside it never pauses the enclosing tree. Set `OutOfBoundsHandler` anyway, and do not nest an executive that pauses.
```

```admonish warning title="Sharp edge"
`Output` is a side channel — `Execute` returns only a `Signal` and delivers the reply by mutating the field (which carries the author's `FIXME`) — and `ToolCalls` and `Crossed` are the same shape of channel beside it, cleared on the next `Execute`; the reply's `tool_calls` annotation holds that same slice, not a copy. Read all three immediately after `Execute`, and check the signal, since a failed turn leaves `Output` empty; or use `Call`, which appends the reply, with the record and the crossed annotations pinned to it, to the messages for you.

The first step in plan order owns a crossed name's slot. A step that wants to influence the merge from second place cannot: put the `Merge` on the pin of the step that plans first, or use distinct names. A `Merge` runs your code inside the executive's turn, on the calling goroutine, after the parallel steps have joined: a panic in it unwinds out of `Execute` to your caller, not out of a step's goroutine, and it must not block. It must return a new value rather than mutate `mine` or `theirs`, since either may still be a step's own pin. And the copy a step receives on resume is one level deep: the step gets its own annotation map and its own `Annotation` values, but a `Data` that is itself a map or slice is still the transcript's, as are the embedded message's `Meta`, `MultiContent` and `ToolCalls`. Replace annotations; never mutate an inherited `Data` in place.
```

```admonish warning title="Sharp edge"
An empty `Preamble` is not empty in the prompt. `AnnotationTemplate.Parse("")` decodes one rune from an empty string, gets `utf8.RuneError`, and emits a text block containing U+FFFD, so `interpolatedPreamble` puts a `�` at the top of the planner and summarizer prompts whenever `Preamble` is unset (as in the quickstart). Set a one-line `Preamble` on every executive.
```

```admonish warning title="Sharp edge"
`Execute` returns its failure as a signal, but the Lua engine's `Execute` binding (`engine/lua_planner.go`) discards it and its `Output` getter reads the field unchanged, so a Lua script sees an empty reply where Go sees an `*ErrorSignal`.
```

```admonish warning title="Sharp edge"
The planner sees only the latest message of `History` and up to three before it, not the whole transcript. Long-range context has to reach the behaviors through the step's own messages (which grow on resume) or through annotations, not through the planner.
```

```admonish warning title="Sharp edge"
User text reaches the planner and summarizer prompts inside `System`, which `LLMCompletionState` parses as an `AnnotationTemplate`. A message containing an unclosed `{{` makes `Plan` panic on the next turns it is quoted, and makes every finished-plan summarizer call fail; `{{ name }}` in user text is substituted away. Escape braces in `History` before the executive sees them.
```

## The trace, re-read

Chapter 2 asked you to read it twice. Here is the second reading, in the vocabulary of Part II.

**Step 1 — Receive.** `RunLoop` blocks in `Channel.Receive`; `TerminalChannel` prints `[User Message]` and returns your lines as one `ChannelMessage`. `RunLoop` wraps the content as a user-role message and appends it to `e.History` with `AppendToMessages`, which gives it an empty, non-nil annotation map.

**Step 2 — Plan.** `len(e.plan)` is zero, so `RunLoop` calls `Plan(ctx, e.History)`, which renders `executivePlannerPrompt` with `chat_behavior: A conversational bot` as the only behavior line under the reserved `Re-plan` and has an `LLMCompletionState` in annotation mode pin the model's JSON onto your one message under `plan`. The one step it parses out becomes a `Copy()` of `chatBehavior` plus one user-role message holding the planner's direction.

**Step 3 — Execute, fan out.** `Execute(ctx, e.History)` hands the one-step plan to `executePlan`, which starts one goroutine, calls `step.Behavior.Call(ctx, step.Messages)`, and collects the `PlanResult` through the buffered channel after `WaitGroup.Wait`.

**Step 4 — The tree walks its states.** `BehaviorTree.Call` finds an empty `State` stack, pushes `Graph.Initial()`, which is `chatState`, and resets `Traversed`. `chatState` sends the direction to `gpt-4o-mini`, appends the reply, and returns `nil`; the tree pushes `pauseState`, pops it, and gets a `*CollectUserInputSignal`, so it pushes the pause state's children (there are none) and returns the messages and the signal with the stack still empty.

**Step 5 — Execute decides the reply.** Triage matches `*CollectUserInputSignal`: the step is kept in the new `plan` and its last message joins the summaries. One step ran and it paused, so `Execute` sets `Output` to that message, resets `planDepth`, and returns `nil` without a summarizer call.

**Step 6 — Send.** `RunLoop` appends `Output` to `e.History` as an assistant-role message and calls `Channel.Send`; `TerminalChannel` prints `[Assistant Response]` and the text. `History` holds two messages, `plan` holds one paused step, and `RunLoop` returns to `Receive`.

**Step 7 — The next turn.** Your second message is appended to `e.History`, but `len(e.plan)` is one, so `Plan` is skipped and `resume` appends what the tree selects from `e.History` — by default its last user message, your second message — to `step.Messages` instead, as the step's own copy with its own annotation map. `Execute` calls the same tree copy; its stack is empty, so `BehaviorTree.Call` restarts from `chatState` on the three-message list, `pauseState` pauses again, the step is kept again, and, as the only step that ran, its reply is `Output` again.

Everything on that page is now something you have built or read.

```admonish example title="Recap"
- The executive holds a flat list of behaviors; a planning LLM picks steps by **name** and writes each a **direction**.
- Steps run concurrently on `Copy()`s; signals decide whether a step is summarized, kept for next turn, or reported as an error.
- One step ran and finished or paused → its last message is the reply, as written; anything else → a summarizer call merges the summaries. A failed reply call comes back as a signal, never as an echo of the user.
- `Call` and `RunLoop` are the same turn; `RunLoop` adds the channel loop. Both pin the turn's executed tool calls onto the reply they append, as `tool_calls`, and every annotation a step marked with `Cross`; a finished step's calls and marked pins survive the step.
- `BehaviorName`/`BehaviorDescription` and `Preamble` are the interface to the planner. Always set `OutOfBoundsHandler` and a `Preamble`.
```
