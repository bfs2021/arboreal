// Package main is a learning-purposes example, NOT a template for real apps.
//
// It runs the quickstart's executive for two turns WITHOUT RunLoop: each turn
// appends a user message to a transcript we own and calls exec.Call once.
// Call plans on the first turn (the plan is empty) and resumes on the second
// (the pause kept the step alive), which is exactly what RunLoop does inside
// its for-loop — minus the terminal channel.
//
// The transcript is ours to carry: Call works on the list it is given and
// never touches exec.History.
//
// Requires OPENAI_TOKEN to be set in the environment (the default model is
// gpt-4o-mini via the OpenAI provider).
package main

import (
	"context"
	"fmt"

	"github.com/zenful-ai/arboreal"
	"github.com/zenful-ai/arboreal/llm"
)

// ANCHOR: build
// buildExecutive is the quickstart, unchanged: one tree, chat then pause.
func buildExecutive() *arboreal.TodoListExecutive {
	chatBehavior := arboreal.CreateBehaviorTree(
		"chat_behavior",
		"A conversational bot",
		"<insert user's input here>",
	)

	chatState := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{})
	pauseState := arboreal.PauseState("Let user respond")
	chatBehavior.AddTransition(&chatState, &pauseState)

	return arboreal.CreateTodoListExecutive(
		"Chat Bot",
		"Simple chat bot",
		&chatBehavior,
	)
}

// ANCHOR_END: build

// ANCHOR: turn
// turn is one message's worth of work: append the user's words to the
// transcript, run the executive once, hand back the transcript with the
// reply appended. Call's second return value is nil whenever the turn
// produced a reply; a failed reply comes back as an *ErrorSignal with the
// transcript unchanged, which this demo lets stand.
func turn(ctx context.Context, exec *arboreal.TodoListExecutive,
	transcript arboreal.AnnotatedMessages, content string) arboreal.AnnotatedMessages {

	transcript = arboreal.AppendToMessages(transcript, llm.ChatCompletionMessage{
		Role:    llm.ChatMessageRoleUser,
		Content: content,
	})

	transcript, _ = exec.Call(ctx, transcript)
	return transcript
}

// ANCHOR_END: turn

// ANCHOR: main
func main() {
	exec := buildExecutive()
	ctx := context.Background()

	var transcript arboreal.AnnotatedMessages
	transcript = turn(ctx, exec, transcript, "Hi, I'm Paul. Please remember my name.")
	transcript = turn(ctx, exec, transcript, "What is my name?")

	for i, m := range transcript {
		fmt.Printf("[%d] %-9s %s\n", i, m.Role, m.Content)
	}
}

// ANCHOR_END: main
