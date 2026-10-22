package arboreal

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/zenful-ai/arboreal/llm"
)

func TestMonotonicIdGenerator(t *testing.T) {
	t.Run("generates monotonic IDs", func(t *testing.T) {
		generator := MonotonicIdGenerator("test-")

		id1 := generator()
		id2 := generator()
		id3 := generator()

		expected := []string{"test-1", "test-2", "test-3"}
		actual := []string{id1, id2, id3}

		for i, expected := range expected {
			if actual[i] != expected {
				t.Errorf("Expected ID %q, got %q", expected, actual[i])
			}
		}
	})

	t.Run("different generators are independent", func(t *testing.T) {
		gen1 := MonotonicIdGenerator("gen1-")
		gen2 := MonotonicIdGenerator("gen2-")

		id1a := gen1()
		id2a := gen2()
		id1b := gen1()
		id2b := gen2()

		if id1a != "gen1-1" {
			t.Errorf("Expected gen1-1, got %s", id1a)
		}
		if id2a != "gen2-1" {
			t.Errorf("Expected gen2-1, got %s", id2a)
		}
		if id1b != "gen1-2" {
			t.Errorf("Expected gen1-2, got %s", id1b)
		}
		if id2b != "gen2-2" {
			t.Errorf("Expected gen2-2, got %s", id2b)
		}
	})

	t.Run("concurrent access is safe", func(t *testing.T) {
		generator := MonotonicIdGenerator("concurrent-")
		var wg sync.WaitGroup
		const numGoroutines = 100
		const idsPerGoroutine = 10

		results := make([]string, numGoroutines*idsPerGoroutine)

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(goroutineIndex int) {
				defer wg.Done()
				for j := 0; j < idsPerGoroutine; j++ {
					id := generator()
					results[goroutineIndex*idsPerGoroutine+j] = id
				}
			}(i)
		}

		wg.Wait()

		// Check that all IDs are unique
		idSet := make(map[string]bool)
		for _, id := range results {
			if idSet[id] {
				t.Errorf("Duplicate ID found: %s", id)
			}
			idSet[id] = true

			// Check format
			if !strings.HasPrefix(id, "concurrent-") {
				t.Errorf("ID %s doesn't have expected prefix", id)
			}
		}

		if len(idSet) != numGoroutines*idsPerGoroutine {
			t.Errorf("Expected %d unique IDs, got %d", numGoroutines*idsPerGoroutine, len(idSet))
		}
	})

	t.Run("empty prefix works", func(t *testing.T) {
		generator := MonotonicIdGenerator("")

		id1 := generator()
		id2 := generator()

		if id1 != "1" {
			t.Errorf("Expected '1', got %s", id1)
		}
		if id2 != "2" {
			t.Errorf("Expected '2', got %s", id2)
		}
	})

	t.Run("special character prefix", func(t *testing.T) {
		generator := MonotonicIdGenerator("@#$-")

		id := generator()
		expected := "@#$-1"

		if id != expected {
			t.Errorf("Expected %q, got %q", expected, id)
		}
	})
}

