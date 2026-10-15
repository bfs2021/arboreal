# Testing agents

## Why this exists

Strip a turn to its model calls and there are two or three: the planner, the state that writes the reply, and, when two or more steps ran, a summarizer. Everything else in this book — the walk order, the signal triage, the annotations riding on messages, the templates that build prompts, the snapshot round trip — is deterministic Go, and deterministic Go is what `go test` was built for. The temptation coming from other frameworks is to mock the model and test the whole agent; Arboreal, as the What you cannot stub section below shows, does not offer that seam. What it offers instead is a wide hermetic zone around a narrow model edge: lambda states make trees walkable without a token, `Restore` puts a plan in flight without a planner, and any logic you keep in plain functions over `AnnotatedMessages` tests like the ordinary code it is. This chapter maps the zone from the inside out — what tests without a model, then the edge where nothing does.

## What tests without a model

### Trees and signals

Chapter 6's `examples/signals` walked a six-state tree of recorder lambdas — each appends its name to a shared slice and returns the signal it was built with — and printed the order. The same example ships `main_test.go`, which turns those printed runs into a table test. First, one predicate per outcome:

```go
{{#include ../../../examples/signals/main_test.go:predicates}}
```

```go
{{#include ../../../examples/signals/main_test.go:cases}}
```

Each row is one of Chapter 6's scenarios: the signals to plant, how many `Call`s to make, the visit order expected from each call, and a predicate for the signal each call hands back. The example's `exercise` helper — Chapter 6 included it — builds a fresh tree per row, seeds a one-message history (a state must never see an empty one — `BehaviorState.Call` reads the last message after the lambda runs), and threads the history through the calls, so the pause row really is two calls on one tree, and the `a2 a1 b b1` resume order — reverse of insertion, Chapter 6's third sharp edge — is pinned by an assertion instead of a paragraph. Note what the predicates assert: not "a skip happened" but that the returned value type-asserts to `*arboreal.SkipSignal`, the *pointer* type. The framework's own switches match only pointer signal types, so a lambda returning `SkipSignal{}` by value misbehaves at runtime while compiling clean — and this predicate is precisely the test that catches it.

### Logic over histories

The states worth having usually wrap a plain function, and the plain function is where the tests go. `examples/little-spy` reads the facts its extractor states annotated onto the conversation through a single function, `learned(messages)`: walk the messages oldest to newest, collect the annotations named in `factKeys`, skip every shape the extractors use for "not found", and let a later answer overwrite an earlier one. It takes `AnnotatedMessages` and returns `map[string]string` — no state, no context, no model — so its test needs only a way to build annotated messages by hand:

```go
{{#include ../../../examples/little-spy/main_test.go:helper}}
```

```go
{{#include ../../../examples/little-spy/main_test.go:merge}}
```

The file's second test, `TestLearnedIgnoresUnknownAndKeepsLatest`, is Chapter 4's sharp edges recast as table rows: an age arriving as `float64(34)`, because `Annotation.Data` that has been through a snapshot comes back in JSON shapes; a first name arriving as the literal string `{"data": null}`, because `evalIntoAnnotation` stores the raw JSON when the model answers null. Both must be tolerated, and the test proves `learned` tolerates them — the `float64` normalized by `fmt.Sprint`, the brace-prefixed string skipped as a miss. A third test feeds only the framework's own annotations (`__trace_annotations`, `plan`) and expects an empty map: iterate by the names you own, and prove it. None of this required running the spy; it required keeping the merge out of the lambda and in a function.

### Templates

Chapter 4 explored `AnnotationTemplate` with a probe program, `examples/annotation-template`, that printed renderings for a human to read. The same `Parse`/`Execute` pair asserts just as well: build a history by hand, annotate it, render, compare. This one is small and chapter-specific enough to live inline rather than in an example:

```go
func TestTemplateForms(t *testing.T) {
	h := arboreal.AppendToMessages(nil, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Something warm, please.",
	})
	h[0].Annotations["name"] = arboreal.Annotation{Data: "Joe"}

	var tmpl arboreal.AnnotationTemplate
	if _, err := tmpl.Parse("Hello {{ name }}.{{ Preference: pref? }}"); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, h); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "Hello Joe."; got != want {
		t.Fatalf("rendered %q, want %q", got, want)
	}
}
```

`AppendToMessages` initializes the `Annotations` map, which is why the assignment on the next line is safe on a freshly built history. The template exercises two of Chapter 4's forms at once: the required `{{ name }}` renders its annotation, and the optional multi-word block `{{ Preference: pref? }}` vanishes whole because `pref` is absent — asserted by the exact string, not eyeballed. This matters more than it looks: `LLMCompletionState` runs every `System` option through exactly this code before every model call, so a test like this is a test of your prompts. The tokenizer traps from Chapter 4's sharp edges — an apostrophe or a URL inside a block — fail loudly under assertion instead of quietly in production.

### Snapshots, and seeding a plan

The executive's `plan` field is unexported, so a test cannot hand it a paused step directly — and without one, `Call` heads straight into `Plan`, which is a model call. Chapter 10 crafted a snapshot by hand and noted that seeding via `Restore` is the only way to put a plan in flight from outside the package, promising this chapter would turn that into a testing technique. Here it is, from `examples/snapshot-edges`' own `main_test.go`:

```go
{{#include ../../../examples/snapshot-edges/main_test.go:roundtrip}}
```

