# Messages and annotations

## Why this exists

Every agent framework needs somewhere to keep the state that flows between steps: the entity one step extracted, the documents the next one retrieved. LangGraph's answer is the `State` schema, a typed dictionary every node reads and patches. A classical behavior tree's answer is the blackboard, a shared bag of keys every node can read and write. Both are a second data structure that has to be kept in step with the conversation.

Arboreal's answer is that the conversation *is* the state. A behavior takes the message list and returns the message list, and anything one state wants to hand to a later state is attached to a message as a named *annotation*. There is no schema to declare, no reducer to write, and no second structure to keep in sync with the transcript, because the transcript is the only structure there is.

## The types

```go
type Annotation struct {
    Name        string
    Data        any
    Explanation string
}

type AnnotatedMessage struct {
    llm.ChatCompletionMessage
    Annotations map[string]Annotation
}

type AnnotatedMessages []AnnotatedMessage
```

These three types in `annotation.go` are the whole data model. `AnnotatedMessage` embeds the provider-neutral `llm.ChatCompletionMessage` from `llm/chat.go`, which carries `Role`, `Content`, `ToolCalls`, `Name` and a few provider details, so `m.Role` and `m.Content` work directly on an annotated message. `Annotations` maps a name to an `Annotation`: the `Name` again, a `Data` value of any type, and a free-text `Explanation` that the framework fills in for the `$` meta-annotations and otherwise leaves to you. `AnnotatedMessages` is the list every `Call` receives and returns. Its `ChatCompletionMessages()` method strips the annotations and returns the plain `[]llm.ChatCompletionMessage`; that is what gets sent to a model, so a model never sees an annotation. `LastMessage()` returns a pointer to the final element, or `nil` for an empty list.

## Run it

`examples/annotations-probe` is the smallest program that shows an annotation being written. It skips the executive and calls a two-state tree directly; Chapter 7 explains trees, and for now all you need is that `tree.Call` runs `extractName` and then `extractProfession` on the list you give it and hands the list back. Both states are `LLMCompletionState`s with the `Annotation` option set, which changes what the state does with the model's reply: instead of appending it as an assistant message, the state parses it and pins it onto the user's message under the given name. The system prompts ask for a JSON object with a `data` field because that is the shape the state expects back.

```go
{{#include ../../../examples/annotations-probe/main.go}}
```

Run it with `go run ./examples/annotations-probe`; it takes no input and exits on its own. This is one run's output. The extracted values are the model's and can differ between runs, and because `dump` ranges over a Go map, the two annotation lines can come out in either order.

```text
=== history BEFORE tree.Call ===
[0] role=user      content="Hi, I'm Joe and I work as a pirate on the Black Pearl."

(tree.Call returned signal: <nil>)

=== history AFTER tree.Call ===
[0] role=user      content="Hi, I'm Joe and I work as a pirate on the Black Pearl."
      annotation "name"               data="Joe"
      annotation "profession"         data="pirate"
```

Look at what did not change. Two model calls were made and the list still has one message; the user's words are untouched. What changed is the map on that message, which now holds a `name` and a `profession` that any later state, or any prompt template, can read.

A note on the source, since the tree's third argument is empty: that argument, `Example`, is a sample direction for the executive's planner (Chapter 8). No executive runs this tree, so nothing reads it, and the user's request enters only through the message handed to `tree.Call`.

## How it works

### Reading annotations: `GetAnnotation`

`AnnotatedMessages.GetAnnotation(name)` walks the list from the newest message to the oldest and returns the first annotation stored under `name`. Newest-first means the most recent write wins: a later state can override an earlier one by writing the same name onto a later message. The return value is a pointer to a copy, so writing through it changes nothing in the history.

If nothing matches and the name starts with `$`, `GetAnnotation` synthesizes one of three built-in *meta-annotations* instead of returning `nil`: `$last_message`, the `Content` of the last message in the list (it dereferences the last message, so it panics on an empty history); `$date`, the current time in RFC 3339; and `$date_llm`, the same date phrased for a prompt, as `Today's date is: …`. Any other `$` name, and any missing plain name, returns `nil`.

`examples/get-annotation` runs each of those rules against a three-message history, with no model involved. `pref` is written on the first message and again on the third; `name` only on the first.

```go
{{#include ../../../examples/get-annotation/main.go}}
```

Run it with `go run ./examples/get-annotation`; it needs no `OPENAI_TOKEN`. The two date lines are whatever now is; everything else is fixed:

