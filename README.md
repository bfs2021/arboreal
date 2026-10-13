# Arboreal

Arboreal is a Go framework for building agentic AI systems using behavior trees and LLM integration. It provides a structured way to create complex AI workflows with planning, execution, and state management capabilities.

## Features

- **Behavior Trees**: Define complex AI behaviors using composable behavior trees
- **LLM Integration**: Built-in support for OpenAI, Anthropic, AWS Bedrock, and Ollama models
- **Planning & Execution**: TodoListExecutive for autonomous task planning and execution
- **State Management**: Persistent state handling with snapshots and memory
- **Vector Search**: SQLite-vec integration for semantic search and retrieval
- **Lua Scripting**: Extensible runtime with Lua scripting support
- **Annotation System**: Rich message annotation and templating

## Quick Start

### Installation

```bash
go get github.com/zenful-ai/arboreal
```

### Basic Example

```go
package main

import (
    "context"

    "github.com/zenful-ai/arboreal"
)

func main() {
    // Create a behavior tree for a chat bot
    chatBehavior := arboreal.CreateBehaviorTree(
        "chat_behavior",
        "A conversational bot",
        "<insert user's input here>",
    )

    // Define states
    chatState := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{})
    pauseState := arboreal.PauseState("Let user respond")

    // Add transitions
    chatBehavior.AddTransition(&chatState, &pauseState)

    // Create executive
    exec := arboreal.CreateTodoListExecutive(
        "Chat Bot",
        "Simple chat bot",
        &chatBehavior,
    )

    // Run the bot
    err := exec.RunLoop(context.Background(), arboreal.TerminalChannel{})
    if err != nil {
        panic(err)
    }
}
```

This example lives in [`examples/quickstart/`](examples/quickstart/) — run it with `go run ./examples/quickstart` (requires `OPENAI_TOKEN`).

## Core Concepts

### Behavior Trees

Behavior trees define the logic flow of your AI agent. They consist of states and transitions:

```go
behavior := arboreal.CreateBehaviorTree("name", "description", "example")

// Add states
state1 := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{
    System: "You are a helpful assistant",
})
state2 := arboreal.PauseState("Wait for user input")

// Connect states
behavior.AddTransition(&state1, &state2)
```

### TodoListExecutive

The TodoListExecutive provides autonomous planning and execution capabilities:

```go
exec := arboreal.CreateTodoListExecutive("Agent Name", "Description", behaviors...)
exec.MaxPlanDepth = 5  // Configure planning depth
exec.Preamble = "You are a helpful AI assistant"
```

### LLM Integration

Arboreal supports multiple LLM providers through environment variables:

```bash
export OPENAI_TOKEN=your_openai_key
export ANTHROPIC_TOKEN=your_anthropic_key
export OLLAMA_SERVICE_URL=http://localhost:11434
export AWS_REGION=us-east-1   # AWS Bedrock, plus credentials from the standard AWS chain
```

To use a model from the AWS Bedrock catalog, prefix its model ID (or inference profile ID) with `bedrock:`:

```go
arboreal.LLMCompletionOptions{
    Model: "bedrock:us.anthropic.claude-haiku-4-5-20251001-v1:0", // or llm.BedrockNovaLite
}
```

Chat goes through the Bedrock Converse API, so any catalog model that supports Converse works; tool use requires a model that supports tools in Converse. Embeddings support Amazon Titan text embedding models (default `llm.BedrockTitanEmbedV2`).

## Advanced Features

### Memory and Persistence

```go
// Save agent state
snapshot := arboreal.CreateSnapshot(behaviors...)
data := snapshot.Serialize()

// Restore agent state
snapshot, err := arboreal.DeserializeSnapshot(data)
```

### Vector Search and RAG

```go
// Semantic chunking and storage
chunks := arboreal.SemanticChunk(content, maxTokens)
// Store in vector database for retrieval
```

### Custom Channels

Implement custom communication channels:

```go
type CustomChannel struct {
    // Your implementation
}

func (c CustomChannel) Send(message string) error {
    // Send message through your channel
    return nil
}

func (c CustomChannel) Receive() (string, error) {
    // Receive message from your channel
    return "", nil
}
```

