# Signals

## Why this exists

A `Call` returns two things: the conversation and a *signal*. Chapter 4 was about the first. The conversation is data; every layer reads it and appends to it, and nothing about it says what should run next. The signal is control flow. It is the state's answer to the only question the tree ever asks it: "now that you have run, what do I do?"

That question has no other place to be answered. Arboreal has no conditional edges, no router, and no composite nodes; Chapter 3 called this the first mental shift. The transitions in a `Graph` are plain, static edges, so the signal is the only way a state influences what runs after it. Five values cover everything a state can want: carry on, skip my subtree, stop the tree, wait for the user, fail.

## The five signals

```go
type Signal interface{ Description() string }

type ErrorSignal            struct{ ErrorMessage, ErrorType string } // also a Go error
type SkipSignal             struct{ Reason string }
type TerminalSignal         struct{ Reason string }
type CollectUserInputSignal struct{ Reason string }
// and nil
```

That is all `signal.go` declares, compressed: the real file adds a `Description` method per type (a value receiver on three, a pointer receiver on `TerminalSignal`, which matters below) and the four `ErrorType` constants. `Signal` is a one-method interface, `nil` is its commonest value, and the four structs carry nothing but one or two strings. `ErrorSignal` also has an `Error()` method returning `ErrorMessage`, so a `*ErrorSignal` satisfies Go's `error`; `BehaviorTree.Call` relies on that. `ErrorType` is one of four constants: `StateErrorTypeRetryable`, `StateErrorTypeUnrecoverable`, `StateErrorTypeUnknown` and `StateErrorTypeLuaSyntax`. The framework does not act on the type today: nothing in `BehaviorTree.Call` reads it, and the one place a retry could happen, `Execute` in `executive.go`, has an empty `behaviorsToRetry` slice and a `// TODO: Retry!` comment where the retry would go. Treat `ErrorType` as documentation for your own handlers, and do not expect the framework to react.

## Run it

`examples/signals` needs no token — the first example in this book that does not. It builds a single tree of print-only Go states, tells one state at a time to return a signal, and prints which states ran and what the tree handed back.

The states come from a `recorder`: each appends its own name to a shared slice and returns whatever signal it was constructed with.

```go
{{#include ../../../examples/signals/main.go:recorder}}
```

`build` wires six of them into this tree; states not named in the `signals` map return `nil`.

```text
root → a → a1
         → a2
     → b → b1
```

```go
{{#include ../../../examples/signals/main.go:build}}
```

`exercise` seeds a one-message history, calls the tree the requested number of times threading the history through, and records each call's visit order and returned signal.

```go
{{#include ../../../examples/signals/main.go:exercise}}
```

```go
{{#include ../../../examples/signals/main.go:scenarios}}
```

```
$ go run ./examples/signals
== nil everywhere: depth-first, insertion order
call 1: visited root a a1 a2 b b1  returned nil

== a returns Skip: a's subtree is pruned, its sibling b still runs
call 1: visited root a b b1        returned nil

== a1 returns Terminal: the whole tree stops, the caller sees nil
call 1: visited root a a1          returned nil

== a1 returns Error: the tree aborts and the error propagates
call 1: visited root a a1          returned ErrorSignal(boom)

== a returns CollectUserInput: pause now, resume on the next call
call 1: visited root a             returned CollectUserInputSignal(need input)
call 2: visited a2 a1 b b1         returned nil

== b1 (the last state) returns Skip: the Skip leaks out of the tree
call 1: visited root a a1 a2 b b1  returned SkipSignal
```

Read each scenario against the tree diagram above. The first line is the baseline: with no signals, the walk is depth-first in insertion order. Every other scenario changes exactly one state's return value, and the difference from the baseline is that signal's effect. Two lines deserve a second look: in the `Terminal` scenario the tree stopped after `a1` yet the caller saw `nil`, and in the `CollectUserInput` scenario the second call did not start from `root` and ran `a2` before `a1`. The next section explains the mechanism behind each line.

## How it works