func TestGenerateStringIdentifier(t *testing.T) {
	t.Run("generates string with correct prefix and length", func(t *testing.T) {
		prefix := "test-"
		length := 16

		id, err := GenerateStringIdentifier(prefix, length)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(id) != length {
			t.Errorf("Expected length %d, got %d", length, len(id))
		}

		if !strings.HasPrefix(id, prefix) {
			t.Errorf("Expected prefix %q, but ID %q doesn't have it", prefix, id)
		}
	})

	t.Run("generates unique IDs", func(t *testing.T) {
		prefix := "unique-"
		length := 20
		numIDs := 100

		ids := make([]string, numIDs)
		for i := 0; i < numIDs; i++ {
			id, err := GenerateStringIdentifier(prefix, length)
			if err != nil {
				t.Fatalf("Error generating ID %d: %v", i, err)
			}
			ids[i] = id
		}

		// Check uniqueness
		idSet := make(map[string]bool)
		for _, id := range ids {
			if idSet[id] {
				t.Errorf("Duplicate ID found: %s", id)
			}
			idSet[id] = true
		}
	})

	t.Run("uses valid base32 characters", func(t *testing.T) {
		prefix := "b32-"
		length := 15

		id, err := GenerateStringIdentifier(prefix, length)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Remove prefix and check remaining characters
		withoutPrefix := id[len(prefix):]
		validPattern := regexp.MustCompile(`^[0-9a-v]+$`) // base32 uses 0-9 and a-v

		if !validPattern.MatchString(withoutPrefix) {
			t.Errorf("ID contains invalid base32 characters: %q", withoutPrefix)
		}
	})

	t.Run("deterministic mode with ZEN_SEED_RNG", func(t *testing.T) {
		// Set the environment variable
		originalEnv := os.Getenv("ZEN_SEED_RNG")
		defer func() {
			if originalEnv == "" {
				os.Unsetenv("ZEN_SEED_RNG")
			} else {
				os.Setenv("ZEN_SEED_RNG", originalEnv)
			}
		}()

		os.Setenv("ZEN_SEED_RNG", "12345")

		// Reset the once to allow re-seeding for test
		seedRNGOnce = sync.Once{}

		prefix := "det-"
		length := 12

		// Generate the same ID multiple times
		id1, err1 := GenerateStringIdentifier(prefix, length)
		if err1 != nil {
			t.Fatalf("Error generating first ID: %v", err1)
		}

		// Reset the once again
		seedRNGOnce = sync.Once{}

		id2, err2 := GenerateStringIdentifier(prefix, length)
		if err2 != nil {
			t.Fatalf("Error generating second ID: %v", err2)
		}

		if id1 != id2 {
			t.Errorf("Expected deterministic IDs to be equal, got %q and %q", id1, id2)
		}
	})

	t.Run("empty prefix", func(t *testing.T) {
		length := 10

		id, err := GenerateStringIdentifier("", length)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(id) != length {
			t.Errorf("Expected length %d, got %d", length, len(id))
		}
	})

	t.Run("minimum length", func(t *testing.T) {
		prefix := "x"
		length := 1

		id, err := GenerateStringIdentifier(prefix, length)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(id) != length {
			t.Errorf("Expected length %d, got %d", length, len(id))
		}

		if id != prefix {
			t.Errorf("Expected %q, got %q", prefix, id)
		}
	})

	t.Run("longer than prefix", func(t *testing.T) {
		prefix := "very-long-prefix-"
		length := 32

		id, err := GenerateStringIdentifier(prefix, length)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(id) != length {
			t.Errorf("Expected length %d, got %d", length, len(id))
		}

		if !strings.HasPrefix(id, prefix) {
			t.Errorf("Expected prefix %q, but ID %q doesn't have it", prefix, id)
		}

		// Check that there's some random content after the prefix
		withoutPrefix := id[len(prefix):]
		if len(withoutPrefix) == 0 {
			t.Error("Expected some content after prefix")
		}
	})
}

func TestGenerateStringIdentifierEdgeCases(t *testing.T) {
	t.Run("invalid ZEN_SEED_RNG value", func(t *testing.T) {
		originalEnv := os.Getenv("ZEN_SEED_RNG")
		defer func() {
			if originalEnv == "" {
				os.Unsetenv("ZEN_SEED_RNG")
			} else {
				os.Setenv("ZEN_SEED_RNG", originalEnv)
			}
		}()

		os.Setenv("ZEN_SEED_RNG", "not-a-number")

		// Reset the once to allow re-processing
		seedRNGOnce = sync.Once{}

		// Should still work (falls back to secure random)
		id, err := GenerateStringIdentifier("test-", 10)

		if err != nil {
			t.Fatalf("Should handle invalid ZEN_SEED_RNG gracefully: %v", err)
		}

		if len(id) != 10 {
			t.Errorf("Expected length 10, got %d", len(id))
		}
	})
}

// Benchmark tests
func BenchmarkMonotonicIdGenerator(b *testing.B) {
	generator := MonotonicIdGenerator("bench-")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		generator()
	}
}

func BenchmarkGenerateStringIdentifier(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateStringIdentifier("bench-", 16)
	}
}

func BenchmarkGenerateStringIdentifierDeterministic(b *testing.B) {
	os.Setenv("ZEN_SEED_RNG", "12345")
	defer os.Unsetenv("ZEN_SEED_RNG")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seedRNGOnce = sync.Once{} // Reset for each iteration in benchmark
		GenerateStringIdentifier("bench-", 16)
	}
}

func BenchmarkMonotonicIdGeneratorConcurrent(b *testing.B) {
	generator := MonotonicIdGenerator("concurrent-")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			generator()
		}
	})
}

