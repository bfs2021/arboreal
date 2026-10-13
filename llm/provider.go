package llm

import (
	"context"
	"fmt"
)

type ModelProvider interface {
	CreateChatCompletion(context.Context, *ChatCompletionRequest) (*ChatCompletionResponse, error)
	CreateEmbedding(context.Context, *EmbeddingRequest) (Embedding, error)
}

func CreateModelProvider(uri, defaultType string) (ModelProvider, error) {
	modelURI := ParseModelURI(uri)
	if modelURI.Type == ProviderUnknown {
		modelURI.Type = defaultType
	}

	switch modelURI.Type {
	case ProviderOpenAI:
		return &OpenAIService{}, nil
	case ProviderAnthropic:
		return newAnthropicService()
	case ProviderOllama:
		return &OllamaService{}, nil
	case ProviderBedrock:
		return newBedrockService()
	default:
		return nil, fmt.Errorf("unknown model type: %s", modelURI.Type)
	}
}