`BehaviorTree.Call` in `behavior.go` is a loop over an explicit stack, `State`, kept on the struct: pop a state, skip it if already in `Traversed`, otherwise mark it and call it, and react to the signal it returned. The reaction has two stages. First, errors are checked with a pointer type assertion, `sig.(*ErrorSignal)`, and return immediately; that check sits before the switch, so `*ErrorSignal` is not a case of it. Then a type switch handles the other three named signals, and `nil`, which matches no case, falls through to the code after the switch: push the state's unvisited children. The table summarizes what each signal does to the stack, to the children, and to what the tree's caller eventually receives.

| Signal | Stack | Children pushed? | Reaches the tree's caller? | Caller receives |
|---|---|---|---|---|
| `nil` | continues | yes, in reverse (first-added runs first) | — | `nil`, when the walk drains |
| `SkipSignal` | continues | **no** | only if it was the last state visited | a leaked `*SkipSignal` |
| `TerminalSignal` | wiped, `Traversed` reset | no | **no** — absorbed | `nil` |
| `CollectUserInputSignal` | kept, with children pushed | yes, in **forward** order | **yes**, unchanged | `*CollectUserInputSignal` |
| `ErrorSignal` | wiped | no | **yes** | `*ErrorSignal` (also an `error`) |

Row by row. On `nil` the children are pushed last-to-first, so the first child added is on top and popped first; that is why insertion order is priority, and why the baseline reads `root a a1 a2 b b1`. On `SkipSignal` the case body is a bare `continue`: nothing is pushed, so everything reachable only through the skipped state is pruned, but the state's siblings, already on the stack, still run; in the second scenario `a1` and `a2` vanish and `b` and `b1` do not. On `TerminalSignal` the stack is emptied, `Traversed` is set to `nil`, and the loop exits through the `done` label. On `CollectUserInputSignal` the unvisited children are pushed and the loop exits through the same label with the stack left as it is, which is what makes the next `Call` resume rather than restart: the entry code pushes `Graph.Initial()` only when the stack is empty. On `ErrorSignal` the stack is emptied and the function returns at once, before the switch, with the `*ErrorSignal` as the tree's signal.

The two exits through `done` have opposite propagation policies, and the difference is deliberate. Before jumping, the `TerminalSignal` case sets `sig = nil`, so the caller sees a clean completion; that is the third scenario's `returned nil`. `Terminal` means "this tree is done", and trees nest: to a parent tree that holds this one as a state, a finished child is an ordinary `nil`, and the parent carries on with its own stack. If `Terminal` propagated, one leaf could kill every enclosing tree. `CollectUserInputSignal` is the opposite case. It must reach the executive, the one thing that can stop and wait for the user, so it passes through every enclosing tree untouched: each matches the same case, pushes the *nested tree's* successors, keeps its own stack, and returns the same pointer upward. Note what that means: the parent has already marked the nested tree as traversed, so the next call resumes with the nested tree's successors, not inside the nested tree; its kept stack waits for a later restart (Chapter 7 covers nesting and what that resume skips). `ErrorSignal` propagates for the same reason: nothing between the failing state and the executive knows how to recover, so nothing absorbs it.

What the executive does with each signal is in `Execute`. After `executePlan` returns one `PlanResult` per step, `Execute` switches on each result's signal. `nil` means the step finished, and its last message becomes one of the summaries that feed the final summarizing call. `*CollectUserInputSignal` means the step is waiting, so the step is kept in the new `plan` for next turn and its last message joins the summaries as well; Chapter 2, step 5, showed the rule that makes a lone step's last message the reply verbatim, whether it finished or paused. `*ErrorSignal` adds an `Error occurred: ` line, built from the error's `Error()` text, to the summaries, and the step is not kept. That is the entire list of cases; the second sharp edge below is about what it leaves out.

### Expressing control flow with signals

Without composites, the patterns you know from behavior trees, and the routers you know from LangGraph, become conventions about which signal a state returns. Three cover most of what you will write.

