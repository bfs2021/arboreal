package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zenful-ai/arboreal"
	"github.com/zenful-ai/arboreal/llm"
)

// craftHoldSnapshot seeds a plan step in flight that has NOT run yet — the
// plannerless stand-in for what Plan() would have produced, so the first
// Call below is the turn that pauses. (A turn actually paused inside
// place_hold would also carry the tree's cursor in the step's "snapshot";
// run 1's own snapshot does, unlike this seed.) Seeding through Restore is
// the only plannerless way to put a plan in flight — Chapter 10; the shape
// follows examples/snapshot-edges.
func craftHoldSnapshot() arboreal.Snapshot {
	direction := arboreal.AppendToMessages(nil, llm.ChatCompletionMessage{
		Role:    llm.ChatMessageRoleUser,
		Content: `The customer said: "Please put Solaris on hold for me."`,
	})

	// No "history" key: the transcript is the caller's; the seed carries
	// only the in-flight step.
	skeleton := map[string]any{
		"exec-bookshelf": map[string]any{
			"plan": []map[string]any{{
				"ref":              "tree-hold",
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

// ANCHOR: exec-roundtrip
// TestHoldPausesAcrossProcesses is main's lifecycle with the process exit
// played by json.Marshal/Unmarshal: run 1 asks the canned confirmation and
// pauses; run 2, on a freshly built executive, takes the answer and
// finishes — after which the snapshot is empty again, which is what tells
// main to delete the file. No token needed: the hold tree is model-free,
// and on each turn exactly one step runs, so its reply is returned as
// written and the executive's summarizer never runs (Chapter 8). Both
// replies are asserted, and so is the empty snapshot.
func TestHoldPausesAcrossProcesses(t *testing.T) {
	t.Setenv("OPENAI_TOKEN", "") // nothing here should reach a model; if the summarizer were reached, the turn would fail and the reply assertion would catch it
	ctx := context.Background()

	first := buildExecutive()
	if err := craftHoldSnapshot().Restore(first); err != nil {
		t.Fatal(err)
	}

	h := arboreal.AppendToMessages(first.History, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Please put Solaris on hold for me.",
	})
	h, _ = first.Call(ctx, h)

	if got := h.LastMessage().Content; got != holdQuestion {
		t.Fatalf("run 1 replied %q, want the canned confirmation question", got)
	}

	first.History = h
	snap, err := arboreal.TakeSnapshot(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) == 0 {
		t.Fatal("expected the paused hold to appear in the snapshot")
	}

	// The "process exit": nothing survives but bytes.
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var revived arboreal.Snapshot
	if err := json.Unmarshal(data, &revived); err != nil {
		t.Fatal(err)
	}

	second := buildExecutive()
	if err := revived.Restore(second); err != nil {
		t.Fatal(err)
	}

	h2 := arboreal.AppendToMessages(second.History, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Yes, go ahead.",
	})
	h2, _ = second.Call(ctx, h2)

	if got := h2.LastMessage().Content; got != holdPlaced {
		t.Fatalf("run 2 replied %q, want the hold confirmation as written", got)
	}

	second.History = h2
	snap2, err := arboreal.TakeSnapshot(second)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap2) != 0 {
		t.Fatalf("expected an empty snapshot after the hold completed, got %d entries", len(snap2))
	}
}

// ANCHOR_END: exec-roundtrip

// ANCHOR: tree-roundtrip
// TestHoldTreeCursorSurvivesSnapshot drives the hold tree directly. The
// pause sits mid-chain — placeHold is still on the walk's stack — so the
// tree records its own snapshot entry (state + traversed, Chapter 10), and
// the restored copy must RESUME at placeHold, not restart at the question.
// The "Done —" assertion proves it: a restarted walk would answer with the
// canned question instead.
func TestHoldTreeCursorSurvivesSnapshot(t *testing.T) {
	ctx := context.Background()

	tree := buildHoldTree()
	h := arboreal.AppendToMessages(nil, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Put Solaris on hold, please.",
	})
	h, sig := tree.Call(ctx, h)
	if _, ok := sig.(*arboreal.CollectUserInputSignal); !ok {
		t.Fatalf("expected the tree to pause, got signal %#v", sig)
	}

	snap, err := arboreal.TakeSnapshot(&tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) == 0 {
		t.Fatal("expected the mid-chain pause to record the tree's cursor")
	}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var revived arboreal.Snapshot
	if err := json.Unmarshal(data, &revived); err != nil {
		t.Fatal(err)
	}

	tree2 := buildHoldTree()
	if err := revived.Restore(&tree2); err != nil {
		t.Fatal(err)
	}

	h = arboreal.AppendToMessages(h, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Yes, go ahead.",
	})
	h, _ = tree2.Call(ctx, h)

	if got := h.LastMessage().Content; got != holdPlaced {
		t.Fatalf("restored tree replied %q, want %q", got, holdPlaced)
	}
}

// ANCHOR_END: tree-roundtrip

// TestHoldRefusalLeavesTheBookOnTheShelf is the only guard on the refusal
// path — it pins that the matcher reads words, not substrings ("book"
// contains "ok"), and the customer's answer, not the question that asked
// it. Not redundant with the tests above: both of those only ever say yes.
func TestHoldRefusalLeavesTheBookOnTheShelf(t *testing.T) {
	ctx := context.Background()
	tree := buildHoldTree()
	h := arboreal.AppendToMessages(nil, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Put Solaris on hold, please."})
	h, _ = tree.Call(ctx, h)
	h = arboreal.AppendToMessages(h, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "No, don't book it."})
	h, _ = tree.Call(ctx, h)
	if got := h.LastMessage().Content; got != holdSkipped {
		t.Fatalf("refusal replied %q, want %q", got, holdSkipped)
	}
}

// TestAvailabilityOpensWithTheCustomersWords pins the configuration the
// lookup depends on: the availability tree opens its step with the
// customer's message as typed and then the planner's direction (Chapter 8),
// so the title reaches the catalog tool in the customer's words. No token:
// Open never calls a model.
func TestAvailabilityOpensWithTheCustomersWords(t *testing.T) {
	exec := buildExecutive()
	var availability arboreal.Step
	for _, b := range exec.Behaviors {
		if b.Name() == "check_availability" {
			availability = b
		}
	}
	if availability == nil {
		t.Fatal("no check_availability behavior on the executive")
	}

	transcript := arboreal.AppendToMessages(nil, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleAssistant, Content: "I recommend Solaris.",
	})
	transcript = arboreal.AppendToMessages(transcript, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Do you have The Dispossessed in stock?",
	})
	direction := arboreal.AppendToMessages(nil, llm.ChatCompletionMessage{
		Role: llm.ChatMessageRoleUser, Content: "Check whether The Dispossessed is in stock.",
	})[0]

	var got []string
	for _, m := range availability.Open(direction, transcript) {
		got = append(got, m.Content)
	}

	want := []string{"Do you have The Dispossessed in stock?", "Check whether The Dispossessed is in stock."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("check_availability opens with %v, want the customer's message then the direction %v", got, want)
	}
}
