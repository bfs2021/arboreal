# Patterns and pitfalls cookbook

This is the page to come back to. Every sharp edge the previous fourteen chapters marked is collected here in one index, keyed by what you will actually observe rather than by the identifier that causes it, and beside them the idioms this book's examples settled on. Nothing below is new material; it is the same framework, arranged for lookup instead of for reading.

## How to use this chapter

Pitfalls are indexed by symptom. Find the line that describes what you are seeing — a reply that drifts from what the user said, a restore that comes back empty, a turn that deadlocks before printing anything — and the row names the cause, the fix, and the chapter that explains why. The rows compress hard on purpose: each one is a pointer, and the linked chapter is the explanation. They are grouped into seven tables by the part of the framework the symptom belongs to, not by the chapter it came from, because a symptom rarely announces which chapter you should have read. Between them the tables consolidate every Sharp edge callout in the book, plus a few pitfalls the chapters teach in prose rather than in a callout, merged wherever several callouts turned out to describe one mechanism seen from different angles.

Recipes sit between the two. There are six: the rows that recur across chapters and cost more than a pointer's worth of explanation. Read them once — they are the traps that are far cheaper to recognize than to diagnose.

Patterns are indexed by intent — what you are trying to build. Each is a named idiom with its code shape and the chapter that derived it, named so that a code review can point at one instead of describing it.

## Pitfalls

### History & messages

| Symptom | Cause | What to do | Chapter |
|---|---|---|---|
| A terminal-driven agent never exits on Ctrl-D, or delivers only the first piped message and then spins on empty input | `TerminalChannel.Receive` never returns an error and rebuilds a fresh `bufio.Scanner` on every call | Use `TerminalChannel` for interactive runs only and quit with Ctrl-C; implement `Channel` yourself for piped or scripted input | [Ch 1](../part-1/01-quick-start.md) |
| A behavior's reply addresses the wrong person or drifts from what the user actually said | The behavior sees the planner's paraphrased `direction`, not the user's literal text | Add a `Preamble` telling the planner to restate the user's message faithfully, quoting it | [Ch 2](../part-1/02-anatomy-of-one-turn.md) |
| A reply looks wrong even though the visible transcript looks fine | `History` (the user-facing transcript) and a step's `Messages` (what the model saw) are two different conversations | Inspect the step's `Messages`, not `History`, when a reply looks wrong | [Ch 2](../part-1/02-anatomy-of-one-turn.md) |
| A state panics on a nil pointer the moment its lambda returns — a tree that opens with a `PauseState`, or any lambda that hands back an empty history | `BehaviorState.Call` dereferences `m.LastMessage()` after the lambda runs, which fails on an empty history | Seed the history with at least one message, or start the tree with a state that appends one | [Ch 5](../part-2/05-behaviors-and-states.md) |
| The planner "forgets" something said several turns ago | `Plan` only renders the latest message of `History` plus up to three before it, not the whole transcript | Carry long-range context through step messages (which grow on resume) or annotations, not the planner | [Ch 8](../part-2/08-the-executive.md) |

### Snapshots & restore

| Symptom | Cause | What to do | Chapter |
|---|---|---|---|
| On the second message the agent starts over — it re-asks what it already asked, or replans from scratch, with no restore anywhere in the picture | Calling `Plan` on an executive with a plan in flight resets the plan list first | Call `Plan` only on turn one; go through `Call` after that | [Ch 9](../part-3/09-a-turn-without-the-loop.md) |
| Everything works until a restore comes back with no transcript | `Call` never touches `History`; nothing assigns it for you | Assign `History` yourself before or after every `Call` | [Ch 9](../part-3/09-a-turn-without-the-loop.md) |
| A restore rarely and unreproducibly comes back at the wrong step, or with a step's messages from mid-turn | `TakeSnapshot` reads live struct fields with no locking, so a concurrent `Call` tears the read | Take exactly one snapshot, after the turn's `Call` has returned, never mid-turn | [Ch 10](../part-3/10-snapshots.md) |
| `TakeSnapshot` returns an empty map after a turn that finished, so the file you persist has no plan and no `history` | Only a step that returned `*CollectUserInputSignal` stays pending; nothing else gives `TakeSnapshot` an executive to record | Keep the transcript of a conversation that might reopen in your own storage, not the snapshot | [Ch 10](../part-3/10-snapshots.md) |
| After a rename or a redeploy, a returning conversation starts over as if it were new — or `Restore` panics with a nil-pointer dereference | An executive id absent from the snapshot map restores nothing; a plan step's `ref` naming an unknown behavior makes `Restore` call `.Copy()` on a nil `Behavior` | Treat every id and `ref` as schema — as stable as a database column name | [Ch 10](../part-3/10-snapshots.md) |
| `TakeSnapshot` panics mid-persist on a turn where a step paused | A kept `Re-plan` tombstone has a nil `Behavior`, and the skeleton builder calls `p.Behavior.Hash()` on it | Forbid `Re-plan` steps in the `Preamble` of any executive you snapshot, or drop tombstones from the plan before snapshotting | [Ch 14](14-putting-it-together.md) |
| Id expectations in a test pass alone and fail in the suite, or change when a test is added above them | `ZEN_SEED_RNG` is process-wide: seed it in one test and every later `GenerateStringIdentifier` in the binary follows that sequence, so ids depend on test order | Seed in `TestMain` or not at all; better, pin ids with the `*WithId` constructors instead of predicting them | [Ch 13](../part-3/13-testing-agents.md) |

