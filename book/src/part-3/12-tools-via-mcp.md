# Tools via MCP

## Why this exists

Every state in Parts I and II produced text and only text. `LLMCompletionOptions` has carried the way out all along: `AllowTools` and `Tools`, the two fields of that struct this book has not yet used, opt a state into letting the model call tools mid-completion and say which. Arboreal has exactly one tool mechanism, the Model Context Protocol. There is no "register a Go function" path — no registry, no reflection over signatures; a Go function becomes a tool by being served over MCP. That sounds heavier than it is: the MCP SDK ships an in-memory transport, so the server that makes your function a tool can live in the same process as the agent, a goroutine away, with no network in between. The machinery splits in two — `mcp.go` holds the client side, a mux over any number of servers plus the context plumbing that delivers it, and `state.go` holds the loop a state with `AllowTools` runs against that mux. This chapter reads both, then runs them.

## The mux

`MCPClientMux` in `mcp.go` is one MCP client over many servers. Every `Add*` call connects a session, asks the server for its tool list once, converts each tool's schema into the typed form the `llm` package carries, and files the tool into two maps: name to definition, name to session. `Tools()` collects the definitions into one `[]llm.ChatTool`, the shape a completion request wants; `Select(names...)` returns just the named definitions, in the order given, and errors on a name no server claimed (listing what the mux has) or a name given twice; `CallTool` looks up the session by tool name and forwards the call, or errors on a name no server claimed; `Close` closes every session — `defer` it next to the `Add`. Two servers exporting the same tool name collide silently: the later registration wins both maps.

Three ways in. `AddInMemoryServer` takes a transport — the client half of the pair `mcp.NewInMemoryTransports` returns, with the server's `Run` holding the other half in a goroutine — and is how a function in your own process becomes a tool. `AddStreamableHTTPServer` takes a base URL and a `*StreamableHTTPOptions`, whose one field, `HTTPClient`, is the auth seam: `NewBearerHTTPClient(token)` builds a client that stamps `Authorization: Bearer` onto every request, `NewBearerTransport` layers the same onto an `*http.Client` you already have, and `nil` means `http.DefaultClient`. `AddSSEServer` takes just a URL; it has no options struct and therefore no auth seam yet — its own `TODO` says so.

Then delivery. The mux travels in the context: `WithMCPClient(ctx, mux)` stores it under an unexported typed key, and `MCPClientFromContext` gets it back. This is the exported, typed pair that Chapter 11's raw `"arboreal_trace"` string is not — the same context-value habit, finished.

## The `AllowTools` loop

`LLMCompletionState`'s lambda in `state.go`, after the system-prompt work Chapter 5 covered, does one new thing when `options.AllowTools` is set: if `MCPClientFromContext` finds a mux, the request gets tools. Which tools depends on `options.Tools`. Empty, and the request gets `client.Tools()` — every tool of every server the mux connected to, in Go map order, which is not stable from one call to the next. Non-empty, and it gets `client.Select(options.Tools...)` — exactly the named tools, in the order written, on every call. With `Tools` empty, both of those conditions — `AllowTools` set and a mux found — must hold for the request to carry tools; no mux, or no `AllowTools`, and the request simply carries none, and neither absence is an error. With `Tools` set the state has been explicit, and five configurations become an `ErrorSignal` of type `StateErrorTypeUnrecoverable` before any model call: `Tools` without `AllowTools`; `Tools` with no mux in the context; `Tools` on an annotation-mode state, whose path never offers tools; a name the mux does not have; a name given twice. The state reports the first three; `Select` reports the last two, naming the offender and, for an unknown name, listing what the mux has. None of them appends a reply: the annotation-mode guard returns the history untouched; the other four return it as step 5 of Chapter 5 left it, so a `System` prompt is already message 0.

Then the completion becomes a loop. Ask the model; if the reply carries no tool calls, break. Otherwise execute the first call through `CallTool`, turn the result's first content into a string — a `TextContent`'s text, anything else marshaled to JSON — and build a `function`-role message carrying that string, the tool's name, and the call's `Meta`. The assistant reply that made the call and the result message are then appended to both lists the loop maintains: the history the state will return, and `request.Messages`, so the next round's model sees what its tool said. Then ask again. The reply that finally has no tool calls breaks the loop and is appended once, after the loop, with the usual `Terminal` handling — from the outside, a state that ran three model rounds returns exactly like one that ran one.

