package arboreal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"math/big"
	insecureRand "math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zenful-ai/arboreal/llm"
)

// ZEN_SEED_RNG selects a deterministic generator for GenerateStringIdentifier.
// It is a private *rand.Rand rather than math/rand's package-level generator
// because, once the main module's go directive is 1.24 or newer (GODEBUG
// randseednop=1), math/rand.Seed is a no-op and the global generator can no
// longer be made deterministic; a library cannot override that for its
// consumers, so it must not depend on it.
var (
	seedRNGOnce sync.Once
	seededRNG   *insecureRand.Rand
	seededRNGMu sync.Mutex
)

// insecureRandInt draws from the seeded generator when ZEN_SEED_RNG parsed as
// an integer, and otherwise from math/rand's auto-seeded global generator,
// which is what an unparseable seed has always fallen back to.
func insecureRandInt() int {
	seededRNGMu.Lock()
	defer seededRNGMu.Unlock()
	if seededRNG == nil {
		return insecureRand.Int()
	}
	return seededRNG.Int()
}

func MonotonicIdGenerator(prefix string) func() string {
	var nextId uint64 = 0
	return func() string {
		id := atomic.AddUint64(&nextId, 1)
		return fmt.Sprintf("%s%d", prefix, id)
	}
}

// GenerateStringIdentifier generates a random string of a given length and prefix.
func GenerateStringIdentifier(prefix string, length int) (string, error) {
	var err error
	var buf bytes.Buffer
	var n = 0

	encoding := base32.NewEncoding("0123456789abcdefghijklmnopqrstuv").WithPadding(base32.NoPadding)
	encoder := base32.NewEncoder(encoding, &buf)

	// We'll generate at most 8 bytes at a time (maximum value of an int64)
	mx := big.NewInt(9223372036854775807)

	// When running in a test environment, it is sometimes valuable to have deterministic pseudo-random numbers
	var useInsecureRNG bool
	if s := os.Getenv("ZEN_SEED_RNG"); s != "" {
		seedRNGOnce.Do(func() {
			randomSeed, err := strconv.Atoi(s)
			if err != nil {
				// Not an initialization but a reset: seedRNGOnce can be re-armed
				// (the tests do), and an unparseable seed must then fall back to
				// the global generator rather than keep an earlier seeded one.
				seededRNGMu.Lock()
				seededRNG = nil
				seededRNGMu.Unlock()
				return
			}

			seededRNGMu.Lock()
			seededRNG = insecureRand.New(insecureRand.NewSource(int64(randomSeed)))
			seededRNGMu.Unlock()
		})
		useInsecureRNG = true
	}

	// Ensure we generate at least n bytes
	for n < length {
		var randomNumber *big.Int

		if useInsecureRNG {
			randomNumber = big.NewInt(int64(insecureRandInt()))
		} else {
			randomNumber, err = rand.Int(rand.Reader, mx)
			if err != nil {
				return "", err
			}
		}

		written, err := encoder.Write(randomNumber.Bytes())
		if err != nil {
			return "", err
		}

		n += written
	}

	// Must close the encoder when finished to flush any partial blocks.
	// If you comment out the following line, the last partial block "r"
	// won't be encoded.
	err = encoder.Close()
	if err != nil {
		return "", err
	}

	// Trim the buffer to the correct length
	result := buf.String()[:length-len(prefix)]

	return prefix + result, nil
}

type BehaviorState struct {
	StateName        string
	StateDescription string
	HashId           string
	ClientID         string
	Lambda           func(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal)
}

func (b *BehaviorState) Hash() string {
	return b.HashId
}

func (b *BehaviorState) Name() string {
	return b.StateName
}

func (b *BehaviorState) Description() string {
	return b.StateDescription
}