### Signals & pauses

| Symptom | Cause | What to do | Chapter |
|---|---|---|---|
| A tree panics with `unknown Signal type` at the state boundary | Every framework type switch matches pointer types (`*SkipSignal`, `*ErrorSignal`, …); a value signal matches nothing and reaches `TraceForSignal` | Always return `&arboreal.SkipSignal{…}`; add a one-line test per lambda asserting the signal's type | [Ch 6](../part-2/06-signals.md) |
| A plan step's output vanishes from the reply on a turn where nothing paused and nothing errored | A leaked `*SkipSignal` matches no case in `Execute`'s triage switch, so the step is neither summarized nor kept | Treat `*SkipSignal` as `nil` in any code that switches on a tree's returned signal | [Ch 6](../part-2/06-signals.md) |
| After a pause, children resume in the opposite order they were declared | The pause path pushes children in forward order, so they pop — and resume — in reverse priority | Don't rely on insertion order for resume when a node has more than one child | [Ch 6](../part-2/06-signals.md) |
| A nested tree's pause skips its own first half on the turn that resumes it | The outer walk marks the nested tree traversed and moves on; its kept stack is only popped on a later restart, which then resumes past the entry point | Pause only in top-level trees — the ones the executive runs directly as plan steps | [Ch 7](../part-2/07-behavior-trees.md) |
| You can't tell from the outside whether a conversation is waiting on the user | `Call` returns `nil` for every turn that produced a reply, paused or not; only a failed reply comes back as an `*ErrorSignal` | Take a snapshot and inspect it to detect a pause, rather than reading `Call`'s return value | [Ch 9](../part-3/09-a-turn-without-the-loop.md) |

### Planner & trees

| Symptom | Cause | What to do | Chapter |
|---|---|---|---|
| A single `BehaviorTree` with no executive around it never loops | `RunLoop` is a method on `TodoListExecutive`; a bare tree has no loop of its own | Wrap every tree in a `TodoListExecutive`, even a one-tree agent | [Ch 1](../part-1/01-quick-start.md) |
| The process panics mid-turn while planning, not while running a behavior | `Plan` has no `recover`: a non-tree entry in `Behaviors`, malformed model JSON, a missing plan annotation, an invented step name, or a planning-call error all panic | Wrap non-tree behaviors in a one-state tree, keep behavior names distinct, and add `recover` around `RunLoop` — `OutOfBoundsHandler` only catches an empty plan, not a bad name | [Ch 8](../part-2/08-the-executive.md) |
| A behavior's lambda silently never runs, or a snapshot restores one tree with another tree's state | Copying a state struct copies its `HashId`: `Graph.AddNode` merges same-hash nodes inside one tree, and `Snapshot.Restore` keys every behavior by hash across trees | Regenerate `HashId` per instance instead of copying a state struct | [Ch 5](../part-2/05-behaviors-and-states.md) |
| A project that only calls Anthropic models still fails without an OpenAI token | Nothing named a model, so every unset `Model` — the executive's planner, its summarizer, its JSON repair, and any state with empty options — resolves to the built-in `openai:gpt-4o-mini` fallback | Name a model once, on the context: `ctx = arboreal.WithDefaultModel(ctx, llm.ClaudeSonnet4)`. The fallback logs a one-line warning the first time it is used, naming both itself and the call that silences it | [Ch 5](../part-2/05-behaviors-and-states.md) |
| A lambda's captured counter, slice, or map behaves inconsistently across plan steps | `BehaviorTree.Copy()` copies the `Graph` by value but its `Nodes` slice still shares the same `*BehaviorState` pointers, so copies share captured state | Keep captured lambda state read-only, or guard it with a mutex | [Ch 5](../part-2/05-behaviors-and-states.md) |
| Concurrent plan steps corrupt a nested subtree's walk, or a nested executive keeps stale state and never pauses the outer tree | `Copy()` is one level deep — a nested `*BehaviorTree` (including one wrapping a `TodoListExecutive`) is shared by pointer, with its own `State`/`Traversed`/`plan`; the executive's own `Copy()`, reachable from the Lua binding, also nil-derefs an unset `OutOfBoundsHandler` | Keep concurrently-run trees flat or build each step a fresh tree; set `OutOfBoundsHandler` anyway, and never nest an executive that pauses | [Ch 7](../part-2/07-behavior-trees.md) |
| A newly built tree panics the first time it's called, not when it's constructed | `Call` on a tree with no states indexes `Nodes[0]` unconditionally in `Graph.Initial()` | Wire at least one state into every tree before running it | [Ch 7](../part-2/07-behavior-trees.md) |
| One of two identically-named behaviors always runs, or re-planning silently stops working | Behaviors are matched by `Name()` case-sensitively, but `re-plan` is matched case-insensitively, so a behavior named `Re-plan` wins the lookup before the tombstone check | Give every behavior a distinct name, and never name one `Re-plan` (in any case) | [Ch 8](../part-2/08-the-executive.md) |
| The reply you read is stale, or empty | `Output` is a side channel that `Execute` mutates rather than returns, and it is left empty when the reply call failed; `ToolCalls` beside it is the same shape, and still holds what ran before the failure | Read `Output` and `ToolCalls` immediately after `Execute` and check `Execute`'s signal (or use `Call`, which appends the reply with its record for you and returns the failure) | [Ch 8](../part-2/08-the-executive.md) |