Narrowing what is *offered* does not narrow what the model can *name*: a model can answer with a function it was never sent. Both providers that speak tools, OpenAI and Anthropic, blunt that by mapping the returned name back through the list they sent, so an unknown name reaches the loop as `""` — but that is each provider's habit, not the loop's guarantee, and the loop hands whatever name the reply carries straight to `CallTool`, which forwards any name the mux knows. So when `Tools` is set the loop checks the reply's name against the list before calling, and refuses a name outside it with an `ErrorSignal` of type `StateErrorTypeRetryable` — a retry may get a different answer; under the default provider the message reads `model called ""`. The invariant this buys lives in `state.go`, whichever provider is wired, and it is the one the option exists for: the offered list is the only thing this state's loop will ever execute, and the mux may be connected to anything. It is a property of the state, not of the mux: another state with `Tools` empty, or code that calls `CallTool` on the mux directly, still reaches every tool the mux knows.

One limit, read straight off the source: `ToolCalls[0]` is hardcoded, so a reply that makes several calls gets exactly one executed, and the extras survive only as unexecuted entries inside the appended assistant message — no result, no error. (The default OpenAI provider still speaks the legacy single-function-call API and can surface only one call per reply anyway; providers that do parallel calls hit this limit for real.) And one thing that is not a limit: tool calls and their results do land in the history — the two appends in the loop put them there, and the dump in the next section is the proof.

That history is the state's, which under an executive is the step's own message list, and a finished step's list is dropped when the turn ends (Chapter 8). So the executive copies the executed calls out. After the fan-out, for every step that ran — finished, paused or failed — and, when the plan was empty, for the out-of-bounds handler, each `function`-role message the step added this turn that directly follows an assistant message whose first tool call names the same tool becomes a `ToolCallRecord{Step, Tool, Arguments, Result}`: the behavior's `Name()`, the tool, a shallow copy of the call's arguments map, and the result string the loop built. The records are collected in plan order on the executive's `ToolCalls` field, and `Call` and `RunLoop` pin the slice onto the reply they append as the `tool_calls` annotation (`AnnotationToolCalls`), absent when no tool ran. A paused step's reply records this turn's calls only, however long its list has grown. Nothing that produced no result is in it: a call the state refused, a `CallTool` that failed, and the unexecuted extras of a parallel reply. Read it back with `ToolCallsFromAnnotation`, which decodes the typed slice before a JSON round trip and the generic shape after one. `GetAnnotation` is the wrong tool here, since it returns only the newest entry; a reader that wants every call in a transcript walks the transcript:

```go
for _, m := range transcript {
	if a, ok := m.Annotations[arboreal.AnnotationToolCalls]; ok {
		calls, err := arboreal.ToolCallsFromAnnotation(&a)
		// …
	}
}
```

## Run it

`examples/tools` is the whole mechanism in one file: one tool, one question, one `Call` — no executive, no tree, a state driven directly as in Chapter 5. The tool is an ordinary function:

```go
{{#include ../../../examples/tools/main.go:server}}
```

The wiring builds the server, connects a mux to it over the in-memory transport, and puts the mux in the context:

```go
{{#include ../../../examples/tools/main.go:wiring}}
```

And the state asks a question only the tool can answer:

```go
{{#include ../../../examples/tools/main.go:call}}
```

Run it with `go run ./examples/tools`; it needs `OPENAI_TOKEN`. The timestamp is whatever now is; the rest is stable. One run printed:

```text
[0] system    "Answer using the tools available to you."
[1] user      "What is the exact current timestamp?"
[2] assistant ""
[3] function  "2026-09-02T00:08:30+02:00"
[4] assistant "The exact current timestamp is 2026-09-02T00:08:30+02:00."
```

Five messages, four roles. `[0]` is the system prompt the state prepended; `[1]` is the caller's question. `[2]` is the tool call, and it prints as an empty string because the dump prints `Content` with `%q` and a tool-call reply carries no content — the call lives in the message's `ToolCalls` field, name and arguments, which the dump cannot show. `[3]` is the `function`-role result: the string the MCP server returned, filed under the tool's name in the message's `Name` field, also unprinted. `[4]` is the reply that had no tool calls, appended after the loop. Two model rounds produced these five messages; attach Chapter 11's trace and you would still see a single entering/leaving pair for the state — the extra round per tool call happens inside the state's one envelope, and nothing on the wire marks it.

## Run it, narrowed

`examples/tools-limited` is the same shape with the mux connected to more than the state needs. The server exposes three tools; only the first is meant for the state:

