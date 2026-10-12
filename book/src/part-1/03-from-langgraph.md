# If you come from LangGraph

Arboreal and LangGraph share a vocabulary: graph, node, edge, interrupt, checkpoint. The overlap is close enough to mislead. The words name something real on both sides, but the machine underneath differs, and if you carry the LangGraph meaning across unexamined you will look for conditional edges that do not exist and miss a planning step that does. This chapter is a translation table plus the three places where the translation breaks. Each row is expanded in the chapter named in its last column.

## Rosetta stone

| LangGraph | Arboreal | Notes | Chapter |
|---|---|---|---|
| `StateGraph` | `BehaviorTree` | A directed graph, not strictly a tree; a node may have several parents | 7 |
| node function `(state) -> dict` | `BehaviorState.Lambda`: `func(ctx, history) (history, Signal)` | Takes and returns the whole conversation | 5 |
| typed `State` (`TypedDict`, reducers) | `AnnotatedMessages` + named annotations | The message list *is* the state; annotations are typed slots attached to messages | 4 |
| `add_edge(a, b)` | `tree.AddTransition(&a, &b)` | Also adds the nodes; the first node added is the entry point (`START`) | 7 |
| `add_conditional_edges` | *none* — edges are static; a node returns `SkipSignal` to prune its own subtree or `TerminalSignal` to stop the tree | Control flow lives in the leaf | 6, 7 |
| `END` | the walk running out of states, or `TerminalSignal` | | 7 |
| `interrupt()` / `Command(resume=…)` | `PauseState` → `CollectUserInputSignal`; the next `Execute` resumes | The tree keeps its own stack between calls | 6, 7, 8 |
| checkpointer + `thread_id` | `TakeSnapshot` / `Snapshot.Restore` | Requires stable behavior ids; coherent only between turns | Part III |
| `@tool`, `ToolNode` | MCP tools via `MCPClientMux` and `AllowTools: true` | No Go-defined tools; you run an MCP server | Part III |
| `create_react_agent` | `TodoListExecutive` | Plan-and-execute with concurrent steps, **not** a ReAct loop | 8 |
| `Send` (map-reduce fan-out) | the executive's plan: one goroutine per step, results summarized | Fan-out is decided by the planning LLM, not by your code | 8 |
| `Command(goto=…)` | *none* | | — |
| `astream` / `stream_mode` | *none* — completions are fully buffered | | — |
| `graph.invoke(...)` in a loop | `exec.RunLoop(ctx, channel)` | The loop is part of the framework and talks to a `Channel` | 1, 8 |

## Three mental shifts

### 1. Control flow lives in the leaf

In LangGraph routing usually belongs to the edge. A node returns a state update, and where the graph goes next is decided by a static `add_edge` or by the router you attached with `add_conditional_edges`, which reads the state and names the next node; that separation is what lets one node be reused under different routers unchanged. A node can also route itself, by returning `Command(goto=…)` and naming its successor. Either way, something names a destination.

Arboreal has neither conditional edges nor composite nodes: no Sequence, Selector or Parallel, and no router. `Graph` in `structs.go` holds plain transitions, and `BehaviorTree.Call` in `behavior.go` follows them with a stack. A state instead decides its *own* control-flow effect and returns it as a `Signal` alongside the messages, and `BehaviorTree.Call` has one fixed reaction per signal: `nil` pushes the state's unvisited children and carries on; `SkipSignal` pushes nothing, pruning whatever is reachable only through it (a node with a second parent can still be reached that way) while the walk continues with what is already on the stack; `TerminalSignal` empties the stack and ends the walk; `CollectUserInputSignal` pushes the children and stops without clearing the stack; `ErrorSignal` empties the stack and returns the error. The mapping breaks here: an Arboreal state can never name a destination at all. It can say whether its own subtree runs (`SkipSignal`) or whether the tree continues (`TerminalSignal`); the wiring is fixed. That is why `Command(goto=…)` has no row on the right.

Two consequences follow. A state that skips or terminates is bound to the context it was written for, and is less reusable than a node whose routing stays outside it. And a "selector" is a shape, not a node type: siblings under one parent, each returning `SkipSignal` when it does not apply, the branch that applies ending in `TerminalSignal` if the rest must not run. Chapter 6 covers the signals; Chapter 7 the walk.

### 2. The agent layer plans, then executes, concurrently

`create_react_agent` is a loop: a model node proposes a tool call, a `ToolNode` runs it, and a conditional edge returns to the model until it answers without one. Thinking and acting alternate one round at a time (a round may carry several tool calls, which `ToolNode` runs together), and the number of rounds is decided as it goes.

