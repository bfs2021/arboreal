// Package main is a learning-purposes example — the book's capstone: the
// generic skeleton of an agent that handles one message per process run.
//
//	$ go run ./examples/bookshelf "Hi! Can you recommend a sci-fi novel?"
//
// Each run is a full cold start:
//
//  1. Load the transcript file (the conversation we own — Chapter 9).
//  2. Build the executive with the same stable ids as every run (Chapter 10).
//  3. If a snapshot file exists, restore it — the agent asked a question
//     last run and the plan is still in flight.
//  4. Append the incoming message, run one Call with a trace drain attached
//     (Chapter 11) and an MCP catalog tool in the context (Chapter 12).
//  5. Persist: snapshot written if a question is pending, deleted if the
//     turn finished; the transcript always written.
//
// The snapshot file therefore exists exactly when the agent is waiting for
// an answer. Delete both bookshelf-*.json files (they land in the
// directory you run from) to start a fresh conversation.
//
// Requires OPENAI_TOKEN to be set in the environment (the default model is
// gpt-4o-mini via the OpenAI provider). The trace goes to stderr, the
// conversation to stdout.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zenful-ai/arboreal"
	"github.com/zenful-ai/arboreal/llm"
)

const (
	snapshotFile   = "bookshelf-snapshot.json"
	transcriptFile = "bookshelf-transcript.json"
)

// The hold tree's fixed lines, shared with main_test.go.
const (
	holdQuestion = "I can put that title on hold at the counter for three days. Should I go ahead? (yes/no)"
	holdPlaced   = "Done — the book is on hold at the counter for three days."
	holdSkipped  = "No problem — I left the book on the shelf."
)

// ANCHOR: tool
// The catalog is data; lookupBook is an ordinary Go function. Serving it
// over MCP is what makes it a tool (Chapter 12).
type book struct {
	Title  string
	Author string
	Price  string
	Stock  int
}

var catalog = []book{
	{"The Dispossessed", "Ursula K. Le Guin", "12.00", 2},
	{"The Left Hand of Darkness", "Ursula K. Le Guin", "11.50", 0},
	{"A Wizard of Earthsea", "Ursula K. Le Guin", "9.99", 5},
	{"Solaris", "Stanisław Lem", "10.50", 1},
	{"The Master and Margarita", "Mikhail Bulgakov", "14.00", 3},
}

type bookQuery struct {
	Title string `json:"title"`
}

func lookupBook(ctx context.Context, req *mcp.CallToolRequest,
	args bookQuery) (*mcp.CallToolResult, any, error) {
	q := strings.ToLower(strings.TrimSpace(args.Title))
	var lines []string
	if q != "" {
		for _, b := range catalog {
			if !strings.Contains(strings.ToLower(b.Title+" "+b.Author), q) {
				continue
			}
			line := fmt.Sprintf("%s by %s: out of stock", b.Title, b.Author)
			if b.Stock > 0 {
				line = fmt.Sprintf("%s by %s: %d in stock at $%s", b.Title, b.Author, b.Stock, b.Price)
			}
			lines = append(lines, line)
		}
	}
	text := "No such title in the catalog."
	if len(lines) > 0 {
		text = strings.Join(lines, "\n")
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}, nil, nil
}

// ANCHOR_END: tool

// ANCHOR: mcp
// startMCP serves the catalog tool from inside this process over the
// in-memory transport and returns a connected mux (Chapter 12).
func startMCP() *arboreal.MCPClientMux {
	server := mcp.NewServer(&mcp.Implementation{Name: "bookshelf", Version: "v1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "bookshelf/lookup_book",
		Description: "Look up a title or author in the store catalog: stock and price",
	}, lookupBook)

	serverSide, clientSide := mcp.NewInMemoryTransports()
	go func() {
		if err := server.Run(context.Background(), serverSide); err != nil {
			log.Fatal(err)
		}
	}()

	mux := arboreal.NewMCPClientMux()
	if err := mux.AddInMemoryServer(context.Background(), clientSide); err != nil {
		log.Fatal(err)
	}
	return mux
}

// ANCHOR_END: mcp