```text
pref           -> "tea"
name           -> "Joe"
pref           -> "tea"
$last_message  -> "Actually, make that tea."
$date          -> "2026-09-01T19:11:54+02:00"
$date_llm      -> "Today's date is: Tue Sep 01 19:11:54 +0200 2026."
$date          -> "1999-12-31T23:59:59Z"
age            -> nil
$user          -> nil
```

`pref` is `tea`, not `coffee`: the walk starts at message 2 and stops there. `name` is not on message 2 or 1, so the walk reaches back to message 0. The third line is still `tea` after `p.Data = "cocoa"`, because `p` points at a copy. The two dates are synthesized, and the third `$date` shows that a stored annotation takes precedence over the built-in one: the walk finds it before the `$` fallback is reached. `age` and `$user` are the two `nil` cases.

`FlattenedAnnotations()` collects every annotation on every message into one map, oldest to newest, so here too the last write wins. One quirk: it keys the result by each annotation's `Name` field, not by the map key it was stored under. An annotation-mode state fills `Name` only on its fallback path (unparseable reply or `data: null`); when the model's JSON parses, `Name` is whatever the model sent, usually nothing, so those annotations land under the empty string in the flattened map. Prefer `GetAnnotation` by key.

### Writing annotations

There are two ways to get an annotation into a history, and which one applies depends on whether the message it belongs on exists yet.

**Appending a message.** `AppendToMessages(history, msg)` wraps an `llm.ChatCompletionMessage` in an `AnnotatedMessage`, appends it, and returns the new list. The wrapper's `Annotations` map is empty but not `nil`, so the message is ready to be written to. `history` may be `nil`; the first call makes the list. The probe builds its history this way, and so does `RunLoop` for every incoming user message.

**Writing into an existing message.** To annotate a message already in the list, assign into its map: `history.LastMessage().Annotations["name"] = Annotation{Data: "Joe"}`. The map key is what `GetAnnotation` and the templates look up, so the struct's own `Name` field can stay empty; `Explanation` is free text for your own use. This is how the framework writes: an annotation-mode `LLMCompletionState` stores the parsed reply this way on the last user message. It is also how the lookup states in `examples/crm` work: after a database hit they replace the model's partial `name` with the full name from the row and add a `membank` beside it.

The second idiom assumes the map exists, and it does not always. A message built with a struct literal has a `nil` map, and the reply that `LLMCompletionState` appends in its normal mode is built exactly that way, as `AnnotatedMessage{ChatCompletionMessage: res.Message}`. Writing into a `nil` map panics. So on any message you did not create with `AppendToMessages`, check the map and `make` it first, which is what `evalIntoAnnotation` and `AddTraceInformation` both do. All three cases in one place:

```go
// Append: the map comes ready to use.
history := arboreal.AppendToMessages(nil, llm.ChatCompletionMessage{
	Role:    llm.ChatMessageRoleUser,
	Content: "Hi, I'm Joe.",
})

// Write into an existing message.
history.LastMessage().Annotations["name"] = arboreal.Annotation{Data: "Joe"}

// A message built by hand has a nil map, like every reply a normal-mode
// LLMCompletionState appends.
history = append(history, arboreal.AnnotatedMessage{
	ChatCompletionMessage: llm.ChatCompletionMessage{
		Role:    llm.ChatMessageRoleAssistant,
		Content: "Hello, Joe.",
	},
})
history.LastMessage().Annotations["greeted"] = arboreal.Annotation{Data: true}
// panic: assignment to entry in nil map

// Make the map first.
if history.LastMessage().Annotations == nil {
	history.LastMessage().Annotations = make(map[string]arboreal.Annotation)
}
history.LastMessage().Annotations["greeted"] = arboreal.Annotation{Data: true}
```

### Templates: `AnnotationTemplate`

The `System` string of `LLMCompletionOptions`, and the executive's `Preamble`, are not sent as written. Both are parsed by `AnnotationTemplate`, a small mustache-like language whose variables are annotation names, and rendered against the history the state was called with. There are three forms:

| Form | Renders as |
|---|---|
| `{{ name }}` | the annotation's `Data` coerced to a string (strings verbatim; ints, floats, `time.Time` formatted; anything else via `%v`); empty if absent |
| `{{ Preference: pref? }}` | multi-word block: words ending in `?` are annotation names; if **any** of them is empty, the **whole** block renders as empty — so a label disappears with its value |
| `{{ Sure?? pref? }}` | renders `Sure? tea` when `pref` is `tea`; `??` is a literal `?`, and only inside a multi-word block |