## Configuration

### Environment Variables

| Variable | Description | Required |
|----------|-------------|----------|
| `OPENAI_TOKEN` | OpenAI API key | For OpenAI models |
| `ANTHROPIC_TOKEN` | Anthropic API key | For Claude models |
| `OLLAMA_SERVICE_URL` | Ollama service URL | For local models |
| `AWS_REGION` / `AWS_PROFILE` | AWS region and credentials ([standard AWS configuration](https://docs.aws.amazon.com/sdkref/latest/guide/creds-config-files.html)) | For AWS Bedrock models |
| `TWILIO_ACCOUNT_SID` | Twilio account SID | For SMS channel |
| `TWILIO_AUTH_TOKEN` | Twilio auth token | For SMS channel |

### Model Configuration

```go
// Configure LLM options
options := arboreal.LLMCompletionOptions{
    Model:       llm.GPT4o,  // or llm.Claude3Sonnet, etc.
    Temperature: 0.7,
    MaxTokens:   1000,
    System:      "System prompt",
}
```

## Examples

The `examples/` directory contains various use cases:

- **Quick Start** (`examples/quickstart/`) - The chat bot from the [Quick Start](#quick-start) section above
- **One Shot** (`examples/oneshot/`) - Plan a todo list and execute it in a single pass, without a run loop
- **Snapshot Simple** (`examples/snapshot-simple/`) - Persist a conversation across separate process runs using snapshots
- **Little Spy** (`examples/little-spy/`) - Extract facts about the user into annotations, one run at a time, persisted with snapshots
- **Signals** (`examples/signals/`) - How each signal steers a behavior tree's traversal; runs without any API token
- **State Direct** (`examples/state-direct/`) - Call one `LLMCompletionState` by hand and inspect what it does to the history
- **Tree Loop** (`examples/tree-loop/`) - Drive a behavior tree with your own loop instead of an executive; shows pause/resume vs restart
- **Poetry** (`examples/poetry/`) - An executive choosing between two behaviors, with an out-of-bounds handler for everything else
- **One Turn** (`examples/one-turn/`) - Drive the executive one message at a time with `Call`, no run loop
- **Trace Turn** (`examples/trace-turn/`) - The same two turns with a `Trace` channel printing what happens inside
- **Snapshot Edges** (`examples/snapshot-edges/`) - When a snapshot records the executive and when it does not; runs without any API token
- **Bookshelf** (`examples/bookshelf/`) - The capstone: one process run per message, snapshots for a pause that outlives the process, an MCP catalog tool and a trace drain in one program
- **Tools** (`examples/tools/`) - One MCP tool served in-process over the in-memory transport, one state with `AllowTools` calling it
- **Bedrock** (`examples/bedrock/`) - One state answering through a model served by AWS Bedrock; requires AWS credentials instead of `OPENAI_TOKEN`
- **Tools Limited** (`examples/tools-limited/`) - An MCP server exposing three tools and a state that offers the model exactly one of them via `Tools`; the rest stay out of the model's sight
- **Chat Bot** (`examples/test/`) - Basic conversational agent
- **CRM Assistant** (`examples/crm/`) - Customer relationship management

Run any of them with `go run ./examples/<name>` (most require `OPENAI_TOKEN` to be set).

## Contributing

We welcome contributions! Please see our [Contributing Guidelines](CONTRIBUTING.md) for details.

### Project Structure

```
├── README.md           # This file
├── go.mod             # Go module definition
├── *.go               # Core framework files
├── llm/               # LLM provider integrations
├── examples/          # Example applications
├── engine/            # Lua scripting engine (see engine/README.md)
└── util/              # Utility functions
```

## License

This project is licensed under the BSD License - see the [LICENSE](LICENSE) file for details.

## Roadmap

- [ ] Vector store re-implementation
- [ ] More LLM provider integrations
- [ ] Enhanced debugging tools
- [ ] Better documentation
- [ ] Enhanced testing coverage
- [ ] Performance optimizations

## Support

- **Issues**: [GitHub Issues](https://github.com/zenful-ai/arboreal/issues)
- **Discussions**: [GitHub Discussions](https://github.com/zenful-ai/arboreal/discussions)
