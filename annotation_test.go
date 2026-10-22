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

func TestCrossingRoundTrip(t *testing.T) {
	marked := Annotation{
		Name: "outcome", Data: "resolved", Explanation: "the user accepted",
		Cross: Crossing{Enabled: true, Merge: KeepLast},
	}
	b, err := json.Marshal(marked)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"cross":{"enabled":true}`) {
		t.Fatalf("a marked annotation marshals as %s, want a cross object holding enabled", b)
	}
	if strings.Contains(strings.ToLower(string(b)), "merge") {
		t.Fatalf("a marked annotation marshals its Merge: %s; the function must not be stored", b)
	}
	var back Annotation
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Cross.Enabled {
		t.Fatal("Enabled did not survive the round trip")
	}
	if back.Cross.Merge != nil {
		t.Fatal("Merge is set after the round trip; nothing can have stored it")
	}
	if back.Name != marked.Name || back.Data != marked.Data || back.Explanation != marked.Explanation {
		t.Fatalf("unmarshaled %+v, want the name, data and explanation of %+v", back, marked)
	}

	// An annotation that does not cross looks as it always did: no cross
	// key at all, not an empty object. This needs omitzero, Go 1.24+.
	plain := Annotation{Name: "brief", Data: "scratch for the reply state"}
	b, err = json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "cross") {
		t.Fatalf("an unmarked annotation marshals with a cross key: %s; the tag must be omitzero", b)
	}
	var backPlain Annotation
	if err := json.Unmarshal(b, &backPlain); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backPlain, plain) {
		t.Fatalf("unmarshaled %+v, want %+v", backPlain, plain)
	}

	// A Merge with Enabled false is ignored, and leaves no trace in JSON
	// either: Crossing.IsZero makes omitzero look at the flag alone.
	unmarked := Annotation{Name: "brief", Data: "scratch", Cross: Crossing{Merge: KeepLast}}
	b, err = json.Marshal(unmarked)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "cross") {
		t.Fatalf("an annotation with a Merge but Enabled false marshals with a cross key: %s", b)
	}
}

func TestKeepFirstAndKeepLast(t *testing.T) {
	if got := KeepFirst("mine", "theirs"); got != "mine" {
		t.Fatalf("KeepFirst returned %v, want mine", got)
	}
	if got := KeepLast("mine", "theirs"); got != "theirs" {
		t.Fatalf("KeepLast returned %v, want theirs", got)
	}
}

func TestReservedAnnotationNames(t *testing.T) {
	for _, name := range []string{AnnotationToolCalls, "raw_history", "$context", "plan", "__trace_annotations"} {
		if !reservedAnnotation(name) {
			t.Fatalf("%q is not reserved; the executive writes it", name)
		}
	}
	for _, name := range []string{"conflict_outcome", "tool_call", "rawhistory", ""} {
		if reservedAnnotation(name) {
			t.Fatalf("%q is reserved; only the executive's five names are", name)
		}
	}
}

func TestForStepClonesTheMapAndClearsCross(t *testing.T) {
	m := AppendToMessages(nil, llm.ChatCompletionMessage{Role: llm.ChatMessageRoleUser, Content: "Hello"}).LastMessage()
	m.Annotations["k"] = Annotation{
		Name: "k", Data: "v", Explanation: "why",
		Cross: Crossing{Enabled: true, Merge: KeepLast},
	}

	got := m.forStep()

	if got.Role != m.Role || got.Content != "Hello" {
		t.Fatalf("forStep returned %+v, want the same embedded message", got.ChatCompletionMessage)
	}
	k, ok := got.Annotations["k"]
	if !ok || k.Name != "k" || k.Data != "v" || k.Explanation != "why" {
		t.Fatalf("the copy's k = %+v (present %v), want the original's name, data and explanation", k, ok)
	}
	if k.Cross.Enabled || k.Cross.Merge != nil {
		t.Fatal("the copy's inherited annotation still carries Cross; forStep must clear it")
	}
	got.Annotations["probe"] = Annotation{Name: "probe"}
	if _, ok := m.Annotations["probe"]; ok {
		t.Fatal("writing into the copy's map wrote into the original's: forStep did not clone it")
	}
	if !m.Annotations["k"].Cross.Enabled {
		t.Fatal("the original's annotation lost its Cross; forStep must not write back")
	}

	// A nil map becomes an empty map of the step's own, as cloneAnnotations
	// gives it, so a state that pins on the copy need not allocate.
	bare := AnnotatedMessage{ChatCompletionMessage: llm.ChatCompletionMessage{Role: llm.ChatMessageRoleUser, Content: "x"}}
	if own := bare.forStep().Annotations; own == nil || len(own) != 0 {
		t.Fatalf("forStep gave a message that had no map %v, want an empty map of its own", own)
	}
}