// ANCHOR: hold
// buildHoldTree is the one behavior that pauses — and it is model-free: a
// canned question, a pause that outlives the process, and a lambda that
// reads the customer's answer. The question and the reply after the resume
// reach the customer as written whenever place_hold is the turn's only step:
// the resumed turn always is, since only the paused step is kept, and the
// first turn is whenever the planner picks the hold alone (Chapter 8).
// Model-free is what lets main_test.go drive the whole pause round trip
// without a token.
func buildHoldTree() arboreal.BehaviorTree {
	hold := arboreal.CreateBehaviorTreeWithId(
		"place_hold",
		"Put a book on hold at the counter so the customer can pick it up",
		"Please put The Left Hand of Darkness on hold for me",
		"tree-hold",
	)

	askConfirm := arboreal.CannedResponseState(holdQuestion)
	askConfirm.HashId = "state-hold-ask"
	askConfirm.StateName = "ask-confirm"

	waitConfirm := arboreal.PauseState("Wait for the customer's confirmation")
	waitConfirm.HashId = "state-hold-wait"
	waitConfirm.StateName = "wait-confirm"

	placeHold := arboreal.BehaviorState{
		HashId:    "state-hold-place",
		StateName: "place-hold",
		Lambda: func(ctx context.Context, messages arboreal.AnnotatedMessages) (arboreal.AnnotatedMessages, arboreal.Signal) {
			// First decisive word wins — and words, not substrings: "no, don't
			// book it" must not place the hold just because "book" contains "ok".
			answer := strings.ToLower(messages.LastMessage().Content)
			reply := holdSkipped
		scan:
			for _, w := range strings.FieldsFunc(answer, func(r rune) bool { return !unicode.IsLetter(r) }) {
				switch w {
				case "no", "not", "nope", "don", "never":
					break scan
				case "yes", "yeah", "yep", "sure", "ok", "okay", "ahead":
					reply = holdPlaced
					break scan
				}
			}
			return arboreal.AppendToMessages(messages, llm.ChatCompletionMessage{
				Role:    llm.ChatMessageRoleAssistant,
				Content: reply,
			}), nil
		},
	}

	hold.AddTransition(askConfirm, &waitConfirm)
	hold.AddTransition(&waitConfirm, &placeHold)
	return hold
}

// ANCHOR_END: hold

// ANCHOR: build
// buildExecutive constructs the same executive on every run — stable ids
// throughout, because a restored snapshot matches behaviors by hash
// (Chapter 10). Note which trees pause: only place_hold. A tree that ends
// in a pause keeps its step alive, so every later message resumes it and
// the planner never chooses again; recommend and check_availability
// complete instead, and continuity between finished turns rides the
// transcript we pass to Call.
func buildExecutive() *arboreal.TodoListExecutive {
	recommend := arboreal.CreateBehaviorTreeWithId(
		"recommend_book",
		"Recommend one book from the store, based on what the customer says they like",
		"Can you recommend a good science-fiction novel?",
		"tree-recommend",
	)
	extractGenre := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{
		Id:   "state-extract-genre",
		Name: "extract-genre",
		System: "Extract the book genre the customer is interested in from their message, " +
			"if it reveals one. Only respond in valid JSON with the following fields:\n" +
			"\tdata - a string containing the genre, or an empty string if the message does not reveal it\n\n" +
			"Example:\n\n{\"data\": \"science fiction\"}\n\n" +
			"Example when the message does not reveal it:\n\n{\"data\": \"\"}",
		Annotation: "genre",
	})
	recommendState := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{
		Id:   "state-recommend",
		Name: "recommend",
		System: "You are the assistant of a small bookstore. Recommend exactly one book " +
			"that fits the customer's request, with one sentence on why. " +
			"Some user-role messages are third-person notes from a planning system " +
			"describing what the customer said; treat them as the customer's own words.",
		ExtraContext: []string{"genre"},
	})
	recommend.AddTransition(&extractGenre, &recommendState)

	availability := arboreal.CreateBehaviorTreeWithId(
		"check_availability",
		"Check whether the store has a given title or author in stock, using the catalog lookup tool",
		"Do you have The Dispossessed in stock?",
		"tree-availability",
	)
	lookupState := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{
		Id:   "state-lookup",
		Name: "lookup",
		System: "You are the assistant of a small bookstore. The customer wants to know " +
			"whether a title or author is available. Use the catalog lookup tool to check, then " +
			"answer from the tool's result only — never guess stock or prices. " +
			"Some user-role messages are third-person notes from a planning system " +
			"describing what the customer said; treat them as the customer's own words.",
		AllowTools: true,
		Tools:      []string{"bookshelf/lookup_book"},
	})
	availability.AddState(&lookupState)

	hold := buildHoldTree()

	exec := arboreal.CreateTodoListExecutiveWithId(
		"Bookshelf",
		"The assistant of a small bookstore",
		"exec-bookshelf",
		&recommend, &availability, &hold,
	)

	// Faithful directions (Chapter 8): keep the customer's words intact in
	// the planner's "direction", and route anything else out of bounds.
	exec.Preamble = "You are the assistant of a small bookstore. When writing the " +
		"\"direction\" for a step, restate the customer's message faithfully in the " +
		"third person, quoting it, e.g.: The customer said: \"Do you have The " +
		"Dispossessed in stock?\". Never rephrase the customer's words as if they " +
		"were addressed to the customer. If the message fits none of the behaviors, " +
		"return an empty JSON array: []. Never include a Re-plan step in the plan."

	outOfBounds := arboreal.CannedResponseState(
		"I can recommend a book, check whether we have one in stock, or put one on hold.")
	outOfBounds.HashId = "state-out-of-bounds"
	exec.OutOfBoundsHandler = outOfBounds

	return exec
}

