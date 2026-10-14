package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/ncruces/go-sqlite3"
	"github.com/zenful-ai/arboreal"
	"github.com/zenful-ai/arboreal/llm"

	_ "embed"

	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
)

func main() {
	db, err := sqlite3.Open("/tmp/crm.db")
	if err != nil {
		log.Fatal(err)
	}

	s, _, err := db.Prepare(`CREATE TABLE IF NOT EXISTS clients (id PRIMARY KEY ASC, name TEXT, membank TEXT);`)
	if err != nil {
		log.Fatal(err)
	}
	err = s.Exec()
	if err != nil {
		log.Fatal(err)
	}

	mem := arboreal.CreateMemoryStore(db, llm.OpenAIService{})

	askAboutClientBehavior := arboreal.CreateBehaviorTree("client_query", "User query for information on a particular client", "When is Bob's daughter's birthday?")
	recordClientFact := arboreal.CreateBehaviorTree("client_record_fact", "Record a fact about a particular client", "I just met with Bob, and learned that he has 3 daughters")
	listClients := arboreal.CreateBehaviorTree("list_clients", "List all of the clients", "")

	evalForClientQuery := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{
		System:     "Extract a client name from the user query. Only respond in valid JSON with the following fields:\n\tdata - a string containing the user's full or partial name.\n\nExample:\n\n{\"data\": \"John\"}",
		Annotation: "name",
	})
	evalForClientRecord := evalForClientQuery
	evalForClientRecord.HashId, err = arboreal.GenerateStringIdentifier("id-", 16)
	if err != nil {
		log.Fatal(err)
	}

	lookupClientQuery := arboreal.BehaviorState{
		HashId: "lookupClientQuery",
		Lambda: func(ctx context.Context, messages arboreal.AnnotatedMessages) (arboreal.AnnotatedMessages, arboreal.Signal) {
			name, ok := messages.GetAnnotation("name").Data.(string)
			if !ok {
				return nil, arboreal.ErrorSignal{
					ErrorMessage: "name is not a string",
					ErrorType:    arboreal.StateErrorTypeRetryable,
				}
			}

			name = strings.ToUpper(name)

			s, _, err := db.Prepare("SELECT name, membank FROM clients WHERE UPPER(name) like '%" + name + "%'")
			if err != nil {
				return nil, arboreal.ErrorSignal{
					ErrorMessage: err.Error(),
					ErrorType:    arboreal.StateErrorTypeUnrecoverable,
				}
			}

			type result struct {
				Name    string
				Membank string
			}
			var results []result

			for s.Step() {
				fullName := s.ColumnText(0)
				membank := s.ColumnText(1)

				results = append(results, result{
					Name:    fullName,
					Membank: membank,
				})
			}

			if len(results) == 0 {
				return nil, arboreal.SkipSignal{
					Reason: "Continuing, no results found",
				}
			}

			messages.LastMessage().Annotations["name"] = arboreal.Annotation{
				Data: results[0].Name,
			}

			messages.LastMessage().Annotations["membank"] = arboreal.Annotation{
				Data: results[0].Membank,
			}

			return messages, nil
		},
	}

	lookupClientRecord := lookupClientQuery
	lookupClientRecord.HashId, err = arboreal.GenerateStringIdentifier("id-", 16)
	if err != nil {
		log.Fatal(err)
	}

	lookupContext := arboreal.BehaviorState{
		HashId: "lookupContext",
		Lambda: func(ctx context.Context, messages arboreal.AnnotatedMessages) (arboreal.AnnotatedMessages, arboreal.Signal) {
			bank, ok := messages.GetAnnotation("membank").Data.(string)
			if !ok {
				return nil, arboreal.ErrorSignal{
					ErrorMessage: "bank is not a string",
					ErrorType:    arboreal.StateErrorTypeRetryable,
				}
			}

			chunks, err := mem.Recall(context.Background(), bank, messages.LastMessage().Content, "10")
			if err != nil {
				return nil, arboreal.ErrorSignal{
					ErrorMessage: err.Error(),
					ErrorType:    arboreal.StateErrorTypeUnrecoverable,
				}
			}

			var c string
			for _, chunk := range chunks {
				c += chunk.Text + "\n\n"
			}

			messages.LastMessage().Annotations["context"] = arboreal.Annotation{
				Data: c,
			}

			return messages, nil
		},
	}

	respondToQuery := arboreal.LLMCompletionState(arboreal.LLMCompletionOptions{
		System: "Answer the user's question given any relevant information contained below:",
		ExtraContext: []string{
			"name",
			"context",
		},
	})

	askAboutClientBehavior.AddTransition(&evalForClientQuery, &lookupClientQuery)
	askAboutClientBehavior.AddTransition(&lookupClientQuery, &lookupContext)
	askAboutClientBehavior.AddTransition(&lookupContext, &respondToQuery)

	recordClientFact.AddTransition(&evalForClientRecord, &lookupClientRecord)
	recordClientFact.AddTransition(&lookupClientRecord, &arboreal.BehaviorState{
		HashId: "storeClientFactInMemory",
		Lambda: func(ctx context.Context, messages arboreal.AnnotatedMessages) (arboreal.AnnotatedMessages, arboreal.Signal) {
			bank, ok := messages.GetAnnotation("membank").Data.(string)
			if !ok {
				return nil, arboreal.ErrorSignal{
					ErrorMessage: "bank is not a string",
					ErrorType:    arboreal.StateErrorTypeRetryable,
				}
			}

			err = mem.CreateMemoryBankIfNotExists(bank)
			if err != nil {
				return nil, arboreal.ErrorSignal{
					ErrorMessage: err.Error(),
					ErrorType:    arboreal.StateErrorTypeUnrecoverable,
				}
			}

			err = mem.Store(context.Background(), bank, messages.LastMessage().Content, "")
			if err != nil {
				return nil, arboreal.ErrorSignal{
					ErrorMessage: err.Error(),
					ErrorType:    arboreal.StateErrorTypeUnrecoverable,
				}
			}

			messages = arboreal.AppendToMessages(messages, llm.ChatCompletionMessage{
				Role:    llm.ChatMessageRoleAssistant,
				Content: "Fact recorded successfully!",
			})

			return messages, nil
		},
	})

	listClients.AddState(&arboreal.BehaviorState{
		HashId: "listClients",
		Lambda: func(ctx context.Context, messages arboreal.AnnotatedMessages) (arboreal.AnnotatedMessages, arboreal.Signal) {
			stmt, _, err := db.Prepare("SELECT name FROM clients;")
			if err != nil {
				return nil, arboreal.ErrorSignal{
					ErrorMessage: err.Error(),
					ErrorType:    arboreal.StateErrorTypeUnrecoverable,
				}
			}

			var clients []string
			for stmt.Step() {
				clients = append(clients, stmt.ColumnText(0))
			}

			return arboreal.AppendToMessages(messages, llm.ChatCompletionMessage{
				Role:    llm.ChatMessageRoleAssistant,
				Content: fmt.Sprintf("The client list is:\n\n%s", strings.Join(clients, "\n")),
			}), nil
		},
	})

	executive := arboreal.TodoListExecutive{
		Behaviors: []arboreal.Step{
			&askAboutClientBehavior,
			&recordClientFact,
			&listClients,
		},
		OutOfBoundsHandler: arboreal.CannedResponseState("Sorry, but I can only store and retrieve information about clients."),
	}

	err = executive.RunLoop(context.Background(), &arboreal.TerminalChannel{})
	if err != nil {
		log.Fatal(err)
	}
}