func (b *BehaviorState) Call(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal) {
	var trace Trace
	var isTracing bool

	historySize := len(history)

	// Check for a trace channel in the context
	if ctx != nil && ctx.Value("arboreal_trace") != nil {
		isTracing = true
		trace, _ = ctx.Value("arboreal_trace").(Trace)
	}

	telem := TraceTelemetry{
		Start: time.Now(),
	}

	trace.Send(&TraceMessage{
		Type:     TraceMessageTypeCallBegin,
		ID:       b.Hash(),
		ClientID: b.ClientID,
		Name:     b.Name(),
		Message:  "entering custom state",
		Telemetry: &TraceTelemetry{
			Start: telem.Start,
		},
	})

	m, s := b.Lambda(ctx, history)

	var err error
	if e, ok := s.(*ErrorSignal); ok {
		err = e
	}

	now := time.Now()
	telem.End = &now

	var ops []*TraceHistoryOperation
	if isTracing {
		a := m.GetAnnotation("__trace_annotations")
		if a != nil {
			s, ok := a.Data.(string)
			if ok {
				annotation_names := strings.Split(s, ",")
				for _, name := range annotation_names {
					x := m.GetAnnotation(name)
					if x != nil {
						ops = append(ops, &TraceHistoryOperation{
							Type:       "annotation",
							Action:     "add",
							Annotation: x,
						})
					}
				}
			}
		}

		// Detect history adds
		if len(m) > historySize {
			for _, message := range history[historySize:] {
				ops = append(ops, &TraceHistoryOperation{
					Type:    "history",
					Action:  "add",
					Message: &message.ChatCompletionMessage,
				})
			}
		}
	}

	// Clear __trace_annotations
	delete(m.LastMessage().Annotations, "__trace_annotations")

	trace.Send(&TraceMessage{
		Type:       TraceMessageTypeCallEnd,
		ID:         b.Hash(),
		ClientID:   b.ClientID,
		Name:       b.Name(),
		Error:      err,
		Message:    "leaving custom state",
		Telemetry:  &telem,
		Operations: ops,
		Signal:     TraceForSignal(s),
	})

	return m, s
}

func (b *BehaviorState) Copy() Behavior {
	var s BehaviorState

	s.StateName = b.StateName
	s.StateDescription = b.StateDescription
	s.HashId = b.HashId
	s.Lambda = b.Lambda
	s.ClientID = b.ClientID

	return &s
}

type LLMCompletionOptions struct {
	Name        string
	Description string
	ClientID    string
	Id          string
	System      string
	// Model is the model URI for this state's completion call, e.g.
	// "anthropic:claude-sonnet-4-20250514". Empty falls back to the context
	// default set with WithDefaultModel, and, when that is absent too, to a
	// transitional built-in default with a once-per-process warning.
	Model        string
	ExtraContext []string
	Annotation   string
	Terminal     bool
	AllowTools   bool
	// Tools narrows what AllowTools offers. When non-empty, only the named
	// tools — by the name the mux knows them, i.e. the server's name — are
	// sent to the model, in this order, and the tool loop refuses to
	// execute any other name. A name the mux does not have, a duplicate, a
	// missing mux, Tools set without AllowTools, or Tools on a state in
	// annotation mode (which never offers tools) is an ErrorSignal before
	// any model call. Empty offers every tool on the mux, as before.
	//
	// The slice is captured by reference, like ExtraContext; do not mutate
	// it after the state is constructed.
	Tools []string
}

func CannedResponseState(message string) *BehaviorState {
	id, _ := GenerateStringIdentifier("id-", 16)
	return &BehaviorState{
		HashId: id,
		Lambda: func(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal) {
			return append(history, AnnotatedMessage{
				ChatCompletionMessage: llm.ChatCompletionMessage{
					Role:    llm.ChatMessageRoleAssistant,
					Content: message,
				},
			}), nil
		},
	}
}

