// Package main is a learning-purposes example, NOT a template for real apps.
//
// One LLMCompletionState with AllowTools set, one MCP tool served from
// inside the same process over an in-memory transport — no network beyond
// the model call. The state offers the mux's tools to the model; when the
// reply is a tool call, the state executes it, appends the call and its
// result to the history, and asks again, until a reply has no tool calls.
//
// The final dump shows the whole exchange, tool messages included.
//
// Requires OPENAI_TOKEN to be set in the environment (the default model is
// gpt-4o-mini via the OpenAI provider). Set BEDROCK_MODEL to a Bedrock model
// or inference profile ID to run against AWS Bedrock instead (see
// examples/bedrock for the AWS setup).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zenful-ai/arboreal"
	"github.com/zenful-ai/arboreal/llm"
)

// ANCHOR: server
// getCurrentTimestamp is an ordinary Go function; wrapping it in an MCP
// server is what makes it a tool — Arboreal has no other tool mechanism.
func getCurrentTimestamp(ctx context.Context, req *mcp.CallToolRequest,
	_ any) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: time.Now().Format(time.RFC3339)}},
	}, nil, nil
}

// ANCHOR_END: server

func main() {
	// ANCHOR: wiring
	server := mcp.NewServer(&mcp.Implementation{Name: "ai.zenful", Version: "v1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ai.zenful/get_current_timestamp",
		Description: "Get the current timestamp",
	}, getCurrentTimestamp)

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
	defer mux.Close()

	// The mux travels in the context; a state only sees tools if BOTH are
	// true: the mux is in the context AND the state sets AllowTools.
	ctx := arboreal.WithMCPClient(context.Background(), mux)
	// ANCHOR_END: wiring

	// An empty model means the default (gpt-4o-mini via OpenAI).
	var model string
	if m := os.Getenv("BEDROCK_MODEL"); m != "" {
		model = llm.ProviderBedrock + ":" + m
	}

	// ANCHOR: call
	state := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{
		System:     "Answer using the tools available to you.",
		Model:      model,
		AllowTools: true,
	})

	history := arboreal.AppendToMessages(nil, llm.ChatCompletionMessage{
		Role:    llm.ChatMessageRoleUser,
		Content: "What is the exact current timestamp?",
	})

	history, sig := state.Call(ctx, history)
	if err, ok := sig.(*arboreal.ErrorSignal); ok {
		log.Fatal(err.Description())
	}

	for i, m := range history {
		fmt.Printf("[%d] %-9s %q\n", i, m.Role, m.Content)
	}
	// ANCHOR_END: call
}
