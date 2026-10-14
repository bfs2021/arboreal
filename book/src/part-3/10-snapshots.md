# Snapshots

## Why this exists

Chapter 9 ended with two `Call`s in one process and a warning: everything that made the second turn work — the paused step, its growing message list, its tree copy — sits in the unexported `plan` field of an executive in memory. A deployment that handles one message per invocation puts a process exit between those turns, and the next message may land in a process where that executive has never existed. For the conversation to survive the gap, whatever the executive knows must become data at the end of one turn and become an executive again at the start of the next. `TakeSnapshot` and `Snapshot.Restore`, both in `snapshot.go`, are that round trip. A snapshot is the executive's in-flight state — the transcript it was told about and the steps still waiting on the user — and deliberately nothing more: no wiring, no prompts, no code. The next process rebuilds those by running the same constructor; the snapshot supplies only what that constructor cannot know.

## What is in one

`TakeSnapshot(root)` returns a `Snapshot`, which is `map[string]snapshotvalue`: behavior hash to that behavior's saved state. That is all it returns. The framework never touches disk; you marshal the map and put the bytes wherever your process keeps things — a file, a database row, a queue message — your code. `examples/snapshot-simple` uses `json.MarshalIndent` and `os.WriteFile`, and this is its `snapshot.json`, abridged, after the two runs the Run it section below walks through (long strings elided with `…`; every key and shape is from the actual file):

```json
{
  "exec-snapshot-chat": {
    "history": [
      { "role": "user", "content": "Hey, my name is John and I am a pirate", "Annotations": {} },
      { "role": "assistant", "content": "Hi John! It's great to meet you. …", "Annotations": {} },
      { "role": "user", "content": "What is my name?", "Annotations": {} },
      { "role": "assistant", "content": "Your name is John!", "Annotations": {} }
    ],
    "plan": [
      {
        "ref": "tree-chat",
        "snapshot": {},
        "messages": [
          { "role": "system", "content": "You are a friendly assistant …", "Annotations": {} },
          {
            "role": "user",
            "content": "The user said: 'Hey, my name is John and I am a pirate.'",
            "Annotations": {
              "$context": { "name": "", "data": null, "explanation": "" },
              "raw_history": {
                "name": "",
                "data": [ { "Annotations": {}, "content": "Hey, my name is John and I am a pirate", "role": "user" } ],
                "explanation": ""
              }
            }
          },
          { "role": "assistant", "content": "Hi John! It's great to meet you. …", "Annotations": null },
          { "role": "user", "content": "What is my name?", "Annotations": {} },
          { "role": "assistant", "content": "Your name is John!", "Annotations": null }
        ],
        "replan_tombstone": false
      }
    ]
  }
}
```

One top-level key: `exec-snapshot-chat`, the executive's `Hash()`. Under it, the two things the executive carries between turns. `history` is `e.History`, the transcript — four messages after two turns. `plan` is the pending steps, one skeleton per step (`ExecGeneratedStepSkeleton` in `snapshot.go`), and each skeleton has four fields.

`ref` is the step's behavior by hash — `tree-chat`, the id `buildExecutive` pinned on the tree — because a snapshot stores no code, only a name to look the code up by. `snapshot` is that behavior's own entry, recursively: a nested `Snapshot` holding, for a tree, its `state` stack and `traversed` set. Here it is empty; the Restore section says why. `messages` is the step's private conversation, Chapter 2's second conversation frozen mid-sentence: the system prompt `LLMCompletionState` prepended for `chatState`, the planner's direction with its `raw_history` and `$context` annotations riding along, the model's replies, and the user messages the resume branch appended. `replan_tombstone` marks Chapter 8's reserved `Re-plan` step.

