// Package main is a learning-purposes example, NOT a template for real apps.
//
// It probes TakeSnapshot and Restore with no model involved. The tree is a
// canned reply followed by a pause, and a plan is put "in flight" by
// restoring a hand-crafted snapshot — Restore is the only way to put a plan
// in flight from outside the package without calling the LLM planner, which
// also makes it the way to seed a plan in tests.
//
// Four probes:
//  1. a fresh executive snapshots to an empty map (no plan — no entry)
//  2. restore the crafted snapshot, run one Call (no LLM: canned state,
//     then pause), snapshot again — now the executive has an entry
//  3. restore into an executive built with a DIFFERENT executive id —
//     Restore finds nothing to do; silently a fresh start
//  4. restore a snapshot whose step references an unknown behavior id —
//     observe what happens (verified at writing time, documented as-is)
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zenful-ai/arboreal"
	"github.com/zenful-ai/arboreal/llm"
)

// ANCHOR: build
// build constructs the same executive every time, with stable ids
// throughout — Restore matches behaviors by hash, so the ids must be
// identical on every run.
func build(execID string) *arboreal.TodoListExecutive {
	tree := arboreal.CreateBehaviorTreeWithId(
		"greeter",
		"Replies with a canned greeting and waits",
		"Say hello to the user",
		"tree-greeter",
	)

	canned := arboreal.CannedResponseState("Hello! I am a canned reply.")
	canned.HashId = "state-canned"

	pause := arboreal.PauseState("Wait for the user")
	pause.HashId = "state-pause"

	tree.AddTransition(canned, &pause)

	return arboreal.CreateTodoListExecutiveWithId(
		"Edges", "Snapshot edge probe", execID, &tree)
}

// ANCHOR_END: build

// ANCHOR: craft
// craftSnapshot builds the JSON a paused turn would have produced: one
// executive entry holding a one-message history and one pending step that
// references the tree by id. Message lists are marshaled from real values so
// the field names always match the framework's.
// The envelope keys ("history", "plan", "ref", ...) are still hand-typed to
// match snapshot.go's json tags — unmarshal silently zeroes a misspelled one.
func craftSnapshot(execID, treeID string) arboreal.Snapshot {
	history := arboreal.AppendToMessages(nil, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Hello there",
	})
	direction := arboreal.AppendToMessages(nil, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Greet the user",
	})

	skeleton := map[string]any{
		execID: map[string]any{
			"history": history,
			"plan": []map[string]any{{
				"ref":              treeID,
				"snapshot":         map[string]any{},
				"messages":         direction,
				"replan_tombstone": false,
			}},
		},
	}

	data, err := json.Marshal(skeleton)
	if err != nil {
		panic(err)
	}

	var snap arboreal.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		panic(err)
	}
	return snap
}

// ANCHOR_END: craft

// ANCHOR: probes
func main() {
	ctx := context.Background()

	// 1. Fresh executive: no plan in flight, so no entry at all.
	fresh := build("exec-edges")
	snap, err := arboreal.TakeSnapshot(fresh)
	fmt.Printf("1. fresh executive: entries=%d err=%v\n", len(snap), err) // entries=0 err=<nil>

	// 2. Seed a plan via Restore, run one hermetic turn, snapshot again.
	seeded := build("exec-edges")
	if err := craftSnapshot("exec-edges", "tree-greeter").Restore(seeded); err != nil {
		panic(err)
	}

	h := arboreal.AppendToMessages(seeded.History, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Anyone home?",
	})
	h, _ = seeded.Call(ctx, h)
	fmt.Printf("2. after Call: reply=%q\n", h.LastMessage().Content) // reply="Hello! I am a canned reply."

	snap, err = arboreal.TakeSnapshot(seeded)
	fmt.Printf("   snapshot: entries=%d err=%v\n", len(snap), err) // entries=1 err=<nil>

	// 3. Same snapshot, different executive id: Restore finds no matching
	// entry and silently restores nothing.
	other := build("exec-other")
	err = craftSnapshot("exec-edges", "tree-greeter").Restore(other)
	snap2, _ := arboreal.TakeSnapshot(other)
	fmt.Printf("3. wrong exec id: restore err=%v, entries after=%d\n", err, len(snap2)) // err=<nil>, entries after=0

	// 4. A step that references a behavior id the executive does not have.
	// Restore's lookupMap has no entry for "tree-gone", and Restore returns
	// an error naming the ref and installs nothing.
	broken := build("exec-edges")
	err = craftSnapshot("exec-edges", "tree-gone").Restore(broken)
	fmt.Printf("4. unknown step ref: err=%v\n", err) // err=arboreal: plan step "tree-gone" refers to no behavior of this executive
}

// ANCHOR_END: probes
