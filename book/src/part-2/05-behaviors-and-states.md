# Behaviors and states

## Why this exists

One interface underlies everything in Arboreal. A state, a tree of states, and the executive that runs trees are all *behaviors*, and a behavior is anything that can take a conversation and return an updated conversation plus a signal. That single contract is why a tree can hold another tree, why the executive can hold trees, and why none of this needs a special node type: `BehaviorTree.Call` calls `state.Call` on whatever it pops off its stack and never asks what kind of thing it is. This chapter covers the interface and its leaf implementation, `BehaviorState`.

## The `Behavior` interface

```go
type Behavior interface {
    Hashable            // Hash() string
    Name() string
    Description() string
    Call(ctx context.Context, messages AnnotatedMessages) (AnnotatedMessages, Signal)
    Copy() Behavior
}
```

Five methods in `behavior.go`, and `Hashable` in `structs.go` is nothing more than `Hash() string`.

`Call` is the contract: history in, history plus signal out. The history is the `AnnotatedMessages` list from Chapter 4; the signal is a `Signal`, which Chapter 6 covers. A `nil` signal means "done, carry on"; anything else is an instruction to whoever called you.

`Hash` is the behavior's identity. `Graph.AddNode` in `structs.go` keys its nodes by it, `BehaviorTree.Call` keys its visited-set `Traversed` by it, and snapshots (Part III) record a tree's position as a list of hashes. Two behaviors with the same hash are, to the framework, the same behavior.

`Name` is how the executive matches a planner step back to a behavior (`behaviorLookup` in `executive.go`). The planner prompt itself reads `BehaviorName` and `BehaviorDescription` straight off the `BehaviorTree` struct, not through these methods — which is why only trees go in an executive's `Behaviors`. The field's type says so: it is a `[]Step`, where `Step` is `Behavior` plus one more one-method interface, `TranscriptSelector` — `Select(transcript AnnotatedMessages) AnnotatedMessages`, what the behavior takes from the transcript when the executive resumes it — and `*BehaviorTree` is the only type in the framework that satisfies both (Chapter 8).

`Copy` produces an independent instance with the same hash. The executive calls it once per step, so concurrent steps never share one stateful tree.

`*BehaviorState`, `*BehaviorTree` and `*TodoListExecutive` all implement the interface, all with pointer receivers.

## `BehaviorState`: one struct, a pluggable lambda

```go
type BehaviorState struct {
    StateName        string
    StateDescription string
    HashId           string
    ClientID         string
    Lambda           func(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal)
}
```

There are not four kinds of state. There is one struct in `state.go`, and four ways to fill in `Lambda`. The first four fields are metadata: `Name()` returns `StateName`, `Description()` returns `StateDescription`, `Hash()` returns `HashId`, and `ClientID` is copied onto every trace message the state emits. The behavior is entirely in the lambda, whose signature is `Call`'s signature.

`BehaviorState.Call` is identical for every state: it is a tracing envelope around `return b.Lambda(ctx, history)`. Before the lambda runs, `Call` looks for a `Trace` under the `arboreal_trace` key in the context, notes `len(history)` in `historySize`, and sends a `CallBegin` trace message; `Trace.Send` is nil-safe, so with no tracer configured these are no-ops. Afterwards, and only if a tracer was present (`isTracing`), it builds a list of `TraceHistoryOperation`s describing what the lambda did: one per annotation the lambda recorded via `AddTraceInformation`, and, in intent, one per appended message (in the current commit that loop ranges over the input slice, so appended messages never reach the trace). Then it scrubs the breadcrumb from the last message, sends `CallEnd` with the timing, the operations and the signal, and returns the lambda's history and signal unchanged. It peeks at the signal only to attach an `*ErrorSignal` to the trace; it never alters control flow.

That is why the four factories below behave identically as far as the tree is concerned. What differs is the lambda. `BehaviorState.Copy` copies all five fields into a fresh struct, `Lambda` included as the same func value, and returns its address.

### The four factories

| Factory | Returns | What the lambda does |
|---|---|---|
| `BehaviorState{HashId: …, Lambda: …}` (a literal) | `BehaviorState` | anything: SQL, vector recall, parsing, branching by returning a signal |
| `CannedResponseState(text)` | `*BehaviorState` | appends `text` as an assistant message |
| `PauseState(reason)` | `BehaviorState` | returns `&CollectUserInputSignal{Reason}` and touches nothing |
| `LLMCompletionState(opts)` | `BehaviorState` | renders a system prompt, calls a model, appends the reply (or extracts an annotation) |