func evalIntoAnnotation(ctx context.Context, history AnnotatedMessages, options LLMCompletionOptions) (AnnotatedMessages, Signal) {
	model := modelURIFor(ctx, options.Model)
	provider, err := llm.CreateModelProvider(model, llm.ProviderOpenAI)
	if err != nil {
		return history, &ErrorSignal{
			ErrorMessage: err.Error(),
			ErrorType:    StateErrorTypeUnrecoverable,
		}
	}

	system := options.System
	if len(options.ExtraContext) > 0 {
		system += "\n\nExtra Context:\n\n"
		for _, extraContext := range options.ExtraContext {
			if a := history.GetAnnotation(extraContext); a != nil {
				system += fmt.Sprintf("%v\n\n", a.Data)
			}
		}
	}

	truncatedHistory := AnnotatedMessages{
		{
			ChatCompletionMessage: llm.ChatCompletionMessage{
				Role:    llm.ChatMessageRoleSystem,
				Content: system,
			},
		},
		{
			ChatCompletionMessage: llm.ChatCompletionMessage{
				Role: llm.ChatMessageRoleUser,
			},
		},
	}

	var lastUserMessageIndex int
	for idx, message := range history {
		if message.Role == llm.ChatMessageRoleUser {
			truncatedHistory[1].ChatCompletionMessage = message.ChatCompletionMessage
			lastUserMessageIndex = idx
		}
	}

	res, err := provider.CreateChatCompletion(ctx, &llm.ChatCompletionRequest{
		Model:    model,
		Messages: truncatedHistory.ChatCompletionMessages(),
	})
	if err != nil {
		return nil, &ErrorSignal{
			ErrorMessage: err.Error(),
			ErrorType:    StateErrorTypeUnknown,
		}
	}

	annotation := annotationFromReply(options.Annotation, res.Message.Content)

	if history[lastUserMessageIndex].Annotations == nil {
		history[lastUserMessageIndex].Annotations = make(map[string]Annotation)
	}

	history[lastUserMessageIndex].Annotations[options.Annotation] = annotation
	history.AddTraceInformation(options.Annotation)

	return history, nil
}

// annotationFromReply decodes an annotation-mode reply: the model's JSON
// when it parses and carries data, else the raw text as the Data of an
// annotation named name. The model names the Data; it does not get to say
// whether the pin crosses, since annotation mode has no switch for that
// (see Annotation.Cross), so a reply carrying "cross" is stored unmarked.
func annotationFromReply(name, content string) Annotation {
	var annotation Annotation
	err := json.Unmarshal([]byte(content), &annotation)
	if err != nil || annotation.Data == nil {
		annotation = Annotation{
			Name: name,
			Data: content,
		}
	}
	annotation.Cross = Crossing{}
	return annotation
}

// allowedToolCall reports whether the AllowTools loop may execute a call to
// name under the given LLMCompletionOptions.Tools. An empty list is no
// narrowing: every tool the mux knows may run, as before the option existed.
// Otherwise only a listed name may run. tools must be the same list the
// state passed to Select, so that what was offered is what may run.
func allowedToolCall(tools []string, name string) bool {
	return len(tools) == 0 || slices.Contains(tools, name)
}