The rules, as `AnnotationTemplate.Parse` and `multiAnnotationBlock.Parse` apply them: a single word between the braces is an annotation name, looked up with `GetAnnotation`; more than one word makes a conditional block; inside it, the word before each `?` is an annotation name and everything else is literal text; if any piece renders empty the whole block renders empty; and `??` is a literal `?`. The conditional form needs at least two whitespace-separated words: `{{ name? }}` and `{{ Label:name? }}` are single-word blocks and look up annotations literally named `name?` and `Label:name?`.

Inside a multi-word block the text is split by Go's `text/scanner`, so the name before a `?` must be a single Go identifier: `first name?` and `user-name?` both look up `name`, and `$last_message?` looks up `last_message`. A multi-word block with no `?` at all is plain text. Keep annotation names you intend to template as single identifiers.

`{{ $last_message }}` and `{{ $date_llm }}` work anywhere a template does, as long as each is the whole block, since single-word blocks go straight to `GetAnnotation`. `examples/state-direct`, which Chapter 5 works through, puts `{{ $date_llm }}` in a system prompt.

`examples/annotation-template` renders one template that uses all three forms, with no model involved. It calls `AnnotationTemplate.Parse` and `Execute` by hand, which is exactly what `LLMCompletionState` does to its `System` option before each call, and it renders the same template twice against one message: first without a `pref` annotation, then with one.

```go
{{#include ../../../examples/annotation-template/main.go}}
```

Run it with `go run ./examples/annotation-template`; it needs no `OPENAI_TOKEN`, and its output is fixed:

```text
=== without pref ===
Hello Joe, welcome back. Visits so far: 3  
The user just said: Something warm, please.
=== with pref ===
Hello Joe, welcome back. Visits so far: 3 Preference: tea Sure? tea
The user just said: Something warm, please.
```

`{{ name }}` and `{{ Visits so far: visits? }}` render both times, and `visits`, an `int`, comes out as `3`. The two blocks that name `pref` render as nothing in the first pass, label and all, and come back in the second, where `??` has become a single `?`. `{{ $last_message }}` is the message's `Content`. Notice the two spaces after `3` in the first pass: only what is between the braces disappears, so the whitespace around an empty block stays in the prompt.

### `ExtraContext`

Templating is for prose you compose around a value. `ExtraContext` is the blunt alternative: an `LLMCompletionState` with `ExtraContext: []string{"name", "context"}` appends the heading `Extra Context:` to its rendered system prompt and, under it, the `Data` of each named annotation that `GetAnnotation` can find, one per paragraph, formatted with `%v`. Names that find nothing are skipped without complaint. It is a cheap way to feed a retrieval result or an extracted entity into a prompt without writing a template.

`examples/extra-context` shows what that adds to a prompt. It calls one `LLMCompletionState` directly, no tree and no executive, on a history whose user message carries a `name` and a `context` annotation set by hand, standing in for what a lookup state would have written. The state names both in `ExtraContext`, along with a `membank` that nothing has set, and because a state prepends its rendered system prompt to the history, the dump afterwards shows exactly what the model was sent.

```go
{{#include ../../../examples/extra-context/main.go}}
```

Run it with `go run ./examples/extra-context`; it needs `OPENAI_TOKEN` and exits on its own. The reply's wording is the model's; the system message is fixed:

```text
[0] system    "You are a CRM assistant. Answer in one sentence using only the extra context below.\n\nExtra Context:\n\nBob Marley\n\nBob Marley has one daughter, Cedella, born on August 23rd.\n\n"
[1] user      "When is Bob's daughter's birthday?"
[2] assistant "Bob Marley's daughter's birthday is on August 23rd."
```

Message 0 is the `System` string, then a blank line, the `Extra Context:` heading, and one paragraph per annotation found, in the order the names were listed. `membank` left no trace. Every paragraph, the last one included, is followed by a blank line, so the prompt ends in `\n\n`; the `%q` in the dump makes that visible.

One catch, in `LLMCompletionState` in `state.go`: the assembled system prompt is only inserted into the conversation when `options.System` is non-empty, so with `ExtraContext` set and `System` blank the extra context is built and never sent. Give the state at least a one-line `System`.

### Annotation mode on `LLMCompletionState`

Setting `Annotation: "name"` switches the state onto a different code path, `evalIntoAnnotation` in `state.go`. The `System` template is rendered first, as always, and any `ExtraContext` is appended to it. Then the state builds a two-message history of its own: the rendered system prompt, and the **last user-role message** it finds by scanning the history it was given. It sends **only** those two to the model, not the whole conversation. In the probe that message is the only one; in `examples/little-spy`, where the extractors run after several turns of chat, it is the user's latest answer, and everything before it is invisible to them.

