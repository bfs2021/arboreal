// Package main is a learning-purposes example, NOT a template for real apps.
//
// It shows the smallest complete pass through Arboreal's planning machinery: a
// TodoListExecutive that plans a todo list for a single request and executes
// that plan exactly once.
//
// This is the "one-shot" counterpart to the executive's RunLoop. RunLoop sits
// on a channel and, for every incoming message, plans and executes over and
// over — a conversation. Here we skip the loop: Call on the executive performs
// one Plan (the LLM turns the request into a todo list of named behaviors) and
// one Execute (each planned step runs; a lone step's reply is the answer as
// written, and two or more are summarized into one). Prompt in, plan, answer
// out, done.
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

func main() {
	// A behavior the planner can put on its todo list. The name, description,
	// and example are what the planner LLM sees when deciding which behaviors
	// accomplish the user's request.
	answerBehavior := arboreal.CreateBehaviorTree(
		"answer_question",
		"Answers a general-knowledge question from the user",
		"What is the capital of France?",
	)

	// The tree holds a single LLM-backed state — no pause, no follow-up
	// states, because nothing loops back to the user in a one-shot run.
	answerState := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{})
	answerBehavior.AddState(&answerState)

	// The executive is the planner: given a request, it drafts a todo list
	// choosing from the behaviors it was constructed with (here just one).
	exec := arboreal.CreateTodoListExecutive(
		"Oneshot",
		"Plans a todo list for a single request and executes it once",
		&answerBehavior,
	)

	// Build a one-message conversation: just the user's question.
	history := arboreal.AppendToMessages(nil, llm.ChatCompletionMessage{
		Role:    llm.ChatMessageRoleUser,
		Content: "What is the capital of France?",
	})

	// One shot: Call plans the todo list, executes every planned step, and
	// appends the answer to the history as an assistant message.
	// For an ongoing conversation you would use exec.RunLoop with a Channel
	// instead — it repeats exactly this plan/execute pass per user message.
	result, _ := exec.Call(context.Background(), history)

	fmt.Println(result.LastMessage().Content)
}