// probeHistory is the minimal history the LLM states accept.
var probeHistory = AnnotatedMessages{
	{ChatCompletionMessage: llm.ChatCompletionMessage{Role: llm.ChatMessageRoleUser, Content: "hello"}},
}

// TestLLMCompletionStateResolvesModelFromContext pins that a default set
// with WithDefaultModel reaches the provider constructor on both of
// LLMCompletionState's paths, and that it is resolved BEFORE the provider is
// built. A "cluster:" URI parses as a known provider type but has no
// constructor case, so CreateModelProvider fails deterministically with no
// network and no credentials; that failure is the assertion. Before the
// resolve-first change, both paths built an OpenAI provider from the empty
// Model and ignored the context entirely.
func TestLLMCompletionStateResolvesModelFromContext(t *testing.T) {
	const clusterErr = "unknown model type: cluster"

	for _, tc := range []struct {
		name    string
		options LLMCompletionOptions
	}{
		{name: "main path", options: LLMCompletionOptions{}},
		{name: "annotation path", options: LLMCompletionOptions{Annotation: "probe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t)
			t.Setenv("OPENAI_TOKEN", "")
			ctx := WithDefaultModel(context.Background(), "cluster:ctx-probe")
			_, sig := LLMCompletionState(tc.options).Lambda(ctx, probeHistory)
			errSig, ok := sig.(*ErrorSignal)
			if !ok {
				t.Fatalf("signal = %T (%v), want *ErrorSignal", sig, sig)
			}
			if !strings.Contains(errSig.ErrorMessage, clusterErr) {
				t.Fatalf("error = %q, want it to contain %q (context default did not reach the provider constructor)", errSig.ErrorMessage, clusterErr)
			}
		})
	}
}

// TestLLMCompletionStateDoesNotPinModelAcrossCalls pins that a state built
// without a Model resolves afresh on every invocation. The Lambda closes over
// its options; writing the resolved URI back into them would make the first
// caller's context default permanent for that state and for every Copy of it.
func TestLLMCompletionStateDoesNotPinModelAcrossCalls(t *testing.T) {
	captureLog(t)
	t.Setenv("ANTHROPIC_TOKEN", "")
	state := LLMCompletionState(LLMCompletionOptions{})

	_, first := state.Lambda(WithDefaultModel(context.Background(), "cluster:first"), probeHistory)
	if sig, ok := first.(*ErrorSignal); !ok || !strings.Contains(sig.ErrorMessage, "unknown model type: cluster") {
		t.Fatalf("first call: signal = %v, want the cluster error", first)
	}

	const anthropicErr = "ANTHROPIC_TOKEN environment variable not set"
	_, second := state.Lambda(WithDefaultModel(context.Background(), "anthropic:second"), probeHistory)
	sig, ok := second.(*ErrorSignal)
	if !ok {
		t.Fatalf("second call: signal = %T (%v), want *ErrorSignal", second, second)
	}
	if !strings.Contains(sig.ErrorMessage, anthropicErr) {
		t.Fatalf("second call: error = %q, want it to contain %q (state was pinned to the first call's model)", sig.ErrorMessage, anthropicErr)
	}
}

func TestAnnotationFromReplyNeverMarksAPinToCross(t *testing.T) {
	// A model's JSON decodes into the whole Annotation, "cross" included.
	// Annotation mode has no switch for crossing, so whatever the reply
	// says, the pin is stored unmarked; a prompt-injected "cross" cannot
	// put the value on the transcript.
	got := annotationFromReply("verdict", `{"name":"verdict","data":"resolved","cross":{"enabled":true}}`)
	if got.Data != "resolved" || got.Name != "verdict" {
		t.Fatalf("decoded %+v, want the reply's name and data", got)
	}
	if got.Cross.Enabled || got.Cross.Merge != nil {
		t.Fatalf("decoded %+v, want Cross zero: the model does not mark pins", got)
	}

	// A reply that is not JSON, or JSON without data, is the Data itself.
	for _, content := range []string{"just words", `{"name":"verdict"}`} {
		got := annotationFromReply("verdict", content)
		if got.Name != "verdict" || got.Data != content || got.Cross.Enabled {
			t.Fatalf("decoded %q as %+v, want it as the Data of an unmarked verdict", content, got)
		}
	}
}