```go
{{#include ../../../examples/tools-limited/main.go:server}}
```

The wiring connects the mux to the whole server, then names the list once, as `offered`, and runs `Select` on it — the same query the state is about to make, so what is printed is what the model will be sent (by the mux's names; a provider may shorten them on the wire), by construction rather than by writing the same string twice:

```go
{{#include ../../../examples/tools-limited/main.go:wiring}}
```

And the state takes the same list and asks a question that needs two tools:

```go
{{#include ../../../examples/tools-limited/main.go:call}}
```

Run it with `go run ./examples/tools-limited`; it needs `OPENAI_TOKEN`. One run printed:

```text
server exposes 3 tools:
  ai.zenful/delete_everything
  ai.zenful/get_current_timestamp
  ai.zenful/get_weather
state offers 1:
  ai.zenful/get_current_timestamp

[0] system                                     "Answer using only the tools available to you. If part of the question needs a tool you do not have, say so instead of guessing."
[1] user                                       "What is the exact current timestamp, and what is the weather like?"
[2] assistant                                  ""
[3] function  ai.zenful/get_current_timestamp  "2026-09-04T19:17:44+02:00"
[4] assistant                                  "The exact current timestamp is 2026-09-04T19:17:44+02:00. \n\nI do not have access to real-time weather information. You can check a weather website or app for the current conditions."
```

The first block is the point: three on the mux, one offered. In the dump, the third column is the message's `Name`, set only on the `function`-role result, and it shows the one tool that ran. The model was asked about the weather too and had nothing to ask with; the system prompt told it to say so rather than guess, and it did. `ai.zenful/get_weather` and `ai.zenful/delete_everything` were on the mux the whole time, one `Select` away, and the model never learned they existed.

## Coming from LangGraph

`AllowTools` plus a mux in the context is `bind_tools` plus a `ToolNode`, with one structural difference: the execute-and-ask-again loop lives inside the state, not in the graph. There is no tools node to wire and no conditional edge routing tool-call replies back to the model; a state with `AllowTools` is the whole agent-with-tools cycle in one node, and the rest of the tree never knows a tool ran. The other habit to unlearn is conflating this with planning. In LangGraph the canonical agent chooses its next action by calling tools; Arboreal's planner does not — choosing a behavior (Chapter 8) is a prompt-and-annotation affair at the layer above, and calling a tool is a mid-completion round trip inside one state. Different mechanisms at different layers, and the planner never sees the mux.

## Sharp edges

```admonish warning title="Sharp edge"
Only the first tool call of each reply is executed; parallel tool calls are dropped without an error and never produce a result — at most they ride along, unexecuted, inside the appended assistant message. Prompt accordingly, or expose only tools that make sense one at a time.
```

```admonish warning title="Sharp edge"
No mux in the context means no tools — silently, as long as `Tools` is empty. `AllowTools: true` with a missing `WithMCPClient` is a state that simply never offers tools; nothing fails. A state that names its tools gets an unrecoverable `ErrorSignal` instead, which is one more reason to name them. When a model "refuses" to use a tool, check the context first.
```

```admonish warning title="Sharp edge"
Tools reach the model only through a state that sets `AllowTools`. The planner, annotation-mode states, and every default state ignore the mux entirely.
```

## Back to the trace

The tool loop lives inside Chapter 2's step 4 — the tree walks its states, and one state's `Call` may now hold several model requests instead of one. Nothing else in that chapter's sequence moves: step 2's planner never sees the mux, step 3's fan-out is unchanged, step 5 triages the same signals, and the one addition straddles them: at the end of step 3, once every goroutine has returned, the executive reads the executed calls off each step's list, and in step 6 the reply it appends carries them. On the wire, Chapter 11 showed step 4 as one begin/end pair per state; it still is. Tools change what a state does and what a reply carries, not what a turn is.

```admonish example title="Recap"
- One mechanism: MCP. A mux merges servers; the context delivers it; `AllowTools` opts a state in.
- `Tools` narrows what a state offers to the names given, in that order; the loop executes nothing outside it, and a name the mux lacks fails before the model is called.
- In-memory transport = tools with no network; HTTP/SSE for real servers, bearer on HTTP only.
- One tool call per round; a missing mux is silent (with `Tools` empty); `Close` the mux.
- Tool calls and results do land in the history, and the executive copies the executed ones onto the reply as `tool_calls`; a finished step's calls are not lost with it.
```