### Tools & MCP

| Symptom | Cause | What to do | Chapter |
|---|---|---|---|
| A model's parallel tool calls silently produce only one result | The `AllowTools` loop hardcodes `ToolCalls[0]`; extra calls ride along unexecuted in the appended assistant message | Prompt for one tool call at a time, or expose only tools that make sense that way | [Ch 12](../part-3/12-tools-via-mcp.md) |
| A model "refuses" to use a tool it was told about | `AllowTools: true` with no `WithMCPClient` mux in the context silently offers no tools at all (with `Tools` empty; a named list errors instead) | Check the context for a mux first when a model won't call a tool | [Ch 12](../part-3/12-tools-via-mcp.md) |
| The planner or an annotation-mode state never gets access to tools | Only a state with `AllowTools` set reaches the mux; every other state ignores it entirely | Put tool use only in states that explicitly set `AllowTools` | [Ch 12](../part-3/12-tools-via-mcp.md) |
| Two servers export a tool with the same name and one silently wins | `MCPClientMux` files each tool by name into two maps; a later registration overwrites both entries with no error | Namespace tool names per server; check `Tools()` after every `Add*` | [Ch 12](../part-3/12-tools-via-mcp.md) |
| A state offers the model tools it should never call (the mux is shared, the tree is not) | `AllowTools` offers every tool on the mux | Set `Tools` on the state to the names it needs; anything else is never sent and refused if named | [Ch 12](../part-3/12-tools-via-mcp.md) |
| A finished step's tool calls vanish with it: the snapshot has no step to read and the transcript holds only text | A finished step is dropped when the turn ends; only a paused step's messages reach the snapshot, and a reply's text carries no calls | Read the `tool_calls` annotation on the reply: `Call` and `RunLoop` pin the turn's executed calls onto the reply they append, in plan order, and `ToolCallsFromAnnotation` decodes it before and after a JSON round trip | [Ch 12](../part-3/12-tools-via-mcp.md) |

### Tracing

| Symptom | Cause | What to do | Chapter |
|---|---|---|---|
| The agent deadlocks mid-turn with no output at all | The trace channel is unbuffered and `Send` is a plain send; an attached trace that nobody drains blocks the first `Send` | Drain the trace channel until closed, from a goroutine that outlives every `Call` | [Ch 11](../part-3/11-tracing.md) |
| A state's history edits never show up on the trace | `Operations`'s diff iterates the slice the state was called with, not the one it returned, so appended messages are invisible to it | Read what a state did to the history from the history itself, not from `Operations` | [Ch 11](../part-3/11-tracing.md) |
| A trace silently stops attaching, or collides with unrelated context data | The trace's context key is the raw string `"arboreal_trace"`, with no exported constant or `WithTrace` helper | Spell the key exactly; avoid reusing `"arboreal_trace"` for anything else in the context | [Ch 11](../part-3/11-tracing.md) |

### Templates & annotations

