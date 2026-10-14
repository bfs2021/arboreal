package arboreal

import (
	"reflect"
	"testing"

	"github.com/zenful-ai/arboreal/llm"
)

// transcript builds a message list from role, content pairs.
func transcript(pairs ...string) AnnotatedMessages {
	var m AnnotatedMessages
	for i := 0; i+1 < len(pairs); i += 2 {
		m = AppendToMessages(m, llm.ChatCompletionMessage{Role: pairs[i], Content: pairs[i+1]})
	}
	return m
}

// contents is the Content of every message, for comparing selections.
func contents(m AnnotatedMessages) []string {
	var out []string
	for _, msg := range m {
		out = append(out, msg.Content)
	}
	return out
}

func TestTrailingUserMessages(t *testing.T) {
	cases := []struct {
		name string
		in   AnnotatedMessages
		want []string
	}{
		{"empty transcript", nil, nil},
		{"one user message", transcript("user", "a"), []string{"a"}},
		{"two user messages and no assistant", transcript("user", "a", "user", "b"), []string{"a", "b"}},
		{"two user messages since the agent spoke", transcript("user", "x", "assistant", "hi", "user", "a", "user", "b"), []string{"a", "b"}},
		{"one user message since the agent spoke", transcript("user", "a", "assistant", "hi", "user", "b"), []string{"b"}},
		{"ends on the assistant", transcript("user", "a", "assistant", "hi"), nil},
		{"only the assistant has spoken", transcript("assistant", "hi"), nil},
		{"only user-role messages count", transcript("assistant", "hi", "system", "note", "user", "b"), []string{"b"}},
		{"a trailing tool result is not the user's", transcript("assistant", "hi", "user", "a", "function", "r"), []string{"a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := contents(TrailingUserMessages(c.in)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("TrailingUserMessages = %v, want %v", got, c.want)
			}
		})
	}
}

func TestLastUserMessage(t *testing.T) {
	cases := []struct {
		name string
		in   AnnotatedMessages
		want []string
	}{
		{"empty transcript", nil, nil},
		{"one user message", transcript("user", "a"), []string{"a"}},
		{"two user messages and no assistant", transcript("user", "a", "user", "b"), []string{"b"}},
		{"two user messages since the agent spoke", transcript("user", "x", "assistant", "hi", "user", "a", "user", "b"), []string{"b"}},
		{"ends on the assistant", transcript("user", "a", "assistant", "hi"), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := contents(LastUserMessage(c.in)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("LastUserMessage = %v, want %v", got, c.want)
			}
		})
	}
}

func TestTrailingUserMessagesReturnsANewSliceOfShallowCopies(t *testing.T) {
	in := transcript("assistant", "hi", "user", "a", "user", "b")
	in[1].Annotations = map[string]Annotation{"k": {Name: "k"}}

	got := TrailingUserMessages(in)

	got[0].Content = "changed"
	if in[1].Content != "a" {
		t.Fatal("TrailingUserMessages returned a slice that aliases the transcript")
	}
	// The copies are shallow: an annotation a state pins on the selected
	// message lands on the transcript's message too, as the framework's
	// annotation states expect.
	got[0].Annotations["pinned"] = Annotation{Name: "pinned"}
	if _, ok := in[1].Annotations["pinned"]; !ok {
		t.Fatal("the selection's annotation map is not the transcript's: a pinned annotation would not reach the transcript")
	}
}
