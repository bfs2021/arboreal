package arboreal

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/zenful-ai/arboreal/llm"
)

func TestTextBlock_Value(t *testing.T) {
	var txt textBlock

	txt.Text = "This is a test"

	if txt.Value(AnnotatedMessages{}) != txt.Text {
		t.Fail()
	}
}

func TestAnnotationBlock_Value(t *testing.T) {
	var n annotationBlock

	n.Name = "Test"

	if n.Value(AnnotatedMessages{
		{
			Annotations: map[string]Annotation{
				"Test": Annotation{
					Name: "Test",
					Data: "Super Awesome!",
				},
			},
		},
	}) != "Super Awesome!" {
		t.Fail()
	}
}

func TestMultiAnnotationBlock(t *testing.T) {
	var m multiAnnotationBlock

	m.Parse("User preference: preference?")

	value := m.Value(AnnotatedMessages{
		{
			Annotations: map[string]Annotation{
				"preference": Annotation{
					Name: "preference",
					Data: "always address me as poopy butt",
				},
			},
		},
	})

	if value != "User preference: always address me as poopy butt" {
		t.Errorf("Unexpected value: %s", value)
	}

	emptyValue := m.Value(AnnotatedMessages{})
	if emptyValue != "" {
		t.Errorf("Unexpected value: %s", emptyValue)
	}
}

func TestDoubleQuestionMark(t *testing.T) {
	var m multiAnnotationBlock

	m.Parse("This is a test??")

	value := m.Value(AnnotatedMessages{})
	if value != "This is a test?" {
		t.Errorf("Unexpected value: %s", value)
	}
}

func TestAnnotationTemplate(t *testing.T) {
	var template AnnotationTemplate

	template.Parse("This is a test. Here is a basic annotation: {{ basic }}\n{{ This is a conditional? }}")

	var buf bytes.Buffer
	template.Execute(&buf, AnnotatedMessages{
		{
			Annotations: map[string]Annotation{
				"basic": Annotation{
					Name: "basic",
					Data: "hello world",
				},
				"conditional": Annotation{
					Name: "conditional",
					Data: "good idea",
				},
			},
		},
	})

	if buf.String() != "This is a test. Here is a basic annotation: hello world\nThis is a good idea" {
		t.Errorf("Unexpected value: %s", buf.String())
	}
}

func TestToolCallsFromAnnotationAfterAJSONRoundTrip(t *testing.T) {
	records := []ToolCallRecord{
		{
			Step:      "tree-a",
			Tool:      "lookup",
			Arguments: map[string]any{"title": "Dune", "limit": 3, "filter": map[string]any{"in_stock": true}},
			Result:    "1 in stock",
		},
		{Step: "tree-b", Tool: "notify", Result: "sent"},
	}
	m := AppendToMessages(nil, llm.ChatCompletionMessage{Role: llm.ChatMessageRoleAssistant, Content: "reply"})
	m.LastMessage().Annotations[AnnotationToolCalls] = Annotation{
		Name:        AnnotationToolCalls,
		Data:        records,
		Explanation: "the tool calls this turn's steps made, in plan order",
	}

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back AnnotatedMessages
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}

	a := back.LastMessage().Annotations[AnnotationToolCalls]
	if _, typed := a.Data.([]ToolCallRecord); typed {
		t.Fatal("Data is still the typed slice after a JSON round trip; the test proves nothing")
	}
	got, err := ToolCallsFromAnnotation(&a)
	if err != nil {
		t.Fatal(err)
	}
	// JSON gives every number back as a float64; the helper does not guess.
	want := []ToolCallRecord{
		{
			Step:      "tree-a",
			Tool:      "lookup",
			Arguments: map[string]any{"title": "Dune", "limit": float64(3), "filter": map[string]any{"in_stock": true}},
			Result:    "1 in stock",
		},
		{Step: "tree-b", Tool: "notify", Result: "sent"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}

	// Before a round trip the typed slice comes back as is.
	typed, err := ToolCallsFromAnnotation(&Annotation{Name: AnnotationToolCalls, Data: records})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(typed, records) {
		t.Fatalf("typed Data decoded to %+v, want it unchanged", typed)
	}

	// A nil annotation decodes to nil, nil.
	if got, err := ToolCallsFromAnnotation(nil); got != nil || err != nil {
		t.Fatalf("nil annotation decoded to (%v, %v), want (nil, nil)", got, err)
	}

	// Data of neither shape is an error naming the annotation, not a panic.
	_, err = ToolCallsFromAnnotation(&Annotation{Name: AnnotationToolCalls, Data: "not a list"})
	if err == nil {
		t.Fatal("decoding a string returned no error")
	}
	if !strings.Contains(err.Error(), AnnotationToolCalls) {
		t.Fatalf("error %q does not name the annotation", err)
	}
}

func TestCloneAnnotationsGivesTheMessageItsOwnMap(t *testing.T) {
	shared := map[string]Annotation{"pref": {Name: "pref", Data: "paperback"}}
	in := AnnotatedMessage{
		ChatCompletionMessage: llm.ChatCompletionMessage{Role: llm.ChatMessageRoleUser, Content: "a"},
		Annotations:           shared,
	}

	got := cloneAnnotations(in)

	if got.Role != llm.ChatMessageRoleUser || got.Content != "a" {
		t.Fatalf("cloneAnnotations changed the chat message: %+v", got.ChatCompletionMessage)
	}
	if got.Annotations["pref"].Data != "paperback" {
		t.Fatalf("the clone lost the entries: %v", got.Annotations)
	}
	got.Annotations["pinned"] = Annotation{Name: "pinned"}
	if _, ok := shared["pinned"]; ok {
		t.Fatal("the clone's map is the original's: a write on the clone reached it")
	}
	if in.Annotations["pref"].Data != "paperback" {
		t.Fatal("the original lost an entry")
	}
}

func TestCloneAnnotationsAllocatesForANilMap(t *testing.T) {
	got := cloneAnnotations(AnnotatedMessage{})

	if got.Annotations == nil {
		t.Fatal("cloneAnnotations left a nil map; a state that pins on the message would have to allocate")
	}
	if n := len(got.Annotations); n != 0 {
		t.Fatalf("the clone of a nil map holds %d entries, want 0", n)
	}
}