| Symptom | Cause | What to do | Chapter |
|---|---|---|---|
| A type assertion on `Data` that worked before a save now fails after a restore | `Annotation.Data` is `any`; a JSON round trip turns numbers into `float64` and objects into `map[string]any` | Normalize with `fmt.Sprint` or handle the JSON shapes explicitly after any restore | [Ch 4](../part-2/04-messages-and-annotations.md) |
| A loop over "all annotations" trips over framework-internal bookkeeping | The framework reuses the annotation map for its own keys: `__trace_annotations`, `plan`, `raw_history`, `$context`, `tool_calls` | Iterate annotations by the names you own; never range over the whole map | [Ch 4](../part-2/04-messages-and-annotations.md) |
| A "not found" annotation reads back as a non-empty, truthy string | `evalIntoAnnotation` stores the raw JSON text as `Data` when the model answers `{"data": null}` | Treat any `Data` that starts with `{` as a miss | [Ch 4](../part-2/04-messages-and-annotations.md) |
| An extraction block silently renders empty, or truncated at a colon | The block's text is tokenized as Go source; an apostrophe starts a char literal and `//` starts a comment | Keep apostrophes and URLs outside `{{ }}` extraction blocks | [Ch 4](../part-2/04-messages-and-annotations.md) |
| The planner/summarizer prompt renders a stray `�`, or `Plan` panics on a later turn quoting a user message | `AnnotationTemplate.Parse` mishandles both ends: an empty `Preamble` decodes to `utf8.RuneError`; an unclosed `{{` inside quoted `History` text fails to parse | Always set a one-line `Preamble`; escape `{{`/`}}` in user text before it reaches `History` | [Ch 8](../part-2/08-the-executive.md) |
| Writing an annotation onto a hand-built message panics with an assignment to a nil map | Only `AppendToMessages` initializes `Annotations`; a struct literal (or a message unmarshaled from `"Annotations": null`) carries a nil map | Build histories with `AppendToMessages`, or initialize `Annotations` before writing | [Ch 4](../part-2/04-messages-and-annotations.md) |

## Recipes

Six of those rows account for most of the afternoons this book cost. A row is not enough for them; each of the following is the same trap written out with its mechanism, so you can recognize it before it happens rather than after.

### Who owns the history

`History` is a field on `TodoListExecutive`, and exactly one method in the framework maintains it. [Chapter 9](../part-3/09-a-turn-without-the-loop.md)'s method table is four rows long and only the last one says yes: `Plan`, `Execute` and `Call` all read and write the message list they are handed as an argument, and `RunLoop` is the sole caller that names `e.History` — appending what `Receive` returned before the turn and `Output` after it. The field is not a store the framework keeps for you; it is where `RunLoop` happens to keep its own working copy.

The consequence is one sentence: the moment you stop using `RunLoop`, the transcript is yours. `Call(ctx, messages)` takes a list and returns a list, and every conversation across two `Call`s is continuous only because you carried the list from one to the other. That is not a limitation to work around, it is the design — [Chapter 10](../part-3/10-snapshots.md) argues it explicitly under "Owning the transcript" — and the alternative is worse than it looks. The snapshot's `history` key is tempting: set `e.History`, snapshot, and the conversation rides along with the plan in one blob. It works until a turn completes. An executive with nothing pending is absent from its own snapshot, so the day a plan finishes, the transcript disappears with it, however many turns went into building it.

So: keep the transcript in your own storage, appended by your code at exactly the two points `RunLoop` would have appended — the user's message before the turn, the reply after — and treat the snapshot as the disposable half. The bookshelf in [Chapter 14](14-putting-it-together.md) is the reference shape. `loadTranscript` reads a JSON file that nothing in the framework knows about, `main` appends the incoming message, `Call` runs and returns the list with the reply on the end, and `persist` writes that file first, before it does anything that might panic. The snapshot file is written or deleted afterwards, and only ever read back to resume a pending question — never to recover what was said.

There is one bookkeeping line to remember on top of that, and it is easy to skip past: `TakeSnapshot` reads `e.History`, and `Call` never writes it. Assign `exec.History = transcript` before you snapshot, or the file you persist will carry a stale or empty `history` next to a perfectly live `plan`, and nothing anywhere will complain. The bookshelf does it immediately after `Call`, with a comment saying why, so the snapshot's view and the transcript's view agree.

The failure smell is unmistakable once you know it: a restore that comes back with an empty or stale history. Everything works — the plan resumes, the step remembers its own conversation, the reply is coherent — but the transcript you show the user has a hole in it, or ends several turns ago. Nothing errored, because nothing in the framework was ever responsible.

### When a snapshot comes back empty

`TakeSnapshot`'s executive case opens with a guard, `if len(t.plan) == 0`, and a `break` that leaves the switch before anything is recorded: no entry for the executive, its behaviors not even queued for traversal, `History` never read. An executive with nothing pending is absent from its own snapshot, and since it is the only entry point into the map, the map comes back empty.

What keeps a plan in flight is narrow. `Execute`'s triage rebuilds `e.plan` from the steps that returned `*CollectUserInputSignal`, and only those: a tree that drained its walk returned `nil` and is dropped, a `Terminal` came back as `nil` at the tree boundary and is dropped, an error is summarized and dropped. Pausing is the only thing that keeps a step alive into the next turn, so pausing is the only thing that gives `TakeSnapshot` an executive to record. `len(snap) == 0` therefore does not mean *the snapshot failed*; it means *this conversation is not waiting on anybody* — which is the normal end of most turns.

Treat it as a branch, not an error. The bookshelf in [Chapter 14](14-putting-it-together.md) *deletes* its snapshot file when the map is empty, and the deletion is load-bearing rather than tidy: leaving the previous turn's file on disk would make the next invocation restore a task that already finished, resume a step nobody is waiting on, and route the customer's next message into it instead of letting the planner choose afresh. The invariant the program earns is that the snapshot file exists exactly when the agent has asked a question.