func LLMCompletionState(options LLMCompletionOptions) BehaviorState {
	id, _ := GenerateStringIdentifier("id-", 16)
	if options.Id != "" {
		id = options.Id
	}
	return BehaviorState{
		HashId:           id,
		StateName:        options.Name,
		StateDescription: options.Description,
		ClientID:         options.ClientID,
		Lambda: func(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal) {
			var system string
			{
				var buf bytes.Buffer

				var tmpl AnnotationTemplate
				_, err := tmpl.Parse(options.System)
				if err != nil {
					return history, &ErrorSignal{
						ErrorMessage: err.Error(),
					}
				}
				err = tmpl.Execute(&buf, history)
				if err != nil {
					return history, &ErrorSignal{
						ErrorMessage: err.Error(),
					}
				}

				system = buf.String()
			}

			// An annotation-mode state never offers tools (evalIntoAnnotation
			// sends none), so a Tools list on one is a misconfiguration, not a
			// request; report it like the other ways the list cannot be honored.
			if options.Annotation != "" && len(options.Tools) > 0 {
				return history, &ErrorSignal{
					ErrorMessage: "Tools is set but this state is in annotation mode, which never offers tools",
					ErrorType:    StateErrorTypeUnrecoverable,
				}
			}

			if options.Annotation != "" {
				opts := options
				opts.System = system
				return evalIntoAnnotation(ctx, history, opts)
			}

			model := modelURIFor(ctx, options.Model)
			provider, err := llm.CreateModelProvider(model, llm.ProviderOpenAI)
			if err != nil {
				return history, &ErrorSignal{
					ErrorMessage: err.Error(),
					ErrorType:    StateErrorTypeUnrecoverable,
				}
			}

			if len(options.ExtraContext) > 0 {
				system += "\n\nExtra Context:\n\n"
				for _, extraContext := range options.ExtraContext {
					annotation := history.GetAnnotation(extraContext)
					if annotation == nil {
						continue
					}
					system += fmt.Sprintf("%v\n\n", annotation.Data)
				}
			}

			if options.System != "" {
				if len(history) > 0 && history[0].Role == llm.ChatMessageRoleSystem {
					history[0].Content = system
				} else {
					history = append(AnnotatedMessages{
						AnnotatedMessage{
							ChatCompletionMessage: llm.ChatCompletionMessage{
								Role:    llm.ChatMessageRoleSystem,
								Content: system,
							},
							Annotations: make(map[string]Annotation),
						},
					}, history...)
				}
			}

			request := llm.ChatCompletionRequest{
				Model:    model,
				Messages: history.ChatCompletionMessages(),
			}

			var client *MCPClientMux
			switch {
			case !options.AllowTools && len(options.Tools) > 0:
				return history, &ErrorSignal{
					ErrorMessage: "Tools is set but AllowTools is false",
					ErrorType:    StateErrorTypeUnrecoverable,
				}
			case options.AllowTools:
				c, ok := MCPClientFromContext(ctx)
				switch {
				case !ok && len(options.Tools) > 0:
					return history, &ErrorSignal{
						ErrorMessage: "Tools is set but no MCP client is in the context (WithMCPClient)",
						ErrorType:    StateErrorTypeUnrecoverable,
					}
				case ok && len(options.Tools) == 0:
					client = c
					request.Tools = c.Tools()
				case ok:
					// Select validates the list; the loop below executes only
					// names on it (allowedToolCall), so what was offered is
					// exactly what may run.
					tools, err := c.Select(options.Tools...)
					if err != nil {
						return history, &ErrorSignal{
							ErrorMessage: err.Error(),
							ErrorType:    StateErrorTypeUnrecoverable,
						}
					}
					client = c
					request.Tools = tools
				}
			}

			var res *llm.ChatCompletionResponse
			for {
				res, err = provider.CreateChatCompletion(ctx, &request)
				if err != nil {
					return history, &ErrorSignal{
						ErrorMessage: err.Error(),
						ErrorType:    StateErrorTypeUnknown,
					}
				}

				if len(res.Message.ToolCalls) == 0 || client == nil {
					break
				}

				// Narrowing what is offered does not narrow what the model can
				// name: nothing on the wire guarantees the name that comes back
				// was one of the tools sent. Refuse anything outside the offered
				// list before it reaches the mux, which may know far more.
				if !allowedToolCall(options.Tools, res.Message.ToolCalls[0].Name) {
					return history, &ErrorSignal{
						ErrorMessage: fmt.Sprintf("model called %q, which this state does not offer", res.Message.ToolCalls[0].Name),
						ErrorType:    StateErrorTypeRetryable,
					}
				}

				r, err := client.CallTool(ctx, &mcp.CallToolParams{
					Name:      res.Message.ToolCalls[0].Name,
					Arguments: res.Message.ToolCalls[0].Arguments,
				})
				if err != nil {
					return history, &ErrorSignal{
						ErrorMessage: err.Error(),
					}
				}

				// TODO: Handle other types of content
				var content string
				switch t := r.Content[0].(type) {
				case *mcp.TextContent:
					content = t.Text
				default:
					b, err := r.Content[0].MarshalJSON()
					if err != nil {
						return history, &ErrorSignal{
							ErrorMessage: err.Error(),
						}
					}
					content = string(b)
				}

				toolResultMessage := llm.ChatCompletionMessage{
					Meta:    res.Message.ToolCalls[0].Meta,
					Name:    res.Message.ToolCalls[0].Name,
					Role:    llm.ChatMessageRoleFunction,
					Content: content,
				}

				// Keep tool call and result in history so callers can inspect executed tools.
				history = append(history,
					AnnotatedMessage{ChatCompletionMessage: res.Message},
					AnnotatedMessage{ChatCompletionMessage: toolResultMessage},
				)

				request.Messages = append(request.Messages, res.Message, toolResultMessage)
			}

			s := Signal(nil)
			if options.Terminal {
				s = &TerminalSignal{}
			}

			return append(history, AnnotatedMessage{ChatCompletionMessage: res.Message}), s
		},
	}
}

func PauseState(reason string) BehaviorState {
	id, _ := GenerateStringIdentifier("id-", 16)
	return BehaviorState{
		HashId: id,
		Lambda: func(ctx context.Context, history AnnotatedMessages) (AnnotatedMessages, Signal) {
			return history, &CollectUserInputSignal{
				Reason: reason,
			}
		},
	}
}