Note the return types: one pointer, three values. `CannedResponseState` returns a pointer, which is what a `Behavior`-typed field such as the executive's `OutOfBoundsHandler` wants. The other three return values, and since `Behavior` is implemented on `*BehaviorState`, wiring one into a tree means taking its address: `tree.AddTransition(&chatState, &pauseState)`.

Writing your own state is a struct literal with a distinct `HashId` and a `Lambda`. The lambda reads what earlier states left on the history, runs whatever Go it likes, and returns the history with a signal. This one, modelled on `lookupClientQuery` in `examples/crm/main.go`, reads the `name` an annotation-mode state extracted (Chapter 4), looks it up in a map, and either annotates the last message with the record or asks the tree to skip its subtree:

```go
lookupClient := arboreal.BehaviorState{
    HashId: "lookup_client",
    Lambda: func(ctx context.Context, history arboreal.AnnotatedMessages) (arboreal.AnnotatedMessages, arboreal.Signal) {
        a := history.GetAnnotation("name")
        if a == nil {
            return history, &arboreal.SkipSignal{Reason: "no name was extracted"}
        }
        record, found := clients[strings.ToUpper(fmt.Sprint(a.Data))]
        if !found {
            return history, &arboreal.SkipSignal{Reason: "no such client"}
        }
        m := history.LastMessage()
        if m.Annotations == nil {
            m.Annotations = make(map[string]arboreal.Annotation)
        }
        m.Annotations["record"] = arboreal.Annotation{Data: record}
        return history, nil
    },
}
```

Two things to copy from it. Signals are returned as pointers, `&arboreal.SkipSignal{…}`, because `BehaviorTree.Call` matches on pointer types; Chapter 6 explains why, and why the value signals in `examples/crm` are a bug rather than a style. And on every path the lambda returns the history it was given, never `nil`: `BehaviorState.Call` dereferences `m.LastMessage()` after the lambda to scrub the breadcrumb, so a lambda that returns `nil` with an error signal on its error path, as several in `examples/crm` do (they compound it by returning the signal as a value), panics before the error is ever seen.

## Run it

`examples/state-direct` builds one `LLMCompletionState` with a templated system prompt and calls it by hand, with no tree and no executive, so you can see exactly what one state does to a conversation.

```go
{{#include ../../../examples/state-direct/main.go:state}}
```

```go
{{#include ../../../examples/state-direct/main.go:history}}
```

```go
{{#include ../../../examples/state-direct/main.go:call}}
```

```go
{{#include ../../../examples/state-direct/main.go:dump}}
```

Run it with `go run ./examples/state-direct`; it needs `OPENAI_TOKEN` and exits on its own. This is one run's output; the model's wording varies, and the date is whatever today is.

```text
[0] system    You are a terse assistant. Today's date is: Mon Aug 31 13:21:09 +0200 2026. Answer in one sentence.
[1] user      What day of the week is it today?
[2] assistant Today is Monday.
```

The dump shows the three things the state did. `{{ $date_llm }}` in the `System` template was rendered against the history, so the prompt at index 0 carries today's date where the source carried a placeholder. Because `System` was non-empty and the history had no system message, the rendered prompt was prepended as message 0, pushing the user's question to index 1. And the model's reply was appended as an assistant message at index 2, with a `nil` signal, which is why the `ErrorSignal` check in the call block did not fire.

## How `LLMCompletionState` works

```go
type LLMCompletionOptions struct {
    Name, Description string
    ClientID, Id      string
    System            string   // template, see Chapter 4
    Model             string   // "openai:gpt-4o-mini-2024-07-18"; empty = llm.GPT4oMini
    ExtraContext      []string // annotation names appended to the system prompt
    Annotation        string   // if set: extract into this annotation, append nothing
    Terminal          bool     // return TerminalSignal after the reply
    AllowTools        bool     // offer MCP tools from the context (Part III)
    Tools             []string // names to offer; empty = every tool on the mux (Part III)
}
```

`Name`, `Description`, `ClientID` and `Id` fill the struct's metadata fields and play no part in the call. The rest configure the lambda, which runs in this order.