Which leaves the transcript, and the answer is the recipe above: the snapshot is a cache of what is pending, so anything that must survive a conversation ending — and most conversations reopen — belongs in your own store. ([Chapter 10](../part-3/10-snapshots.md) has both conditions in full.)

### Where a pause is allowed to live

A pause is the only thing in this framework with a life longer than a turn — the triage above is the whole reason — which makes *where* you put one an architectural decision rather than a detail. The constraints on it are spread over four chapters, because each was found somewhere else.

Put pauses in top-level trees, the ones the executive runs directly as plan steps, and nowhere else. A `PauseState` inside a nested tree does not resume on the turn that answers it: the outer walk has already marked the inner tree traversed and pushed its successors, and the inner tree's kept stack is popped only the next time the outer walk reaches that tree — which then resumes past the pause and skips the tree's first half ([Chapter 7](../part-2/07-behavior-trees.md)). Inside a nested executive it is quieter still: `Call` never returns a pause signal, so the inner executive keeps its plan to itself and the enclosing tree never learns that anything paused ([Chapter 8](../part-2/08-the-executive.md)). A pause also cannot be a tree's first state on an empty history — `BehaviorState.Call` dereferences `LastMessage()` after every lambda, so seed the list with a message first ([Chapter 5](../part-2/05-behaviors-and-states.md)). And on the pause path a node's children are pushed in forward order, so a node with more than one child resumes them in reverse priority ([Chapter 6](../part-2/06-signals.md)).

Then two things the paused turn will not tell you. `Call` returns `nil` whether or not a step paused, so "is this conversation waiting on someone?" is answered by taking a snapshot and checking whether it is empty, never by the return value ([Chapter 9](../part-3/09-a-turn-without-the-loop.md)). And if the plan that pause is kept from also carries a `Re-plan` tombstone, snapshotting that turn panics on a nil `Behavior`, which is why the `Preamble` of any executive you snapshot has to forbid `Re-plan` ([Chapter 14](14-putting-it-together.md)). A plan in which one step pauses and another finishes no longer loses the finished step: both outputs go to the work-in-process summarizer, which folds them into one question ([Chapter 8](../part-2/08-the-executive.md)).

Which reduces to a rule you can hold in your head: exactly one tree may pause, it is a top-level tree, and it always asks a question before it does. `examples/bookshelf`'s `place_hold` is that tree, and the invariant it buys — the snapshot file exists exactly when the agent has asked something — is the reason every other decision in that program has an obvious answer.

### Ids are schema

Every cross-reference inside a snapshot is a hash. The map is keyed by the executive's `Hash()`, each plan step's `ref` is its behavior's hash, and `Restore` resolves those against a freshly built object graph in a process that has never seen the original. The plain factories — `CreateBehaviorTree`, `CreateTodoListExecutive`, the state constructors — draw their ids from `GenerateStringIdentifier`, which reads `crypto/rand`. Those ids are different in every process, which is fine for an agent that lives inside one `RunLoop` and fatal for one that persists anything.

The fix is to pin them, all of them, in one place. `CreateBehaviorTreeWithId` and `CreateTodoListExecutiveWithId` take the hash as an argument; `LLMCompletionOptions` has an `Id` field; and every state exposes `HashId` for plain assignment. One constructor function that builds the whole agent with every id spelled out, called unconditionally at the top of the run before anyone knows whether there is anything to restore, is the shape both `examples/snapshot-edges` and `examples/bookshelf` use.

Get it wrong and you get one of two outcomes, neither of which is an error return. An executive id that no longer matches any key in the snapshot map restores nothing and reports nothing — a silent fresh start, indistinguishable from a first message. A plan step whose `ref` names a behavior the rebuilt graph does not have panics: the lookup yields a nil `Behavior` interface and `Restore` calls `.Copy()` on it. `examples/snapshot-edges` demonstrates both on purpose, as probes 3 and 4.

Which is why the rule is worth stating in the strongest available terms: ids are schema. A snapshot sitting in your store references them by name, exactly as a serialized row references a column, so changing one is a migration and not a rename. If you must predict an id you did not assign — and prefer not to need to — `ZEN_SEED_RNG` seeds the generator, but it is process-wide: set it in one test and every later `GenerateStringIdentifier` call in the binary follows the seeded sequence, so id expectations start depending on test order. `TestMain` or nothing. ([Chapter 10](../part-3/10-snapshots.md) for the round trip, [Chapter 13](../part-3/13-testing-agents.md) for the knob.)

### Signals are pointers

