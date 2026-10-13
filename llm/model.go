package llm

import (
	"fmt"
	"strings"
)

// LLM-related conversational models

const (
	// Providers
	ProviderUnknown   = "unknown"
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderOllama    = "ollama"
	ProviderBedrock   = "bedrock"
	ProviderCluster   = "cluster"

	// Models
	ModelGPT4oMini        = "gpt-4o-mini-2024-07-18"
	ModelGPT41Nano        = "gpt-4.1-nano-2025-04-14"
	ModelClaudeSonnet4    = "claude-sonnet-4-20250514"
	ModelClaudeHaiku35    = "claude-3-5-haiku-20241022"
	ModelChatModelLlama8b = "llama3.1:8b-instruct-q8_0"

	// Bedrock model IDs. Some models are only invocable through a
	// cross-region inference profile, whose ID carries a geography prefix
	// (e.g. "us.anthropic.claude-...").
	ModelBedrockNovaLite     = "amazon.nova-lite-v1:0"
	ModelBedrockTitanEmbedV2 = "amazon.titan-embed-text-v2:0"

	// Prebuilt URIs
	GPT4oMini           = ProviderOpenAI + ":" + ModelGPT4oMini
	GPT41Nano           = ProviderOpenAI + ":" + ModelGPT41Nano
	ClaudeSonnet4       = ProviderAnthropic + ":" + ModelClaudeSonnet4
	ClaudeSonnet        = ClaudeSonnet4
	ClaudeHaiku35       = ProviderAnthropic + ":" + ModelClaudeHaiku35
	ClaudeHaiku         = ClaudeHaiku35
	BedrockNovaLite     = ProviderBedrock + ":" + ModelBedrockNovaLite
	BedrockTitanEmbedV2 = ProviderBedrock + ":" + ModelBedrockTitanEmbedV2

	// Custom Models
	ChatModelScribeV1 = "scribe-v1"
)

type Model struct {
	Name    string `json:"name"`
	URI     string `json:"uri"`
	Default *bool  `json:"default,omitempty"`
}

type ModelURI struct {
	Type string
	Name string
}

func (u *ModelURI) String() string {
	return fmt.Sprintf("%s:%s", u.Type, u.Name)
}

func ParseModelURI(uri string) *ModelURI {
	m := ModelURI{
		Type: ProviderUnknown,
		Name: uri,
	}

	parts := strings.Split(uri, ":")
	if len(parts) < 2 {
		return &m
	}

	switch parts[0] {
	case ProviderOpenAI, ProviderAnthropic, ProviderOllama, ProviderBedrock, ProviderCluster:
		m.Type = parts[0]
	default:
		return &m
	}

	m.Name = strings.Join(parts[1:], ":")

	return &m
}

func SupportedModels() []Model {
	d := true
	return []Model{
		{
			Name:    "GPT 4o Mini",
			URI:     GPT4oMini,
			Default: &d,
		},
		{
			Name: "GPT 4.1 Nano",
			URI:  GPT41Nano,
		},
		{
			Name: "Claude Haiku 3.5",
			URI:  ClaudeHaiku35,
		},
		{
			Name: "Claude Sonnet 4.0",
			URI:  ClaudeSonnet4,
		},
	}
}
