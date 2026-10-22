package arboreal

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zenful-ai/arboreal/llm"
	"io"
	"strings"
	"text/scanner"
	"time"
	"unicode/utf8"
)

// Crossing says whether an annotation a step writes is copied onto the
// reply the executive appends, and how two steps' values for the same name
// combine when both cross in one turn.
type Crossing struct {
	// Enabled makes the annotation cross. Off by default: an annotation a
	// step writes stays on the step's messages unless it says otherwise.
	Enabled bool `json:"enabled,omitempty"`

	// Merge is called when another step crossed the same name in the same
	// turn, in plan order, as (mine, theirs) on the annotation already
	// placed, and returns the Data to keep. Nil keeps mine: the first step
	// to cross a name wins. Only consulted when Enabled. Not stored: the
	// merge happens within the turn, before any snapshot or transcript is
	// written, and on the transcript the value is a result.
	Merge func(mine, theirs any) any `json:"-"`
}

// IsZero reports whether the annotation does not cross, so that omitzero
// leaves Cross out of JSON whenever Enabled is false: a Merge on an
// annotation that does not cross is ignored, and is not stored either.
func (c Crossing) IsZero() bool { return !c.Enabled }

// KeepFirst is a Merge strategy: the value already placed stands.
func KeepFirst(mine, theirs any) any { return mine }

// KeepLast is a Merge strategy: the later step's value replaces the placed
// one.
func KeepLast(mine, theirs any) any { return theirs }

type Annotation struct {
	Name        string `json:"name"`
	Data        any    `json:"data"`
	Explanation string `json:"explanation"`
	// Cross says whether, and how, this annotation crosses from a step's
	// messages to the reply the executive appends. The zero value does not
	// cross, and omitzero (with Crossing.IsZero) keeps it out of JSON, so
	// an annotation that does not cross is stored as it always was.
	Cross Crossing `json:"cross,omitzero"`
}

// reservedAnnotation reports whether name is one the executive writes for
// itself: tool_calls (the record on the reply), raw_history and $context
// (on a planned step's direction), plan (the planner's answer) and
// __trace_annotations (the tracer's breadcrumb). A step's annotation under
// one of these names never crosses, whatever its Cross; raw_history is the
// one that matters, since its Data is the whole transcript.
func reservedAnnotation(name string) bool {
	switch name {
	case AnnotationToolCalls, "raw_history", "$context", "plan", "__trace_annotations":
		return true
	}
	return false
}

type AnnotatedMessage struct {
	llm.ChatCompletionMessage
	Annotations map[string]Annotation
}

// cloneAnnotations returns m with its own annotation map: a new, non-nil map
// holding the same entries (not maps.Clone, which keeps a nil map nil).
// Annotation values are copied by value; an Annotation.Data that is a map or
// a slice stays shared. The executive uses it to give a step's messages maps
// the transcript does not share.
func cloneAnnotations(m AnnotatedMessage) AnnotatedMessage {
	annotations := make(map[string]Annotation, len(m.Annotations))
	for name, annotation := range m.Annotations {
		annotations[name] = annotation
	}
	m.Annotations = annotations
	return m
}

type AnnotatedMessages []AnnotatedMessage

func (a AnnotatedMessages) ChatCompletionMessages() (m []llm.ChatCompletionMessage) {
	for _, message := range a {
		m = append(m, message.ChatCompletionMessage)
	}
	return
}

func (a AnnotatedMessages) LastMessage() *AnnotatedMessage {
	if len(a) == 0 {
		return nil
	}
	return &a[len(a)-1]
}

func (a AnnotatedMessages) AddTraceInformation(name string) {
	var annotationsAdded []string
	annotation := a.GetAnnotation("__trace_annotations")
	if annotation != nil {
		as, ok := annotation.Data.(string)
		if ok {
			annotationsAdded = strings.Split(as, ",")
		}
	}

	annotationsAdded = append(annotationsAdded, name)

	if a.LastMessage().Annotations == nil {
		a.LastMessage().Annotations = make(map[string]Annotation)
	}

	a.LastMessage().Annotations["__trace_annotations"] = Annotation{
		Name: "__trace_annotations",
		Data: strings.Join(annotationsAdded, ","),
	}
}

func (a AnnotatedMessages) GetAnnotation(name string) *Annotation {
	for idx := len(a) - 1; idx >= 0; idx-- {
		m := a[idx]
		if m.Annotations == nil {
			continue
		}

		annotation, ok := m.Annotations[name]
		if ok {
			return &annotation
		}
	}

	// If we didn't find an annotation, handle special "meta-annotations"
	if strings.HasPrefix(name, "$") {
		switch name {
		case "$last_message":
			return &Annotation{
				Name:        "$last_message",
				Data:        a.LastMessage().Content,
				Explanation: "The last message in the chat history",
			}
		case "$date":
			return &Annotation{
				Name:        "$date",
				Data:        time.Now().Format(time.RFC3339),
				Explanation: "The current date",
			}
		case "$date_llm":
			return &Annotation{
				Name:        "$date_llm",
				Data:        fmt.Sprintf("Today's date is: %s.", time.Now().Format(time.RubyDate)),
				Explanation: "The current date, formatted for LLM consumption",
			}
		}
	}

	return nil
}

func (a AnnotatedMessages) FlattenedAnnotations() map[string]Annotation {
	annotations := make(map[string]Annotation)
	for _, message := range a {
		for _, annotation := range message.Annotations {
			annotations[annotation.Name] = annotation
		}
	}
	return annotations
}