`TodoListExecutive` in `executive.go` is not that loop. `Plan` makes one model call that returns the whole todo list as a JSON array of steps, each a behavior name plus a free-text direction. `executePlan` then starts a goroutine for every step at once, each running its own `Copy()` of the chosen behavior on its own message list, whose first message is the direction rather than the user's words. `Execute` folds the results into one reply: when exactly one step ran and it finished or paused, that step's last message as written; otherwise a summarizing call (Chapter 2, step 5). While any step is paused, later messages go to it and no planning happens. Re-planning exists: a plan may end with the reserved `Re-plan` step, and when nothing is left paused `Execute` folds the summaries into the request and calls `Plan` again, up to `MaxPlanDepth` (default `DefaultMaxPlanDepth`, 3). But that is an escape hatch; the default shape is fan-out and join, not a cycle. The mapping breaks here: `Send` fans out too, but in LangGraph *your* router decides how many branches to send, while in Arboreal the planning LLM decides, by writing more or fewer steps.

The other difference is what the planner chooses among. A ReAct agent picks *tools*, through the model's function-calling API, from schemas you declared. The executive picks *behaviors*, by `Name()`, from the one-line `BehaviorDescription` of each, and not by function calling: `executivePlannerPrompt` asks for a JSON array in plain text, `Plan` parses it with `json.Unmarshal` (retrying through `fixJSON` if it is malformed) and looks each name up in `behaviorLookup`. Function calling exists one level down: an `LLMCompletionState` with `AllowTools: true` calls MCP tools in a loop inside the state until the model stops asking. Chapter 8 covers the executive.

```admonish warning title="Sharp edge"
Because the plan is free text rather than a constrained tool call, nothing stops the planner writing a name you did not declare; if it does, and the name is not `Re-plan`, `Plan` panics with `No plan named … found!`. The `OutOfBoundsHandler` is consulted only when the plan comes back empty, not when a name is wrong. Chapter 8 covers both.
```

### 3. The tree is a stateful struct

A compiled LangGraph is immutable. Whatever persists between calls lives in the checkpointer, keyed by `thread_id`, so one compiled graph serves any number of concurrent requests.

A `BehaviorTree` is the opposite. Alongside its `Graph` the struct carries `State`, the traversal stack, and `Traversed`, the visited set, and `BehaviorTree.Call` reads and writes both. That is the whole pause/resume mechanism: a `CollectUserInputSignal` leaves the stack as it is, and the next `Call` on the same value picks up from it, pushing `Graph.Initial()` only if the stack is empty. No checkpointer is involved because the position is on the struct. The mapping breaks here: one tree *instance* is one execution, not a reusable program, and two goroutines must never share one. The executive respects this itself: `Plan` calls `Copy()` for every step, and `Copy` shares the `Graph` but gives the copy an empty `State` and a fresh `Traversed`, which is why three concurrent steps of one behavior whose nodes are all states do not trample each other. `Copy` is one level deep: a nested tree is shared by pointer, stack and all, so concurrent steps of a behavior that contains a subtree do collide there, and a pause inside a nested tree does not resume inside it (Chapter 7 spells out both limits). Resume differs for the same reason. LangGraph re-runs the interrupted node from its top and hands the resume value back as the return value of `interrupt()`; Arboreal appends the user message to the step's `Messages` and continues from the tree's stack, or from the entry state if the stack is empty (Chapter 2, step 7).

Snapshots, in Part III, are for when the executive must outlive the process. `TakeSnapshot` in `snapshot.go` writes each tree's stack as `BehaviorRef`s, the `Hash()` values of the behaviors on it, and `Snapshot.Restore` rebuilds the stack by looking those hashes up in a freshly constructed tree. That works only if the new process builds its behaviors with the same ids. `CreateBehaviorTree`, `LLMCompletionState` and `PauseState` draw a random id on every call, so a tree you intend to restore is built with `CreateBehaviorTreeWithId`, `CreateTodoListExecutiveWithId` and the `Id` field of `LLMCompletionOptions`, and, for `PauseState`, by setting `HashId` on the returned value, as `examples/snapshot-simple` does. Chapter 7 covers the walk; Part III covers snapshots.

```admonish tip title="Coming from LangGraph"
The phrase "behavior tree" will also mislead if you know it from game AI: there are no Sequence/Selector nodes and no per-frame tick. Chapter 7 opens with a one-page primer and then lists exactly what Arboreal kept and what it replaced.
```

```admonish example title="Recap"
- Same words, different machine: graph, node, edge and interrupt all exist, but conditional routing and composites do not.
- Signals from inside a state replace routers on edges.
- The executive is plan-and-execute with concurrent steps.
- A tree instance carries its own execution state; copy before sharing.
```
