# Tracing

## Why this exists

Every claim in this book so far was verified after the fact: run the turn, then dump the transcript, the step's `messages`, the snapshot, and read what happened. The framework can also narrate live. A `Trace`, in `trace.go`, is `chan *TraceMessage` — not a logger, a channel — and every executive, tree and state on the turn path looks for one in the context and sends into it as it enters and leaves. No registration, no levels, no sinks: you make the channel, put it in the context, and read the other end. This chapter is that one type and the three places that send to it.

## The message

Here is the whole payload, verbatim from `trace.go`:

```go
type TraceMessage struct {
	Type       string                   `json:"type"`
	ID         string                   `json:"id"`
	ClientID   string                   `json:"client_id,omitempty"`
	Name       string                   `json:"name"`
	Message    string                   `json:"message"`
	Error      error                    `json:"error,omitempty"`
	Telemetry  *TraceTelemetry          `json:"telemetry,omitempty"`
	Operations []*TraceHistoryOperation `json:"operations,omitempty"`
	Signal     *TraceSignal             `json:"signal,omitempty"`
}
```

`Type` is one of three constants; in practice only `begin_call` and `end_call` flow — `lua_source` belongs to the Lua engine and never fires here. `ID` and `Name` are the sender's `Hash()` and `Name()`: name your states or their lines are anonymous, as the pause state is about to demonstrate — nothing sets a `PauseState`'s `StateName`. (`ClientID` rides along for callers that tag behaviors; nothing in this book sets it.) `Message` is a fixed string per emit site: `BehaviorTree.Call` sends "entering/leaving behavior tree", `BehaviorState.Call` — every state is one — "entering/leaving custom state", and the executive's `Call` sends "entering/leaving planner state", which is its wording for its own envelope, the one Chapter 9's anatomy ran inside. The planner's actual model call emits nothing of its own: Chapter 5 showed `Plan` invoking the state's `Lambda` directly, bypassing the envelope.

`Signal` is `TraceForSignal`'s translation of the sender's returned signal — `error`, `skip`, `stop` (a `Terminal`), `user` (a pause) — with `Reason` from the signal's `Description()`. It rides the `end_call` of the state that raised it and of the tree that returned it; the executive's `end_call` carries one only when the turn produced no reply, an `error` with `Error` set, which is the one signal `Call` returns (Chapter 9). A turn that merges two or more steps also shows a `summarize` begin/end pair, the executive's own state. `Telemetry` is start and end times; `Error` is set when a state returned an `*ErrorSignal`. `Operations` promises what the state did to the history — message adds and annotation adds, the latter fed by the `__trace_annotations` breadcrumb that Chapters 4 and 5 met as the tracer's own bookkeeping on messages — but the message-add half never fires; the sharp edges say why. On the executive's own `end_call` the field reports instead what the turn's steps crossed (Chapter 8): one operation of type `annotation` per outcome, with `Action` `cross` for an annotation placed, `merge` for a later step's value merged into a placed one through its `Merge`, `keep` for a later value dropped because the placed one has none, and `skip` for a marked annotation under a reserved name; the `Annotation` on each is the value in question, after the merge for `merge` and the dropped one for `keep`. They ride the `end_call` whatever the turn's signal, since the fold happens before the reply is decided.

## Run it

`examples/trace-turn` is Chapter 9's two turns with a trace attached. Two additions make it work. First a drain — the channel is unbuffered, so a goroutine must be reading before the first `Call`:

```go
{{#include ../../../examples/trace-turn/main.go:drain}}
```

Then the attachment, which is nothing but a context value under a raw string key:

```go
{{#include ../../../examples/trace-turn/main.go:attach}}
```

Run it with `go run ./examples/trace-turn`; it needs `OPENAI_TOKEN`. One run printed:

```text
begin_call Chat Bot       entering planner state
begin_call chat_behavior  entering behavior tree
begin_call chat           entering custom state
end_call   chat           leaving custom state
begin_call                entering custom state
end_call                  leaving custom state  [signal: user "Let user respond"]
end_call   chat_behavior  leaving behavior tree  [signal: user "Let user respond"]
end_call   Chat Bot       leaving planner state
[the same eight lines repeat for the second turn]
---
[0] user      Hi, I'm Paul. Please remember my name.
[1] assistant Got it, Paul! How can I assist you today?
[2] user      What is my name?
[3] assistant Your name is Paul. How can I help you today?
```

Read it top to bottom. The outermost pair is the executive's envelope. Inside it, the tree; inside that, the named `chat` state, then an anonymous state — the pause, nameless because nothing named it — whose `end_call` carries `user "Let user respond"`, the `CollectUserInputSignal` and its reason. The tree's `end_call` repeats the signal on the way out; the executive's does not. This is Chapter 2's diagram printing itself, live. Turn two is the resume, and the trace cannot show it: the resume branch skips `Plan`, but the planner's call never had a line to lose, so the same eight lines print again — the envelope opens, the same tree walks again, the same pause holds it open.

## Coming from LangGraph

The nearest LangGraph analogue is `stream_mode="debug"`, or a callback handler, minus every piece of subscription machinery: there is one channel, every event goes into it, and filtering is a `for` loop you write. What you do not get is per-node token streaming — a `TraceMessage` marks a state entered or left, never a partial completion — so the trace narrates the structure of a turn, not the text being generated inside it.

## Sharp edges

```admonish warning title="Sharp edge"
The channel is unbuffered and `Send` is a plain send. Attach a trace and stop draining it and the agent deadlocks mid-turn, silently — before the first line of output. Drain until you close the channel, from a goroutine that outlives every `Call`. (A nil trace — no key in the context — is safe: `Send` is a no-op.)
```

```admonish warning title="Sharp edge"
`Operations` never reports a history change. The diff in `BehaviorState.Call` iterates the slice the state was called with, not the one it returned, so appended messages never reach the trace — only annotation adds can appear, and only from states that leave the breadcrumb. What a state did to the history must still be read from the history itself, and what a turn's tools did is not on the trace either: read it from the `tool_calls` annotation on the reply the executive appends (Chapter 12).
```

```admonish warning title="Sharp edge"
The context key is the raw string `"arboreal_trace"`; there is no exported constant or `WithTrace` helper (contrast `WithMCPClient` in Chapter 12). The string is API surface — spell it exactly, and expect a collision if anything else in your context uses the same key.
```

## Back to the trace

Set the eight lines against Chapter 2's numbered steps. The executive's envelope brackets steps 2 through 5; the tree and state lines are step 3's fan-out entering step 4's walk, one line per stop; the `user` signal on the way out is what step 5's triage reads; and step 2's planner call is the one participant with no line of its own. Chapter 12 will lean on this view — one begin/end pair per state — to show what tools do *not* change.

```admonish example title="Recap"
- `Trace` is `chan *TraceMessage` under the context key `"arboreal_trace"`.
- Drain it or deadlock; nil is a safe no-op.
- Name your states or their lines are anonymous; signals ride the state's and the tree's `end_call`, and the executive's only when the turn produced no reply; `Operations` never carries a history change; what a turn's tools did is on the reply's `tool_calls`, not on the trace, and what its steps crossed is on both, the reply and the executive's `end_call`.
```