Every type switch in the framework matches pointer types: `*ErrorSignal`, `*SkipSignal`, `*TerminalSignal`, `*CollectUserInputSignal`, in `BehaviorTree.Call`, in `Execute`'s triage, and in `TraceForSignal`. A value satisfies the `Signal` interface — `Description()` has a value receiver on three of the four types — so `return history, SkipSignal{Reason: "…"}` compiles cleanly and matches nothing at runtime.

It does not even fail quietly. `BehaviorState.Call` builds its end-of-call trace message with `Signal: TraceForSignal(s)` on every return, and `TraceForSignal` panics with `unknown Signal type` on anything its switch does not recognize. The argument is evaluated before `Send` is reached, so the nil-safe trace channel is no protection: the panic fires at the state boundary whether or not a tracer is attached. The nastiest case is a value `ErrorSignal`, which also slips past the check that would have reported it as an error, replacing the failure you meant to surface with a panic that names none of it.

The test is one line per lambda, and it is cheap enough that there is no excuse: assert that the returned signal type-asserts to the pointer type you meant. `examples/signals`' predicates in [Chapter 13](../part-3/13-testing-agents.md) are exactly that assertion, written once per outcome and reusable as they stand. Write `&arboreal.SkipSignal{…}` — with the ampersand — everywhere, and let the predicate catch the day you forget. ([Chapter 6](../part-2/06-signals.md) has the full mechanism.)

### One tool call at a time

The `AllowTools` loop in `LLMCompletionState` executes `ToolCalls[0]` and nothing else. A reply that asks for three tools gets one of them run; the other two survive only as unexecuted entries inside the assistant message the loop appends — no result, no error, no trace line, no record. Under the default OpenAI provider this is mostly theoretical, because it still speaks the legacy single-function-call API and can surface only one call per reply anyway, but a provider that does parallel calls hits the limit for real, and silently.

Two neighboring silences compound it. `MCPClientMux` files every tool by name into two maps — name to definition, name to session — so two servers exporting the same tool name collide and the later registration wins both; nothing errors, and the losing server's tool is simply unreachable. And a state with `AllowTools: true`, `Tools` empty, and no mux in the context offers the model no tools at all, which is why "the model refuses to use the tool" is nearly always a context problem rather than a prompt problem. Name your tools uniquely across servers, check `Tools()` after wiring, and check the context before rewriting the prompt.

The design conclusion is the one [Chapter 12](../part-3/12-tools-via-mcp.md) draws: prompt and design for one tool call at a time. Ask for a single lookup per round and let the loop iterate — the state asks, executes, feeds the result back, and asks again, and from outside a state that ran three model rounds returns exactly like one that ran one. A tool surface built around that cadence never meets the limit. `Tools` on the state is how you build it: name the tools that make sense one at a time, and the mux's other tools stay out of the model's sight.

## Patterns

These are the idioms this book's examples converged on. Each has a name so that a review comment can be three words long.

### Stable Ids

**When:** any agent that snapshots — which in practice means any agent that outlives a process. **Shape:** one constructor function that builds the whole object graph, with every id pinned inline and no id left to the random generator.

```go
tree := arboreal.CreateBehaviorTreeWithId(name, description, example, "tree-hold")
state.HashId = "state-hold-ask"
exec := arboreal.CreateTodoListExecutiveWithId(name, description, "exec-bookshelf", &tree)
```

[Chapter 10](../part-3/10-snapshots.md) explains why `Restore` needs them; `examples/bookshelf`'s `buildExecutive` and `buildHoldTree` pin every id in the program and are the shape to copy.

### Caller-Owned Transcript

**When:** anything driven by `Call` rather than `RunLoop`. **Shape:** append the user's message to a list you own, hand it to `Call`, keep what comes back, persist it yourself — and assign it onto `History` so that `TakeSnapshot` records the truth. The snapshot is disposable pending-state beside it, not the record of what was said.

```go
transcript = arboreal.AppendToMessages(transcript, userMessage)
transcript, _ = exec.Call(ctx, transcript)
exec.History = transcript // TakeSnapshot reads it; Call never writes it
```

[Chapter 9](../part-3/09-a-turn-without-the-loop.md) for the method table, [Chapter 10](../part-3/10-snapshots.md) for why the transcript must not live in the snapshot, [Chapter 14](14-putting-it-together.md) for the whole lifecycle around these three lines.

### Annotation → ExtraContext Pipeline

**When:** one state needs to extract a fact that a later state has to see. **Shape:** an annotation-mode state pins the fact onto the message under a name; a later state in the same tree lists that name in `ExtraContext` and gets the value under an `Extra Context:` heading in its system prompt. No schema, no shared dictionary — the annotation on the message is the entire handoff.

```go
extract := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{
	Id: "state-extract-genre", System: extractPrompt, Annotation: "genre",
})
use := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{
	Id: "state-recommend", System: recommendPrompt, ExtraContext: []string{"genre"},
})
tree.AddTransition(&extract, &use)
```

