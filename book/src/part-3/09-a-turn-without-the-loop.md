# A turn without the loop

Every agent in Parts I and II ran the same way: `RunLoop` owned a terminal, blocked until you typed, and kept the conversation alive for as long as the process ran. That shape fits a chat window and almost nothing else. In most deployments a message arrives as its own event — from a queue, a webhook, a job — and lands in a process that handles one message per invocation. That process does not own the conversation; it must pick the conversation up, run one turn, and get out, and the next message may land in a different process entirely.

Part III is everything that makes that work. This chapter finds the per-message API that has been hiding in Chapter 8 and keeps Chapter 2's promise by reducing `RunLoop` to a few lines around it. Chapter 10 makes a conversation survive a process exit, so the next turn can run somewhere else. Chapter 11 watches a turn while it runs. Chapter 12 gives states tools. Chapter 13 says what can be tested without a model on the wire.

## Why this exists

`RunLoop` in `executive.go` is a `for` loop with no exit condition over a blocking `Channel.Receive`. That is the right shape exactly when the process owns the conversation end to end: something has to sit and wait for the next message, and the loop is that something. In a process that handles one message per invocation there is nothing to sit and wait *on*. The message is already in hand when the process starts, and when the turn is over the process should return, not block. The loop is not wrong there; it is unemployable.

The per-message API has been in front of us since Chapter 8. Its section "`Call` and `RunLoop` are twins" said the two methods run the same turn, and `examples/oneshot` already drove a single turn through `Call`. What that chapter did not establish is that `Call` is the only correct way to drive turn after turn without the loop. This chapter does.

## The anatomy of `Call`

`Call(ctx, messages)` in `executive.go` does four things. If the plan is empty, it calls `Plan(ctx, messages)`. Otherwise it takes the resume branch: for each pending step it appends `*messages.LastMessage()` — the message the caller just added — to that step's `Messages`, and no planning happens. Then `Execute(ctx, messages)`. Then it appends `e.Output` to the list as an assistant-role message and returns the list, with a signal that is always `nil`, all inside the trace envelope from Chapter 5. This is Chapter 8's twins section one level down, and the level down is where the contract lives: every read and write goes through the `messages` argument. `Call` works on the list it is given, and the executive's `History` field never appears in its body.

That last point deserves a table. Of the four methods on the turn path, exactly one touches `History`:

| Method | Reads `History`? | Writes `History`? |
|---|---|---|
| `Plan` | no | no |
| `Execute` | no | no |
| `Call` | no | no |
| `RunLoop` | yes | yes |

The transcript, in other words, is the caller's job. `RunLoop` is one such caller: it appends what `Receive` returned and what `Execute` produced to `e.History` and runs the turn on that field. Without the loop, you keep the list yourself — append the user's message, hand the list to `Call`, keep what comes back. (The table is about the field. `Execute`'s re-plan path does rewrite the last message of whatever list it is handed — Chapter 8 traced that — so a caller's transcript can still be edited through the argument; but no method except `RunLoop` names `e.History`.)

Could you go lower and call `Plan` and `Execute` yourself? On the first turn, yes: that pair is exactly what `Call` runs when the plan is empty, minus the envelope and the final append. From the second turn on, no, for two reasons both visible in `Plan`. First, `Plan` begins by discarding any plan in flight: its first act after rendering the planner prompt is to reset `e.plan` to an empty slice, so if a step paused last turn, calling `Plan` erases it before the planner even answers. Second, the field is `plan`, unexported. The resume branch — append the new message to each pending step's `Messages` — reads and writes state your code cannot reach from outside the package, so it cannot be written by hand at all. "Plan then Execute" is therefore a first-turn-only idiom; `Call` is that same pair with the resume branch included, and it is the only method that has one.

One corner carries over from Chapter 8: `Execute` with an empty plan is not a no-op. It hands the last message to `OutOfBoundsHandler`, or, with none set, makes `Output` the literal `Please set an out-of-bounds handler, this request was unable to be planned.` — the first case of that chapter's `Execute` walkthrough. An empty plan is a routed outcome, not an idle one, and a per-message caller sends its result to the user like any other reply.

## Run it

`examples/one-turn` is the quickstart executive — one tree, `chatState` then `pauseState` — driven for two turns with no loop, no channel, and a transcript carried by hand. `buildExecutive` is Chapter 1 unchanged:

```go
{{#include ../../../examples/one-turn/main.go:build}}
```

`turn` is one message's worth of work: append the user's words to the transcript we own, run `Call` once, hand back the transcript with the reply appended. `Call`'s signal is `nil` whenever the turn produced a reply; a failed reply comes back as an `*ErrorSignal` with the transcript unchanged, and this demo lets that stand, so a failed turn would print a user message with no reply after it:

```go
{{#include ../../../examples/one-turn/main.go:turn}}
```

`main` runs two turns and prints the transcript:

```go
{{#include ../../../examples/one-turn/main.go:main}}
```

Run it with `go run ./examples/one-turn`; it needs `OPENAI_TOKEN` and exits on its own. One run printed the following — the model's wording varies between runs:

```text
[0] user      Hi, I'm Paul. Please remember my name.
[1] assistant I'm sorry, but I can't remember personal information such as names for future interactions. However, I'm here to help you with anything you need right now! How can I assist you today, Paul?
[2] user      What is my name?
[3] assistant Your name is Paul. How can I assist you today?
```