**Sequence.** A chain `a → b → c`, wired with two `AddTransition` calls, where each state does its work and returns `nil`. Because `nil` pushes the next state and `ErrorSignal` wipes the stack, any state failing aborts the rest of the chain and reports the failure to the caller:

```go
Lambda: func(ctx context.Context, history arboreal.AnnotatedMessages) (arboreal.AnnotatedMessages, arboreal.Signal) {
    if err := doStep(history); err != nil {
        return history, &arboreal.ErrorSignal{ErrorMessage: err.Error(), ErrorType: arboreal.StateErrorTypeRetryable}
    }
    return history, nil
},
```

**Selector.** Siblings under one parent, tried in insertion order. Each returns `SkipSignal` when it is not applicable, so the walk moves to the next sibling already on the stack, and `TerminalSignal` when it has handled the case, so nothing after it runs:

```go
Lambda: func(ctx context.Context, history arboreal.AnnotatedMessages) (arboreal.AnnotatedMessages, arboreal.Signal) {
    if !applies(history) {
        return history, &arboreal.SkipSignal{Reason: "not this branch"}
    }
    return handle(history), &arboreal.TerminalSignal{Reason: "handled"}
},
```

The caveat is in the table: `Terminal` clears the whole tree's stack, not the selector's subtree. In `preprocess → selector → postprocess`, `postprocess` never runs once a branch succeeds. So this pattern is clean only when the selector is the whole tree. There is one in-tree alternative: if the branches are mutually exclusive by their guards, have the applicable branch return `nil` instead of `Terminal` and wire `postprocess` as the lowest-priority sibling of the branches, added after them under the same parent. Insertion order makes it run after whichever branch applied. Do not wire it as a shared child of every branch instead: it would then run under the first applicable branch, before the later guards, and whenever an earlier branch applies, the last guard's Skip leaks. Chapter 7 expands on the other workarounds, which come down to writing the selection imperatively inside one state or lifting the alternatives into separate trees for the executive to choose among.

**Branch on an annotation.** A Go state guards its own branch by reading a value an earlier state extracted (Chapter 4) and pruning itself when the value does not match. Put one such guard at the top of each branch and the annotation decides which branch runs:

```go
Lambda: func(ctx context.Context, history arboreal.AnnotatedMessages) (arboreal.AnnotatedMessages, arboreal.Signal) {
    a := history.GetAnnotation("intent")
    if a == nil || fmt.Sprint(a.Data) != "refund" {
        return history, &arboreal.SkipSignal{Reason: "intent is not refund"}
    }
    return history, nil
},
```

In all three, note where the decision sits: in the state that returns the signal, not in an edge above it.

## Coming from LangGraph

A router registered with `add_conditional_edges` reads the state and returns the name of the next node, choosing among destinations you listed in the mapping; `Command(goto=…)` lets a node do the same for itself. A signal can do none of that. It can prune (skip my subtree), stop, pause, or fail; it never names a destination, and the edges out of a state are whatever `AddTransition` wired. To choose between destinations, wire all of them as children of the deciding state and have each child skip itself when it does not apply, as in the annotation pattern above. The decision moves from the edge into the destination: each branch tests the condition that would have lived in the router. The two ends of the mapping are recognizable, and `CollectUserInputSignal` is close to `interrupt()`, but the mapping breaks at "go to": there is no signal, and no other mechanism inside a tree, by which a state can send the walk to a node that is not already its child.

## Sharp edges

```admonish warning title="Sharp edge"
Signals must be returned as **pointers**. Every type switch in the framework — `BehaviorTree.Call`, `Execute`, and `TraceForSignal` in `trace.go` — matches `*ErrorSignal`, `*SkipSignal`, `*TerminalSignal`, `*CollectUserInputSignal`. A value such as `SkipSignal{Reason: "…"}` matches nothing. It never even reaches the tree's switch: `BehaviorState.Call` passes every returned signal through `TraceForSignal`, which panics with `unknown Signal type` at the state boundary (and a value `TerminalSignal{}` does not compile as a `Signal` at all, because only its pointer type has `Description`). `examples/crm` returns value signals and is wrong; write `&arboreal.SkipSignal{…}`.
```