// ANCHOR_END: build

// ANCHOR: restore
// loadTranscript reads the conversation we own; restoreSnapshot rehydrates
// a pending question, if the last run left one. A missing file is a fresh
// start, not an error.
func loadTranscript() arboreal.AnnotatedMessages {
	data, err := os.ReadFile(transcriptFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		panic(err)
	}
	var transcript arboreal.AnnotatedMessages
	if err := json.Unmarshal(data, &transcript); err != nil {
		panic(err)
	}
	return transcript
}

func restoreSnapshot(exec *arboreal.TodoListExecutive) {
	data, err := os.ReadFile(snapshotFile)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		panic(err)
	}
	var snap arboreal.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		panic(err)
	}
	if err := snap.Restore(exec); err != nil {
		panic(err)
	}
	fmt.Fprintln(os.Stderr, "(a question is pending — resuming the paused task)")
}

// ANCHOR_END: restore

// ANCHOR: persist
// persist is the end of every run. An empty snapshot means the turn
// finished — Chapter 10: only a pause keeps a step alive — so the snapshot
// file is deleted rather than written; a stale one would resurrect a
// finished task. The transcript, the system of record, is written first —
// nothing later in the run may lose the customer's message.
func persist(exec *arboreal.TodoListExecutive, transcript arboreal.AnnotatedMessages) {
	out, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(transcriptFile, out, 0o644); err != nil {
		panic(err)
	}

	snap, err := arboreal.TakeSnapshot(exec)
	if err != nil {
		panic(err)
	}

	if len(snap) == 0 {
		if err := os.Remove(snapshotFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			panic(err)
		}
	} else {
		out, err := json.MarshalIndent(snap, "", "  ")
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(snapshotFile, out, 0o644); err != nil {
			panic(err)
		}
	}
}

// ANCHOR_END: persist

// ANCHOR: drain
// startDrain reads the trace until the channel is closed — the channel is
// unbuffered, so the drain must be running before the first Call
// (Chapter 11). The narration goes to stderr; stdout is the conversation.
func startDrain(trace arboreal.Trace) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for msg := range trace {
			line := fmt.Sprintf("%-10s %-16s %s", msg.Type, msg.Name, msg.Message)
			if msg.Signal != nil {
				line += fmt.Sprintf("  [signal: %s %q]", msg.Signal.Type, msg.Signal.Reason)
			}
			fmt.Fprintln(os.Stderr, line)
		}
	}()
	return done
}

// ANCHOR_END: drain

// ANCHOR: lifecycle
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, `usage: bookshelf "<message>"`)
		os.Exit(2)
	}
	incoming := os.Args[1]

	transcript := loadTranscript() // the conversation is ours (Chapter 9)
	exec := buildExecutive()       // same ids on every run (Chapter 10)
	restoreSnapshot(exec)          // a question may be pending

	transcript = arboreal.AppendToMessages(transcript, llm.ChatCompletionMessage{
		Role:    llm.ChatMessageRoleUser,
		Content: incoming,
	})

	trace := make(arboreal.Trace)
	done := startDrain(trace)
	ctx := context.WithValue(context.Background(), "arboreal_trace", trace)

	mux := startMCP()
	defer mux.Close()
	ctx = arboreal.WithMCPClient(ctx, mux)

	transcript, _ = exec.Call(ctx, transcript)
	close(trace)
	<-done

	// Call never touches History; TakeSnapshot reads it (Chapter 9) — this
	// keeps the snapshot's history field the real transcript, not a stale one.
	exec.History = transcript
	persist(exec, transcript)

	fmt.Printf("\nBookshelf: %s\n", transcript.LastMessage().Content)
}

// ANCHOR_END: lifecycle