1. `System` is parsed and rendered with `AnnotationTemplate` against the history the state was called with, even when it is empty. A template that fails to parse or render returns the history untouched with an `ErrorSignal`.
2. If `Annotation` is set, the lambda hands the rendered prompt to `evalIntoAnnotation` and returns whatever it returns; with `Tools` also set it returns an `ErrorSignal` instead, since the annotation path never offers tools. Chapter 4 covers the annotation path; none of the steps below run.
3. `CreateModelProvider(options.Model, llm.ProviderOpenAI)` picks a provider from the prefix of the model URI, defaulting to OpenAI when there is no recognized prefix. Failure to construct one, which for Anthropic means a missing `ANTHROPIC_TOKEN`, returns an `ErrorSignal` of type `StateErrorTypeUnrecoverable`.
4. `ExtraContext`, if any, is appended to the rendered prompt.
5. If `System` is non-empty, the assembled prompt becomes message 0: when the history already starts with a system message its `Content` is overwritten in place, otherwise a new system message with an empty, non-nil annotation map is prepended. When `System` is empty this step is skipped, and with it the extra context, which is the catch Chapter 4 noted.
6. An empty `Model` is replaced by `llm.GPT4oMini`, and the model is called with `history.ChatCompletionMessages()`, so roles and content only. A failed completion returns the history from step 5 with an `ErrorSignal` of type `StateErrorTypeUnknown`.
7. The reply is appended as a new `AnnotatedMessage` built from `res.Message`. It is a struct literal, so its `Annotations` map is `nil`; Chapter 4 said what that means for anyone who writes to it.
8. The signal is `nil`, or `&TerminalSignal{}` if `Terminal` is set. The flag changes nothing about the call itself; it matters only inside a tree, where `BehaviorTree.Call` ends the walk on it (Chapters 6 and 7 explain what happens to it after that).

`AllowTools` inserts a loop between steps 6 and 7: if an `MCPClientMux` is in the context, the request carries the client's tools — or, when `Tools` is set, only the tools it names, in that order — and the first tool call in each reply is executed and fed back until the model answers without one. `Tools` narrows the offered list to the names given; naming one the mux lacks, or setting it on a state that cannot offer tools (no `AllowTools`, no mux, or annotation mode), is an error before the model is called. Part III covers it.

### Model URIs

A model is named as `provider:model`. `llm.GPT4oMini` is `"openai:gpt-4o-mini-2024-07-18"` and `llm.ClaudeHaiku` is `"anthropic:claude-3-5-haiku-20241022"`; `llm/model.go` lists the rest. `ParseModelURI` splits on the first colon; if the part before it is `openai`, `anthropic`, `ollama` or `cluster` that is the provider and everything after it is the model name, colons included, and otherwise the whole string is the model name with an unknown provider, which `CreateModelProvider` turns into its default. The model name is passed through to the provider unchecked, so a typo is reported by the provider, not by Arboreal.

Three providers can be constructed in `llm/provider.go`. `openai` reads `OPENAI_TOKEN` (and optionally `OPENAI_PROJECT` and `OPENAI_ORG`) when the completion is made, in `createOpenAIClient`. `anthropic` reads `ANTHROPIC_TOKEN` when the provider is constructed, in `newAnthropicService`, so a missing key surfaces at step 3 above rather than at the call. `ollama` is a trap for completions in the current commit: the provider constructs, `OllamaService.CreateChatCompletion` in `llm/ollama.go` succeeds with an empty message without contacting anything, so an `ollama:` state appends an empty assistant message and returns `nil`, and nothing reports an error. Only `CreateEmbedding` reads `OLLAMA_SERVICE_URL`. The `cluster` prefix is recognized by the parser but has no case in `CreateModelProvider`, so it fails with `unknown model type: cluster`.

### Identity: `HashId`

`Hash()` returns `HashId`, and nothing else. The three factories generate one with `GenerateStringIdentifier("id-", 16)`: a random base32 string, sixteen characters including the `id-` prefix, drawn from `crypto/rand` unless the `ZEN_SEED_RNG` environment variable is set, in which case a seeded `math/rand` makes the sequence repeatable for tests. `LLMCompletionState` lets you pin the id through the `Id` option, and because `HashId` is an exported field you can set it on any state after the fact, which is how `examples/snapshot-simple` gives its `PauseState` a stable name.

Two states in one tree must never share an id. `Graph.AddNode` keys nodes by hash, so the second state never enters the graph at all: its transitions attach to the first and its lambda never runs. Stable ids also matter for snapshots (Part III).

## Coming from LangGraph