func AppendToMessages(messages AnnotatedMessages, message llm.ChatCompletionMessage) AnnotatedMessages {
	return append(messages, AnnotatedMessage{
		ChatCompletionMessage: message,
		Annotations:           make(map[string]Annotation),
	})
}

// ToolCallsFromAnnotation decodes the executive's tool_calls annotation: the
// typed slice before a JSON round trip, the generic shape after one. A nil
// annotation decodes to nil, nil. The shape of an argument value is whatever
// JSON gives: a number that was an int before the round trip is a float64
// after it, and this helper does not guess.
func ToolCallsFromAnnotation(a *Annotation) ([]ToolCallRecord, error) {
	if a == nil {
		return nil, nil
	}
	if records, ok := a.Data.([]ToolCallRecord); ok {
		return records, nil
	}
	b, err := json.Marshal(a.Data)
	if err != nil {
		return nil, fmt.Errorf("annotation %q: %w", a.Name, err)
	}
	var records []ToolCallRecord
	if err := json.Unmarshal(b, &records); err != nil {
		return nil, fmt.Errorf("annotation %q: %w", a.Name, err)
	}
	return records, nil
}

//
// Annotation Templates
//
// Template syntax is similar to mustache-style template syntax:
//	 * basic annotation: {{ annotation_name }} or {{ $last_message }}
//   	- The mustache-style closure will be replaced with the value of the annotation,
//	      coerced into a string.
//	 * truthy annotation: {{ User preference: preference? }}
//		- If multiple words are specified in the template, then only words followed by
//		  question marks will be interpreted as annotation names.
//		- If the annotation is false-y, the whole contents will be omitted, otherwise the
//		  template will be replaced.

type templateBlock interface {
	Value(AnnotatedMessages) string
}

type textBlock struct {
	Text string
}

func (t *textBlock) Value(_ AnnotatedMessages) string {
	return t.Text
}

type annotationBlock struct {
	Name string
}

func (a *annotationBlock) Value(m AnnotatedMessages) string {
	annotation := m.GetAnnotation(a.Name)
	if annotation == nil {
		return ""
	}

	switch t := annotation.Data.(type) {
	case string:
		return t
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", t)
	case time.Time:
		return t.Format(time.RFC3339)
	case float32, float64:
		return fmt.Sprintf("%f", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

type multiAnnotationBlock struct {
	Values []templateBlock
}

func (m *multiAnnotationBlock) Parse(text string) {
	var s scanner.Scanner

	s.Init(strings.NewReader(text))
	s.Whitespace ^= 1<<'\t' | 1<<'\n' | 1<<' '

	var isEscaping bool
	var previous string
	var curr string
	for tok := s.Scan(); tok != scanner.EOF; tok = s.Scan() {
		if strings.HasSuffix(s.TokenText(), "?") && isEscaping {
			curr += previous
			previous = "?"
			isEscaping = false
			continue
		}

		if strings.HasSuffix(s.TokenText(), "?") {
			if s.Peek() == '?' {
				isEscaping = true
				continue
			}

			if len(curr) > 0 {
				m.Values = append(m.Values, &textBlock{Text: curr})
				curr = ""
			}

			m.Values = append(m.Values, &annotationBlock{Name: previous})
			previous = ""
			continue
		}

		curr += previous
		previous = s.TokenText()
	}

	if curr != "" || previous != "" {
		m.Values = append(m.Values, &textBlock{Text: curr + previous})
	}

}

func (m *multiAnnotationBlock) Value(a AnnotatedMessages) string {
	var values []string
	for _, v := range m.Values {
		t := v.Value(a)
		if t == "" {
			return ""
		}

		values = append(values, t)
	}

	return strings.Join(values, "")
}

type AnnotationTemplate struct {
	blocks []templateBlock
}

func (a *AnnotationTemplate) Parse(text string) (*AnnotationTemplate, error) {
	var position int

	var insideBlock bool
	var current string
	for {
		r, width := utf8.DecodeRuneInString(text[position:])
		skip := 0

		switch r {
		case '{':
			next, _ := utf8.DecodeRuneInString(text[position+width:])

			if next == '{' {
				skip = 2
				insideBlock = true

				if current != "" {
					a.blocks = append(a.blocks, &textBlock{Text: current})
					current = ""
				}
			} else {
				current += string(r)
				skip = width
			}
		case '}':
			if insideBlock {
				next, _ := utf8.DecodeRuneInString(text[position+width:])

				if next == '}' {
					insideBlock = false
					skip = 2

					if current != "" {
						if len(strings.Fields(strings.TrimSpace(current))) > 1 {
							m := multiAnnotationBlock{}
							m.Parse(strings.TrimSpace(current))
							a.blocks = append(a.blocks, &m)
						} else {
							a.blocks = append(a.blocks, &annotationBlock{Name: strings.TrimSpace(current)})
						}
						current = ""
					}

					break
				}
			}

			current += string(r)
			skip = width
		default:
			current += string(r)
			skip = width
		}

		position += skip

		if position >= len(text) {
			break
		}
	}

	if insideBlock {
		return a, errors.New("unclosed annotation template")
	}

	if current != "" {
		a.blocks = append(a.blocks, &textBlock{Text: current})
	}

	return a, nil
}

func (a *AnnotationTemplate) Execute(wr io.Writer, messages AnnotatedMessages) error {
	var err error
	for _, block := range a.blocks {
		_, err = wr.Write([]byte(block.Value(messages)))
	}
	if err != nil {
		return err
	}

	return nil
}