Read the output against the anatomy. The first turn planned: the plan was empty, so `Call` called `Plan`, the planner wrote a direction for `chat_behavior`, `chatState` replied, `pauseState` paused, and the kept step's last message became `Output` — message `[1]`. The model even protests that it cannot remember a name; the framework then remembers for it, because the pause kept the step and its message list alive on the executive. The second turn resumed: the plan held one step, so `Call` skipped `Plan` and routed `What is my name?` into the pending step, whose `Messages` already held the first exchange — the direction that carried the name, and the protest. The model saw a conversation and answered it — message `[3]`.

Everything here lives in one process. The transcript is ours, but the thing that made turn two work — the paused step, its growing message list, its tree copy — sits in the unexported `plan` field of an executive in memory. Put a process exit between the two turns and it is gone. For the next message to land in a fresh process, the pending plan has to survive as data. That is Chapter 10.

## `RunLoop`, revisited

Here is all of `RunLoop`, from `executive.go`, trimmed of nothing:

```go
func (e *TodoListExecutive) RunLoop(ctx context.Context, c Channel) error {
	for {
		cm, err := c.Receive()
		if err != nil {
			return err
		}

		e.History = AppendToMessages(e.History, llm.ChatCompletionMessage{
			Role:    llm.ChatMessageRoleUser,
			Content: cm.Content,
		})

		if len(e.plan) == 0 {
			e.Plan(ctx, e.History)
		} else {
			for _, step := range e.plan {
				step.Messages = append(step.Messages, *e.History.LastMessage())
			}
		}

		if sig, ok := e.Execute(ctx, e.History).(*ErrorSignal); ok {
			return fmt.Errorf("arboreal: the turn produced no reply: %w", sig)
		}

		e.History = AppendToMessages(e.History, llm.ChatCompletionMessage{
			Role:    llm.ChatMessageRoleAssistant,
			Content: e.Output,
		})

		err = c.Send(&ChannelMessage{
			Id:      cm.Id,
			Content: e.Output,
		})
		if err != nil {
			return err
		}
	}
}
```

Read it top to bottom: receive; append the user's message to `History`; the same plan-or-resume as `Call`, run on `e.History` instead of an argument; `Execute`, and if it reports a failed reply, return it as the loop's error, with `History` ending on the user's message and nothing sent; otherwise append `Output` to `History`; send. Every mechanism the loop exercises belongs to `Call`'s turn. What the loop adds is the channel and the upkeep of `History` — precisely the two things that are the caller's job everywhere else in Part III.

The `Channel` interface — `AllocateID`, `Send`, `Receive` in `channel.go` — exists for this loop style, and the framework ships two implementations: the `TerminalChannel` you have been typing into, and `TwilioSMSChannel`, which buffers webhook posts into a Go channel so that `Receive` has something to block on. This book does not cover them further.

## Coming from LangGraph

`Call` is `compiled_graph.invoke` with no checkpointer configured: nothing saves state for you, so you thread it yourself — and the state here is the transcript, passed in with the new message appended, taken back with the reply appended, and handed to the next invocation. `RunLoop` is the `while True:` REPL a tutorial wraps around `invoke` to make a chat demo. What LangGraph splits between a checkpointer and a `thread_id` — where a conversation's state lives, and which conversation a message belongs to — Arboreal splits between your transcript and Chapter 10's snapshots: you carry the conversation you would show the user, and the snapshot carries the paused plan.

## Sharp edges

```admonish warning title="Sharp edge"
Calling `Plan` on an executive with a plan in flight destroys the plan: its first act is to reset the list. A paused conversation planned again is a conversation forgotten. After turn one, go through `Call`.
```

```admonish warning title="Sharp edge"
`Call` never touches `History`. If anything downstream reads the executive's own transcript — `TakeSnapshot` does (Chapter 10) — you must assign `History` yourself, before or after `Call`. Forgetting is silent: everything works until a restore comes back empty.
```

```admonish warning title="Sharp edge"
`Call` returns `nil` for every turn that produced a reply, even when a step paused inside; only a failed reply comes back as an `*ErrorSignal`, with the list unchanged, so a caller that drops the signal and reads the last message reads the user's own words on that turn. From the outside you still cannot ask "is this conversation waiting on the user?" — the snapshot is how you find out (Chapter 10).
```

## Back to the trace

Chapter 2 numbered the seven steps of a turn and promised that this chapter would reduce `RunLoop` to a few lines around `Call`. The lines are above; here is the accounting. Steps 2 through 5 — `Plan`, the fan-out in `executePlan`, the tree walk, the reply decision — are exactly what `Call` runs. Step 1's `Receive` and step 6's `Send` are gone; there is no channel. The appends `RunLoop` made to `History` — the user's message in steps 1 and 7, the `Output` in step 6 — become appends to the caller's own transcript: in `examples/one-turn`, `turn` makes the first before calling `Call`, and `Call` itself makes the second onto the list it returns. And step 7's resume — skip `Plan`, append the new message to the paused step's `Messages`, `Execute` again — is `Call`'s else branch. Nothing on Chapter 2's page belonged to the loop. The loop just called it.

```admonish example title="Recap"
- `Call` is the per-message turn: plan or resume, execute, append the output.
- The transcript is yours: no method but `RunLoop` maintains `History`.
- `Plan` wipes an in-flight plan; `plan` is private — after turn one, only `Call`.
- An empty plan at `Execute` means the out-of-bounds path, not a no-op.
```