The panic fires whether or not a tracer is installed: `BehaviorState.Call` in `state.go` builds its `CallEnd` trace message with `Signal: TraceForSignal(s)` on every return, and `Trace.Send` being nil-safe does not help, because the argument is evaluated before the call. A value `ErrorSignal{…}` is the worst case: it also slips past the `sig.(*ErrorSignal)` check that would have recorded it as an error, so the failure you meant to report is replaced by a panic that names none of it.

```admonish warning title="Sharp edge"
A `SkipSignal` from the last state visited leaks out as the tree's return value (last scenario above). Callers that switch on the tree's signal should treat `*SkipSignal` as `nil`. The executive does not: `Execute`'s triage switch has cases only for `*ErrorSignal`, `*CollectUserInputSignal` and `nil`, so a plan step whose behavior returns a leaked `*SkipSignal` is neither summarized nor kept — its output silently vanishes from the reply.
```

The leak has a narrow cause. The `SkipSignal` case is `continue`, which does not reset `sig`, and `sig` is normally overwritten by the next `state.Call`. If the stack drains right after the skipped state there is no next call, the loop ends, and the stale `*SkipSignal` is what the function returns. A skip anywhere else is invisible to the caller, as the second scenario shows: `a` skipped, `b1` ran last, and the tree returned `nil`. What triggers it is a guard that is the last state the walk visits, which is exactly where the annotation-branch pattern can put one. The fix is to make sure the walk cannot end on a guard: wire one more child under the same parent, after the guards, that returns `nil` (a no-op `BehaviorState`, or the state that would have been `postprocess`). Insertion order makes it run last, and its `nil` overwrites the stale Skip. A nested tree needs the same care, because a parent tree is a caller that does not treat the leak as `nil` either: it matches `*SkipSignal` and prunes the nested tree's successors.

```admonish warning title="Sharp edge"
On the pause path children are pushed in forward order, so they resume in **reverse** priority (`a2` before `a1` above). With one child — the common case — this is invisible; with several, do not rely on insertion order after a pause.
```

Trace it through the fifth scenario. `root` returns `nil` and pushes `b` then `a`, so `a` is on top. `a` pauses and pushes `a1` then `a2`, in forward order, so `a2` is on top when the walk stops. The second call pops `a2`, then `a1`, then `b`, which pushes `b1`, and `Traversed` still holds `root` and `a`, so neither runs again. That is `a2 a1 b b1`.

## Back to the trace

Step 4 of Chapter 2 is the `CollectUserInputSignal` row of the table. `pauseState` returns a `*CollectUserInputSignal`; `BehaviorTree.Call` matches it in the switch, pushes the pause state's unvisited children, of which there are none, jumps to `done`, and returns the messages and the same signal to `executePlan`. The tree's stack is left as it is, which in the quickstart means empty.

Step 5 is `Execute`'s triage. The `PlanResult` carries the `*CollectUserInputSignal`, `Execute` matches that case, and the step is kept in `plan` with its last message added to the summaries; it is the only step that ran, so that message is the reply as written.

Steps 4 and 7 together are the `CollectUserInputSignal` row's Stack cell, `kept`. The second call in step 7 runs on the same tree copy and resumes from whatever the pause left on its stack. Because `pauseState` is a leaf, that stack is empty and the resume is a restart from `Graph.Initial()`, which is why the quickstart re-runs `chatState` every turn. Had `pauseState` had a child, the second turn would have started there instead, in the order the fifth scenario shows.

```admonish example title="Recap"
- Five signals: `nil`, `Skip`, `Terminal`, `CollectUserInput`, `Error`.
- `Terminal` is absorbed; `CollectUserInput` and `Error` propagate; `Skip` prunes and can leak.
- Control flow is expressed by the state that returns the signal, not by the edge.
- Always return pointers.
```