Both states must be in one tree, because the annotation is pinned onto the last user-role message of the list the tree was called with, which on a fresh step is the planner's direction. Which makes the handoff per-turn: when the step completes it is dropped from the plan, and its message list, annotations and all, goes with it. Facts that must outlive the task belong on the transcript you own, not on a step. `examples/bookshelf`'s `extract-genre` → `recommend` pair is this pattern end to end; [Chapter 4](../part-2/04-messages-and-annotations.md) has both halves in detail.

### Thin Model States

**When:** always. You can now say *which* model a state calls — `Model` on the options, `WithDefaultModel` on the context — but you still cannot hand it a fake. `LLMCompletionState`'s lambda resolves that URI and constructs its provider inside itself on every call; there is no provider field, no constructor parameter, and no context key that takes an `llm.ModelProvider`. Choosing a model is configuration; substituting one is not on offer. Nothing can be stubbed, so the leverage is not in swapping the model but in shrinking what touches it.

**Shape:** prompts in states, logic everywhere else — in lambda states and in plain functions over `AnnotatedMessages`, which test like the ordinary Go they are. `learned` in `examples/little-spy` is the reference: it walks a message list, collects the annotations the extractors wrote, normalizes the JSON shapes and skips the misses, and returns a `map[string]string` in microseconds with no model anywhere near it. `examples/bookshelf`'s hold tree is the same idea taken to its end — a canned question, a pause, and a word-scanning lambda for the answer, so the whole pause round trip runs hermetically. What is left untested is then exactly what a stub would have told you nothing about: whether the model exercises good judgment on the prompt you proved it receives. ([Chapter 13](../part-3/13-testing-agents.md).)

### Tiered Planner Models

**When:** an executive whose routing decision deserves a capable model and whose other calls do not. Repairing a stray comma in the planner's JSON is not a reasoning task, and it is billed like one if it runs on whatever the planner ran on.

**Shape:** name the default once on the context, then raise or lower the two calls the executive makes on its own behalf. Three knobs, resolved independently per call.

```go
ctx = arboreal.WithDefaultModel(ctx, llm.ClaudeHaiku35) // behaviors, summarizer
exec.PlannerModel = llm.ClaudeSonnet4                   // routing needs the judgment
exec.RepairModel = llm.ClaudeHaiku35                    // fixing a stray comma does not
```

The third line is the one worth reading twice. `RepairModel` does not fall back to the context default when it is empty — it inherits the *resolved planner model*, so that repair never silently runs on a model you did not name for it. Which means tiering the planner up tiers repair up with it: leave `RepairModel` unset in the example above and the repair call goes to Sonnet, not Haiku. Set it explicitly whenever `PlannerModel` differs from your default, and the saving is real, because repair only fires on malformed planner output — rarely, unpredictably, and always on a request you already paid the planner for.

Each of the three resolves the same way: the explicit value if there is one, then the context default, then a built-in `openai:gpt-4o-mini` fallback that logs a deprecation warning once per process. That fallback is transitional and will be removed, so treat the warning as a build error with a grace period: an agent that never names a model is an agent whose provider was chosen for it.

Note the seam `PlannerModel` does *not* cover. It is the planning call only — `Execute`'s turn summary sets no `Model` of its own and so follows the context default like any behavior state. An executive with `PlannerModel` set and no context default plans on the model you chose and summarizes on the fallback, which is the one arrangement that still needs a credential you did not plan to use. Set the context default first, always; treat the two fields as adjustments to it rather than as the place where model choice lives. ([Chapter 5](../part-2/05-behaviors-and-states.md) for how a `provider:model` URI resolves, [Chapter 8](../part-2/08-the-executive.md) for the two calls the executive makes without being asked.)

### Faithful Directions via Preamble

**When:** any executive whose behaviors read the user's own words — which is most of them, since a behavior never sees the transcript, only the planner's paraphrase of the latest message.

**Shape:** a `Preamble` that pins the shape of a step's `direction`, plus a line in the receiving state's system prompt saying that some user-role messages are third-person notes from a planning system and should be treated as the user's own words. Add the `[]`-for-out-of-bounds instruction, which is the only deliberate route to the `OutOfBoundsHandler`, and — since [Chapter 14](14-putting-it-together.md) — the guard against `Re-plan` for any executive that snapshots.

```go
exec.Preamble = "You are the assistant of a small bookstore. When writing the " +
	"\"direction\" for a step, restate the customer's message faithfully in the " +
	"third person, quoting it, e.g.: The customer said: \"…\". … " +
	"If the message fits none of the behaviors, return an empty JSON array: []. " +
	"Never include a Re-plan step in the plan."
```

Remember what it costs: `interpolatedPreamble` opens the summarizer prompts as well as the planner prompt, so a restate-faithfully instruction shows through as a `The customer said: …` preface in any reply the executive writes itself, which is a merged reply of two or more steps, or the reply after a lone step failed. That is the `Preamble` being the executive's voice rather than a planner setting. ([Chapter 2](../part-1/02-anatomy-of-one-turn.md) for the drift it fixes, [Chapter 8](../part-2/08-the-executive.md) for the three jobs it does.)

