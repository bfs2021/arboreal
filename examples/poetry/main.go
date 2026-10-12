// Package main is a learning-purposes example, NOT a template for real apps.
//
// It shows the TodoListExecutive doing what it exists for: choosing between
// several behaviors. The executive holds two behavior trees — one writes
// haikus, one writes sonnets — and an out-of-bounds handler for everything
// else. On every user message the executive's planner (an LLM call) picks
// which behaviors to run, from their names and descriptions.
//
// Try these, submitting each with a line containing only "$":
//
//   - "A haiku about autumn rain"         → one step, write_haiku (its
//     output is the reply as written; no summarizer runs)
//   - "A haiku and a sonnet about the sea" → two steps, run concurrently,
//     then merged into one reply by the executive's summarizer
//   - "What's the weather like?"          → an empty plan, so the
//     out-of-bounds handler answers
//
// If the planner invents a step name that is not one of the two behaviors,
// Plan panics with `No plan named ... found!`. That is a framework sharp
// edge, not a mistake in this file; run it again.
//
// The planner sees each behavior's name and description, plus the FIRST
// behavior's example (shown once as a sample direction) — never its states.
// So name and description are the whole interface between your trees and the
// planning LLM; write them the way you would write a tool description.
//
// Requires OPENAI_TOKEN to be set in the environment (the default model is
// gpt-4o-mini via the OpenAI provider). Stop the program with Ctrl-C. Ctrl-D
// does not end it: TerminalChannel reports end of input as an empty message
// and RunLoop keeps planning replies to it (each one a paid model call), so
// never pipe input into this program.
package main

import (
	"context"
	"log"

	"github.com/zenful-ai/arboreal"
)

func main() {
	// ANCHOR: behaviors
	haiku := arboreal.CreateBehaviorTree(
		"write_haiku",
		"Write a haiku (three lines, 5-7-5 syllables) on the topic the user asked for",
		"Write a haiku about autumn rain",
	)
	haikuState := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{
		System: "Respond only with a haiku that fits the user's request.",
	})
	haiku.AddState(&haikuState)

	sonnet := arboreal.CreateBehaviorTree(
		"write_sonnet",
		"Write a Shakespearean sonnet (fourteen rhymed lines) on the topic the user asked for",
		"Write a sonnet about the sea",
	)
	sonnetState := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{
		System: "Respond only with a Shakespearean sonnet that fits the user's request.",
	})
	sonnet.AddState(&sonnetState)
	// ANCHOR_END: behaviors

	// ANCHOR: executive
	exec := arboreal.CreateTodoListExecutive(
		"Poet",
		"Writes haikus and sonnets on request",
		&haiku, &sonnet,
	)

	// The Preamble is prepended to both the planner's prompt and the
	// summarizer's prompt. Two instructions:
	// keep the user's topic intact in each step's direction, and return an
	// empty plan for anything that is not a poetry request — an empty plan
	// is what routes a message to the out-of-bounds handler.
	exec.Preamble = "You are a poetry service. When writing a step's \"direction\", " +
		"restate the user's request faithfully, keeping the topic they asked for. " +
		"If the user did not ask for a haiku or a sonnet, return an empty JSON array: []"

	exec.OutOfBoundsHandler = arboreal.CannedResponseState(
		"Sorry, I only write haikus and sonnets.",
	)
	// ANCHOR_END: executive

	// ANCHOR: run
	if err := exec.RunLoop(context.Background(), arboreal.TerminalChannel{}); err != nil {
		log.Fatal(err)
	}
	// ANCHOR_END: run
}