A LangGraph node is a function of the state: it receives the current `State`, returns a partial update, and the runtime merges the update through the schema's reducers. A `BehaviorState` is a function of the conversation that also returns a control-flow signal: it receives the whole message list and returns the whole message list, plus a `Signal` that says what the tree should do next. There is no `ToolNode`; the closest equivalent is `LLMCompletionState` with `AllowTools: true` and an MCP client in the context, which runs the tool loop inside the state. There is no `with_structured_output`; the idiom is annotation mode plus a system prompt that instructs the model to answer in JSON with a `data` field. The mapping breaks at the return value: a node's dictionary is a patch the runtime merges for you, while a lambda's return *is* the conversation from then on, so whatever you leave out of it is gone.

## Sharp edges

```admonish warning title="Sharp edge"
A state whose lambda returns an **empty** history panics: `BehaviorState.Call` in `state.go` dereferences `m.LastMessage()` after the lambda to scrub the trace breadcrumb. A tree that starts with a `PauseState` and is called on an empty history dies here. Always seed the history with at least one message (`AppendToMessages(nil, …)`), or start the tree with a state that appends.
```

```admonish warning title="Sharp edge"
Copying a state struct copies its `HashId`. Put both copies in one tree and `Graph.AddNode` treats the second as the first: its transitions attach to the original and its lambda never runs. Put them in different trees and the walk is fine, but `Snapshot.Restore` keys every behavior in the executive by hash, so a restore can rehydrate one tree with the other's state. `examples/crm` regenerates `HashId` with `GenerateStringIdentifier` after `evalForClientRecord := evalForClientQuery` for that reason.
```

```admonish warning title="Sharp edge"
An empty `Model` means OpenAI and `gpt-4o-mini`, and `CreateModelProvider` defaults an unprefixed model name to OpenAI too. A project that runs on Anthropic still needs `OPENAI_TOKEN` for any state that leaves `Model` empty — including the executive's own planner and summarizer states (Chapter 8).
```

```admonish warning title="Sharp edge"
`BehaviorTree.Copy()` copies the `Graph` by value, but the graph's `Nodes` slice holds the same `*BehaviorState` pointers, so every copy of a tree shares its state structs. That is harmless for the framework's own factories — a `BehaviorState` carries no execution state — but a `Lambda` that captures a mutable variable (a counter, a slice, a map) is shared across the concurrent plan steps the executive runs on those copies. Keep captured state read-only, or guard it with a mutex.
```

The first edge is not only about your own lambdas. `PauseState`'s lambda returns the history it was given, so an empty history in means an empty history out, and the framework's own factory trips the same dereference when it is the first state in a tree that is called with nothing. Inside the executive this cannot happen, because every step's list starts with the planner's direction; it happens when you call a tree directly, as Chapter 7 does. Nor is `PauseState` the only framework state that can return nothing: an annotation-mode `LLMCompletionState` whose completion fails returns a `nil` history from `evalIntoAnnotation`, so a network error in an extractor state inside a tree is a panic, not an `ErrorSignal` (`Plan` calls `.Lambda` directly and panics on the `ErrorSignal` itself).

## Back to the trace

Step 4 of Chapter 2 is two `BehaviorState`s doing what this chapter described. `chatState` is an `LLMCompletionState` with empty options, so steps 1, 3, 6, 7 and 8 with defaults and no system message: an empty `System` renders to a stray U+FFFD (Chapter 8) that step 5 never inserts, `Model` defaults to `gpt-4o-mini` through the OpenAI provider, the reply is appended, and the signal is `nil`, so the tree carries on to the child. `pauseState` is a `PauseState`: its lambda returns the same history and a `*CollectUserInputSignal`, and `BehaviorState.Call` passes both up unchanged.

Step 2 is an `LLMCompletionState` too, in annotation mode, but it is not called through `Call`. `Plan` constructs the state and invokes `.Lambda(ctx, history)` directly, bypassing the tracing envelope, so the planner's model call emits no trace messages and its `__trace_annotations` breadcrumb is never scrubbed, which is harmless because that one-message history is discarded once `plan` has been read. The summarizer in step 5 is not: it is a named state, `summarize`, called through `Call`, so its model call does appear on the trace, and its failure comes back as a signal rather than being lost.

```admonish example title="Recap"
- Everything is a `Behavior`; `Call(ctx, history) → (history, signal)` is the only contract.
- `BehaviorState` is one struct with a pluggable `Lambda`; `Call` is a tracing envelope around it.
- `LLMCompletionState` renders a templated system prompt, calls a model chosen by URI, appends the reply — or extracts an annotation.
- `HashId` is identity: unique within a tree, stable across runs if you want snapshots.
```