### Restore-Seeded Plan

**When:** tests, and any tooling that has to put a plan in flight without invoking the planner — replaying a turn, reproducing a bug report, driving a fixture. `plan` is unexported and `Plan` is a model call, so `Restore` is the only door.

**Shape:** build the map a paused turn would have produced — one executive entry keyed by the executive's pinned id, holding a `plan` array of skeletons with `ref`, `snapshot`, `messages` and `replan_tombstone` — marshal it, unmarshal into a `Snapshot`, and `Restore` it into a freshly built executive. Marshal the `messages` from real `AnnotatedMessages` values rather than hand-typing them, so the field names always match the framework's; only the envelope keys are typed by hand, and `json.Unmarshal` silently zeroes a misspelled one. If the seeded turns can reach the executive's own model calls — the summarizer fires on any turn where two or more steps ran, or the one step failed — open the test with `t.Setenv("OPENAI_TOKEN", "")`: a call that slips in then fails the turn, and `Call` returns the failure for the test to catch, where a machine with a real token would quietly spend one. A seeded step that is meant to call a tool needs no mux: a lambda state that appends the call-and-result pair is recorded on the reply's `tool_calls` like a real one ([Chapter 13](../part-3/13-testing-agents.md)).

`craftSnapshot` in `examples/snapshot-edges` is the canonical version, `craftHoldSnapshot` in `examples/bookshelf` the trimmed one that carries a step and no history at all. ([Chapter 10](../part-3/10-snapshots.md) discovered it, [Chapter 13](../part-3/13-testing-agents.md) made it a technique, [Chapter 14](14-putting-it-together.md) tested a process boundary with it.)

### Always-Drained Trace

**When:** any run you might need to watch. The rule is attach-and-drain or attach nothing: the channel is unbuffered and `Send` is a plain send, so an attached trace nobody reads deadlocks the turn before its first line of output.

**Shape:** make the channel, start the drain goroutine *before* the first `Call`, close the channel after the last one, and wait on a done channel so the drain finishes before the process moves on. Write the narration to stderr and keep stdout for the conversation, so a piped run stays readable and a trace can be turned on without changing what the program outputs.

```go
trace := make(arboreal.Trace)
done := startDrain(trace)                             // running before the first Call
ctx = context.WithValue(ctx, "arboreal_trace", trace) // the key is a raw string
transcript, _ = exec.Call(ctx, transcript)
close(trace)
<-done // the drain finishes before the process moves on
```

`startDrain` in `examples/trace-turn` is the reference version; `examples/bookshelf`'s is the same function with the narration on stderr, closed and joined around a single `Call`. ([Chapter 11](../part-3/11-tracing.md); [Chapter 14](14-putting-it-together.md) reads a real trace line by line.)

## Closing the book

You can now build a behavior out of a lambda or a model call, compose behaviors into a tree and trees into an executive, and drive that executive with the loop or one message at a time. You can pause a conversation mid-task and pick it up in a process that did not exist when it started. You can watch a turn go by on the wire, give one state a tool over MCP without the rest of the program knowing, and write tests for everything except the model's judgment. That is the whole turn path, and nothing on it has been held back. What the book left out sits beside that path rather than on it — the Twilio channel, the Lua binding, the sqlite-vec memory store — and no turn ever runs through it.

What Arboreal is good at is being small and explicit. One file per idea — `annotation.go`, `state.go`, `executive.go`, `snapshot.go`, `trace.go`, `mcp.go` — and no hidden machinery between them: the conversation is the state, so there is no schema to design and no reducer to write; the whole persistence interface is a map you serialize and put wherever you keep things; a pause is a signal that travels up the stack like any other value. Nothing is saved, loaded or retried that you did not write a line for, and the one thing that is decided for you — which behavior a message goes to — is decided by a prompt you can read. When you want to know what happened, the answer is in a message list you can print. The checkpointer you fought elsewhere is, here, a map you serialize yourself — which is the whole trade this framework makes, and the reason the last of the three rules below is a rule at all.

The edges are real, and they are the price of that. The framework panics where it could return an error, keeps two conversations that look alike and are not, hides the plan behind an unexported field, and matches everything by a hash you have to remember to pin. This book documented it as it is at the current commit rather than as it is meant to be, which is why the tables above exist: not as a complaint, but as a map for the day one of those edges finds you — and it will find you by symptom, not by name.

Three things to keep. Keep the model at the edges: prompts in states, logic in plain Go, so most of your agent is testable and the untestable remainder is a thin seam. Keep the ids stable: they are schema, and a snapshot in your store references them by name. And own your transcript: the framework will carry a paused plan across a process boundary for you, but the record of what was said is yours to keep, because it is the only thing that has to survive everything else.