The pattern is the probe made rigorous: build the executive with pinned ids, `Restore` a crafted snapshot to seed the plan, append a user message, `Call`. The seeded plan makes `Call` take the resume branch — no planner — and the tree's canned state makes step execution model-free, so the whole turn is hermetic. The test then goes one leg further than the probe: it snapshots the paused executive, restores into a *second* freshly built one, and runs the same turn there, asserting the same reply — the round trip itself under test, in a package that imports no provider and needs no token.

A tool-recording test needs no model either. The executive reads a step's tool calls off the shape of its messages, not off the state that made them (Chapter 12), so a lambda state that appends the pair `LLMCompletionState`'s loop would — an assistant message carrying the call, then a `function`-role message carrying the result — is recorded like a real one. `toolTree` in the framework's own `executive_test.go` is that state wired before a canned reply, and the tests around it seed it through `Restore`, `Call` once, and decode the reply's `tool_calls` annotation with `ToolCallsFromAnnotation`: the same hermetic turn as above, with the record under assertion.

One aside on ids, since a crafted snapshot and a built executive must agree on them. Chapter 5 covered the generator: `GenerateStringIdentifier` draws from `crypto/rand` unless `ZEN_SEED_RNG` seeds it, so under the knob a test *can* predict an id it did not assign. Prefer not to need that: assign ids explicitly, as `snapshot-edges` pins `exec-edges` and `tree-greeter` through its constructor, and the question of what the generator will produce never arises. The env knob is a last resort, and the first sharp edge below says why.

## What you cannot stub

The model is not injectable. `LLMCompletionState`'s lambda constructs its provider inside itself — `llm.CreateModelProvider(options.Model, llm.ProviderOpenAI)` in `state.go` — on every call: there is no provider field on `LLMCompletionOptions`, no constructor parameter, and no context key a test could hang a fake on. Annotation mode is the same seam-free shape; `evalIntoAnnotation` builds its own provider the same way. And the planner is out of reach twice over: `Plan` in `executive.go` builds its own `LLMCompletionState` with `Annotation: "plan"` and calls its lambda directly, so planning always means a real model. That is the framework as it stands; this book documents it rather than pretending otherwise.

The workaround is structural, and it is the shape every hermetic test above already relies on. Keep model states thin — a prompt in, text or one annotation out — and put every branch, lookup, and merge in lambda states and plain functions around them, where the tests in this chapter reach. `little-spy`'s extractors are thin by construction (one system prompt, one annotation); everything the agent *does* with the extracted facts lives in `learned`, which tests in microseconds. What remains untestable without spending a token is then exactly the part a stub would tell you nothing about anyway: whether the model, given the prompt your template test proved it receives, exercises good judgment. That question has no hermetic answer, from any framework.

## Coming from LangGraph

The habit that does not transfer is `FakeListLLM` — binding a mock model that replays canned responses and asserting the graph's behavior around it. Arboreal has no equivalent because the provider is constructed inside the state, not passed to it; there is nothing to bind. The leverage point moves accordingly: instead of *swapping the model*, you *shrink what touches the model*, until the untested remainder is a thin prompt-to-text seam. The idiom that does transfer is seeding: where a LangGraph test hands a checkpointer a prepared state and invokes from there, an Arboreal test hands `Restore` a crafted snapshot and `Call`s — same move, with the snapshot map playing the checkpoint and your constructor's pinned ids playing the `thread_id`'s half of the contract.

## Sharp edges

```admonish warning title="Sharp edge"
`ZEN_SEED_RNG` is process-wide: set it in one test and every later `GenerateStringIdentifier` call in the binary follows the seeded sequence, so id expectations depend on test order. Set it in `TestMain` or not at all.
```

```admonish warning title="Sharp edge"
Copying a state copies its `HashId` (Chapter 5). In table-driven tests that build many trees from shared state values, two trees sharing one id can restore each other's snapshots; regenerate or assign ids per instance.
```

```admonish warning title="Sharp edge"
Returning a signal by value instead of by pointer is a bug the compiler mostly lets through — every framework switch matches pointer types, and `BehaviorState.Call` feeds each returned signal through `TraceForSignal`, which panics with `unknown Signal type` on a value, tracer or no tracer (Chapter 6; `examples/crm` does this). A one-line test per lambda asserting the returned signal's type reports the mistake by name instead of by panic; the signals test's predicates are that assertion, ready to copy.
```

## Back to the trace

Lay this chapter over Chapter 2's seven steps and the hermetic zone has a clean boundary. The snapshot round-trip test runs steps 3 through 6: step 3's fan-out over the seeded plan, step 4's walk with the model call replaced by a canned state at the state boundary, step 5's triage keeping the paused step and returning its reply as written, since it was the only step that ran, and step 6 minus its `Send`: `Call` appends the reply itself, and the test's assertions stand in for the channel. Step 2 — the planner — is the model edge, which is why the test enters through `Restore` instead; and step 5's other path, the summarizer that fires when two or more steps ran, is the same edge in different clothes. Everything the trace showed as framework machinery tests without a token; the two model calls the executive makes on its own — the planner, the summarizer — are the two places nothing here can follow.

```admonish example title="Recap"
- Hermetic: walks and signals, history logic, templates, snapshot round-trips.
- Seed a paused conversation through `Restore`; assign ids, don't mine them.
- Not stubable: the provider inside `LLMCompletionState`, the planner. Keep model states thin.
```