Both of Chapter 2's conversations are on this one page, side by side, which makes a snapshot the best debugging artifact the framework produces: when a reply looks wrong, the step's `messages` here are what the model actually saw. (One shape to notice for later: the `raw_history` annotation's `data` has alphabetized keys because this file has already been restored and re-marshaled once — it has been through `any`, and came back as `map[string]any`. The last sharp edge returns to this.)

## The two conditions

Two conditions decide what a snapshot contains, and both live in plain sight in `TakeSnapshot`. The first is the guard at the top of its `*TodoListExecutive` case:

```go
// If there is no plan, no need to snapshot
if len(t.plan) == 0 {
	break
}
```

The `break` leaves the `switch` before anything is recorded: no entry for the executive, its behaviors not even pushed for traversal, and `History` never read. An executive with nothing pending is absent from its own snapshot entirely. And what keeps a plan pending is narrow: Chapter 8's triage rebuilds `e.plan` in `Execute` from the steps that returned `*CollectUserInputSignal`, and only those. A tree that drained its walk returned `nil` and is dropped; a `Terminal` came back as `nil` at the tree boundary and is dropped; an error is summarized and dropped. Pausing is the only thing that keeps a step alive into the next turn, so pausing is the only thing that gives `TakeSnapshot` an executive to record. "The conversation finished" therefore means an empty map — `len(snap) == 0` — and that is a normal outcome, not a failure: persist it, and the next process restores nothing and plans afresh, which is exactly right for a finished conversation.

The second condition is the line that fills the entry: `val := snapshotvalue{History: t.History}`. The field read is `e.History` — the field Chapter 9's table showed no method on the turn path writes. `Call` runs the whole turn without naming it; only `RunLoop` maintains it, and without the loop that caller is you. `examples/snapshot-simple` appends the user's message to `exec.History`, calls `Call` on it, and assigns the returned list back, so by snapshot time the field is the real transcript. Skip that bookkeeping and `TakeSnapshot` records whatever stale or empty value the field holds, next to a perfectly live `plan`. The `history` key is only as truthful as your assignments.

## Stable identity

Every cross-reference in a snapshot is a hash. The map is keyed by `Hash()`; each skeleton's `ref` is `p.Behavior.Hash()` — and a step's behavior is a `Copy()`, which keeps the prototype's hash (Chapter 7). `Restore` resolves those hashes against a freshly built executive in a new process, so the whole scheme works only if every behavior gets the same hash on every run. The plain factories draw random ids (Chapter 5), which differ per process; restorable agents pin them. The tools are the `…WithId` constructors — `CreateBehaviorTreeWithId`, `CreateTodoListExecutiveWithId` — the `Id` option on `LLMCompletionState`, and plain assignment to the exported `HashId` field on any state, which, Chapter 5 noted, is how this chapter's example names its `PauseState`.

The pattern this produces is one constructor function that builds the same executive, with the same ids, on every run — `buildExecutive` in `examples/snapshot-simple`, included below, which pins `tree-chat`, `state-chat`, `state-pause` and `exec-snapshot-chat` and is called unconditionally at the top of `main`, before anyone knows whether there is a snapshot to restore. Ids are schema: the snapshot in your store references them, so changing one is a migration, not a rename.

## Restore, and what "resume" really means

`Restore(root)` in `snapshot.go` makes two passes. The first walks the behavior graph from `root` and builds `lookupMap`, hash to `Behavior`, noting which behaviors have entries in the map. The second rehydrates them: a tree entry gets its `Traversed` set back and its `State` stack rebuilt by looking each saved ref up in `lookupMap`; an executive entry gets `History` assigned back, and each skeleton in `plan` rebuilt as a live step — the behavior `p.Ref` names in `lookupMap`, copied (an absent ref, or one that is not a `Step`, is an error instead), the skeleton's nested `Snapshot` restored *into that copy*, and the skeleton's `messages` and `replan_tombstone` carried over into an `ExecGeneratedStep`. After that, `len(e.plan)` is nonzero, and the next `Call` takes Chapter 9's resume branch as if the process had never exited.

Now the empty `"snapshot": {}` from the JSON walk. `pauseState` in the example is a leaf: when it paused, it had no children to push, so the tree's `State` stack was empty — and `TakeSnapshot`'s tree case records an entry only inside `if !t.State.IsEmpty()`. A tree paused at a leaf records no cursor at all. Restored, the step holds a fresh copy with an empty stack, and on the next `Call` the walk restarts from `Graph.Initial()`: `chatState` runs again, on a message list that now ends with the user's new message. Nothing was lost, because there was nothing to lose — this is Chapter 7's restart-vs-resume rule, unchanged by the process exit. A tree that drained its stack, or paused at the end of a chain, restarts from the entry; continuity lives in the step's `Messages`, not in the tree's position, and the second run's `Your name is John!` came from exactly that: a restarted walk over a remembered conversation.

A tree paused mid-walk is the other half of the rule. Pause with children pushed, or with pending siblings on the stack, and `State` is nonempty at snapshot time: the tree gets its own entry — `state`, the refs still to visit, and `traversed`, the hashes already run — and the restored copy resumes exactly where it stood, honoring the visited set. Chapter 6's pause scenario in `examples/signals` is this case in miniature: `a` pauses with `a2`, `a1` and `b` stacked, and the next call runs `a2 a1 b b1` without revisiting `root` or `a`. Put a snapshot and a new process between those two calls and the order is the same. So "resume" after a restore means precisely what it meant without one: the next turn continues from whatever the pause left on the stack, and the position of the `PauseState` — leaf or mid-chain — decides whether that is a restart or a continuation.

## Owning the transcript

The `history` key suggests an easy division of labor: let the snapshot carry the transcript. Set `e.History` before `Call`, snapshot after, and the conversation you would show the user rides along with the plan — one blob, one write, and `examples/snapshot-simple` does exactly this, because it is the smallest thing that works. It works for as long as every turn ends in a pause. The day a plan completes — a `Terminal`, a drained walk, a planner that returns `[]` — the executive vanishes from its own snapshot and the transcript vanishes with it, however many turns it took to build.

The alternative is to keep the transcript in your own store, appended by your code at the same points `RunLoop` would have appended to `History`, and to treat the snapshot as disposable in-flight state: a thing you write after a turn that paused, delete or ignore after a turn that finished, and can afford to lose. That survives completion. It survives a restore that finds nothing because an id changed. It lets you prune or compact old messages without editing a blob that also holds a live plan. And it means a finished conversation can be reopened with context — build the executive, set `e.History` from your store, `Call` — even though no snapshot exists. Prefer this one. The snapshot is a cache of *what is pending*, dropped by design the moment nothing is; the system of record for what was said has to be a thing that does not evaporate when the agent finishes its job.

## Run it

`examples/snapshot-simple` is the per-message shape the rest of Part III builds toward, readable top to bottom in `main`: build the executive (same ids every run), restore the snapshot if one exists, append the user's message to `History`, `Call`, persist a fresh snapshot. There is deliberately no loop — to continue the conversation, run the program again:

```go
{{#include ../../../examples/snapshot-simple/main.go}}
```

Run it twice with `go run ./examples/snapshot-simple`; it needs `OPENAI_TOKEN`, and the snapshot lands in `snapshot.json` in the working directory. Tell the first run `Hey, my name is John and I am a pirate`; ask the second `What is my name?`. The first prints `(no snapshot found, starting a fresh conversation)` and greets John; the second prints `(restored previous conversation from snapshot.json)` and answers with the name — knowledge that reached it only through the file, since the process that learned it has exited. The `Preamble` and the system prompt on `chatState` are doing quiet work here: Chapter 2 traced how a planner paraphrase can drop the name before it ever reaches a model, so the example pins the direction format and tells the state how to read directions — worth stealing along with the skeleton.

The edges are in `examples/snapshot-edges`, which is hermetic — a canned reply and a pause, no model, no token — and runs with `go run ./examples/snapshot-edges`. Its executive is built by a constructor with pinned ids, parameterized only by the executive's own id so that probe 3 can get it wrong on purpose:

```go
{{#include ../../../examples/snapshot-edges/main.go:build}}
```

Its snapshot is not taken but crafted — the JSON a paused turn would have produced, built by hand and unmarshaled:

```go
{{#include ../../../examples/snapshot-edges/main.go:craft}}
```

```go
{{#include ../../../examples/snapshot-edges/main.go:probes}}
```

The output, verbatim and the same on every run:

```text
1. fresh executive: entries=0 err=<nil>
2. after Call: reply="Hello! I am a canned reply."
   snapshot: entries=1 err=<nil>
3. wrong exec id: restore err=<nil>, entries after=0
4. unknown step ref: err=arboreal: plan step "tree-gone" refers to no behavior of this executive
```

Probe 1 is the first condition made visible: no plan, no entry, and an empty map with a `nil` error is the normal shape of "nothing pending". Probe 2 seeds a plan through `Restore` and runs a whole turn with no model anywhere — seeding via `Restore` is the *only* way to put a plan in flight from outside the package without invoking the LLM planner, since `plan` is unexported; Chapter 13 turns that into a testing technique, and the example's own `TestSnapshotRoundTrip` in `main_test.go` is the first use. Probe 3 restores under a mismatched executive id: no error, no entry, no state — a silent fresh start. Probe 4 restores a step whose `ref` names no behavior, and `Restore` returns an error naming the ref and installs nothing. Both of those last two reappear as sharp edges below.

## Coming from LangGraph

In LangGraph, persistence is a checkpointer plus a `thread_id`: configure the store once, pass the id on every `invoke`, and the framework saves state after every super-step and loads it before the next, implicitly. Arboreal has no store and no thread registry; the whole interface is the map — build the executive, `Restore` the bytes you kept, `Call`, `TakeSnapshot`, keep the new bytes. What the `thread_id` did — saying which conversation a message belongs to — is done by which snapshot you load, and where snapshots live is your code, as is the transcript (Chapter 9 made that split: you carry the conversation you would show the user, the snapshot carries the paused plan). And where LangGraph checkpoints on every super-step whether you asked or not, Arboreal snapshots only when you call `TakeSnapshot`, which is between turns or not at all: there is no mid-turn state to capture, because a turn that has not returned is still mutating the fields the snapshot would read.

## Sharp edges

```admonish warning title="Sharp edge"
A snapshot is coherent only between turns. `TakeSnapshot` reads live struct fields with no locking; taking one while `Call` runs on another goroutine hands you a torn state. One message, one turn, one snapshot at the end.
```

```admonish warning title="Sharp edge"
An executive with nothing pending is absent from its own snapshot — `history` included. If a finished conversation can be reopened, the transcript must live somewhere of yours; the snapshot will not carry it across the gap.
```

```admonish warning title="Sharp edge"
`Restore` has two failure modes and only one is an error return. An executive id with no snapshot entry restores nothing and reports nothing — a silent fresh start. A plan step whose `ref` matches no behavior, or names something that is not a tree, makes `Restore` return an error naming the `ref` (probe 4), and the executive is left as it was: the plan and the history are installed only once every step has restored. Treat a restore after any id change as suspect, and keep ids as stable as a database schema.
```

```admonish warning title="Sharp edge"
`Annotation.Data` is `any` and a snapshot is JSON: numbers come back `float64`, objects `map[string]any` (Chapter 4). Every annotation your states type-assert must tolerate the JSON shapes after a restore. The executive's own `tool_calls` record has no key of its own in a snapshot: it rides the reply on the transcript, so it is in `history` exactly when that reply is (Chapter 9), and after a JSON round trip — the snapshot's or your store's — it reads back through `ToolCallsFromAnnotation`, which takes the typed slice and the JSON shape alike, so do not assert on its `Data` by hand.
```

Because a snapshot is plain data, you can also edit it. Delete the last exchange from `history` and from a step's `messages` and you have rewound the conversation by a turn; drop an annotation and the system un-learns a fact; rewrite a direction and the next resume runs under different orders. This is a powerful debugging tool — replaying a turn from a doctored snapshot is often the fastest way to isolate a misbehaving step — but nothing validates what you write back, so you own the consequences.

## Back to the trace

The snapshot is Chapter 2's trace frozen at its quietest moment: after step 6 has sent the reply, before step 7's message arrives. `history` is the transcript as of the end of the last turn — the user message appended before planning, the `Output` appended after. Each skeleton's `messages` is a step's private list exactly as step 3's `executePlan` wrote it back after step 4's walk stopped at the pause, and `ref` names the tree copy step 2 made. `Restore` rebuilds precisely the world step 7 expects to find on entry: `len(e.plan)` nonzero, so the plan-or-resume branch skips `Plan`, routes the new message into the pending step, and executes — the same turn, in a process that was not alive when the conversation started.

```admonish example title="Recap"
- A snapshot is a map you serialize: executive entry (`history` + step skeletons), each step's `messages`, nested behavior entries.
- Recorded only while a plan is in flight; only pauses keep steps alive; empty is normal.
- `history` is only what you put in `e.History`; the durable transcript should be yours.
- Restore matches by hash: same construction, same ids, every run.
- A leaf-pause tree re-runs from its entry; the step's `messages` carry the continuity.
```