The reply is expected to be a JSON object shaped like `Annotation`, `{"data": …, "explanation": …}`, and is parsed with `json.Unmarshal` straight into one. If that succeeds and `data` is not `null`, the parsed value is stored as is, which is why its `Name` is usually empty. If the parse fails, or if `data` is `null`, the state stores `{Name: "name", Data: <the raw reply string>}` instead. Either way the result is pinned onto that last user message, at the index remembered while scanning, under the given name, making the map first if needed. Nothing is appended, and `Terminal` and `AllowTools` are not consulted on this path. On success the signal is `nil`; if the provider cannot be created or the completion fails, the state returns an `ErrorSignal`, and on a completion failure the returned history is `nil`. Chapter 5 covers the rest of `LLMCompletionState`.

## Coming from LangGraph

A LangGraph `State` is a schema: `TypedDict` fields, each optionally with a reducer such as `add_messages` that says how a node's patch is merged into the existing value. The runtime validates keys, applies reducers, and merges the writes of parallel branches before the next superstep. Arboreal has none of that. An annotation is a name and an `any`, attached to whichever message produced it and found by searching backwards from the newest. The upside is that provenance is free: a fact extracted from the user's third message sits on that message, and a later fact with the same name sits on a later one, so the list is a history of the value as well as its current reading. The downside is that nothing checks names or types: a typo in `ExtraContext` injects nothing and reports nothing, and a state that asserts `Data.(string)` gets whatever the writer put there. The mapping breaks at reducers: LangGraph needs them because parallel branches write into one shared state, while Arboreal's concurrent steps each own a separate message list, so there is nothing to merge and no place to declare how.

## Sharp edges

```admonish warning title="Sharp edge"
`Annotation.Data` is `any`. It survives a JSON round trip (snapshots, Part III) lossily: numbers come back as `float64`, objects as `map[string]any`. Code that type-asserts `Data.(string)` after a restore must expect the JSON shapes. `examples/little-spy` normalizes with `fmt.Sprint(a.Data)`.
```

```admonish warning title="Sharp edge"
The framework uses the annotation map for its own bookkeeping. `__trace_annotations` is a breadcrumb written by `AddTraceInformation` and scrubbed by `BehaviorState.Call`; `plan`, `raw_history`, `$context` and `tool_calls` are written by the executive, the last onto the reply it appends, as the record of the turn's executed tool calls (Chapter 12). Iterate over annotations by the names you own, never over the whole map, and never write these names yourself.
```

```admonish warning title="Sharp edge"
`evalIntoAnnotation` stores the raw JSON text as `Data` when the model answers `{"data": null}` — so "not found" can arrive as the string `{"data": null}` rather than as an empty value. Treat any `Data` that starts with `{` as a miss, as `examples/little-spy` does.
```

```admonish warning title="Sharp edge"
Inside a multi-word block the text is tokenized as Go source. An apostrophe starts a char literal: `{{ The user's name: name? }}` prints `literal not terminated` to stderr and renders empty. `//` starts a comment: `{{ see https://x.com url? }}` renders `see https:`. Keep apostrophes and URLs outside `{{ }}`.
```

## Back to the trace

Step 2 of Chapter 2 is annotation mode at work. `Plan` runs an `LLMCompletionState` with `Annotation: "plan"` on a one-message history holding only your last message, and reads the answer back with `GetAnnotation("plan")`. The planner's answer is a JSON array, and an array does not unmarshal into an `Annotation` struct, so the reply takes the fallback path and arrives as the raw string, which is exactly what `Plan` wants: it asserts `Data.(string)` and parses the array itself. Then, for each step, `Plan` builds the step's first message with a struct literal whose `Annotations` already holds two entries: `raw_history`, whose `Data` is the executive's whole `History` at planning time, and `$context`, whose `Data` is whatever `GetAnnotation("$context")` returned, a nil `*Annotation` in the quickstart, so `%v` prints `<nil>`. That is how your words ride along with the direction, as an annotation nothing in the quickstart reads.

Steps 4 and 7 are `ChatCompletionMessages()` at work. `chatState` passes the step's list through that method before calling the model, so the model sees roles and content and never sees `raw_history`, `$context`, or anything else pinned to the messages.

```admonish example title="Recap"
- The message list is the state; annotations are named, typed slots attached to messages.
- `GetAnnotation` searches newest-first; `$last_message`, `$date`, `$date_llm` are built in.
- Templates: `{{ name }}`, conditional `{{ Label: name? }}`, escape `??`.
- `ExtraContext` injects annotations into a system prompt; annotation mode extracts them from a reply.
```
