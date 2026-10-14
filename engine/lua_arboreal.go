package engine

import (
	"context"

	lua "github.com/yuin/gopher-lua"
	"github.com/zenful-ai/arboreal"
)

var arborealNamespaceExport = LuaTypeExport{
	Name: "arboreal",
	Methods: map[string]lua.LGFunction{
		"state":        arborealNewBehaviorState,
		"tree":         arborealNewBehaviorTree,
		"llm_complete": arborealNewLLMCompletionState,
		"planner":      arborealNewPlanner,
	},
}

func arborealNewBehaviorState(l *lua.LState) int {
	name := l.CheckString(1)
	description := l.CheckString(2)

	var lambda *lua.LFunction
	var clientID string

	if l.GetTop() == 3 {
		lambda = l.CheckFunction(3)
	} else if l.GetTop() == 4 {
		clientID = l.CheckString(3)
		lambda = l.CheckFunction(4)
	}

	id := l.Context().Value("id").(func() string)
	if id == nil {
		panic("id generator is nil")
	}

	state := arboreal.BehaviorState{
		StateName:        name,
		StateDescription: description,
		HashId:           id(),
		ClientID:         clientID,
		Lambda: func(ctx context.Context, history arboreal.AnnotatedMessages) (arboreal.AnnotatedMessages, arboreal.Signal) {
			messages := arborealAnnotatedMessagesToLua(l, history)

			err := l.CallByParam(lua.P{
				Fn:      lambda,
				NRet:    2,
				Protect: true,
			}, messages)

			if err != nil {
				return history, &arboreal.ErrorSignal{
					ErrorMessage: err.Error(),
					ErrorType:    arboreal.StateErrorTypeLuaSyntax,
				}
			}

			ret := l.Get(-2)

			if ret == lua.LNil {
				return history, &arboreal.ErrorSignal{
					ErrorMessage: "expected two return value from lambda function",
					ErrorType:    arboreal.StateErrorTypeLuaSyntax,
				}
			}

			var h arboreal.AnnotatedMessages
			var s arboreal.Signal

			if t, ok := ret.(*lua.LTable); ok {
				hh, err := arborealAnnotatedMessagesFromLua(t)
				if err != nil {
					return history, &arboreal.ErrorSignal{
						ErrorMessage: err.Error(),
						ErrorType:    arboreal.StateErrorTypeLuaSyntax,
					}
				}
				h = *hh
			}

			ret = l.Get(-1)
			if ret != lua.LNil {
				if ud, ok := ret.(*lua.LUserData); ok {
					s, err = arborealSignalFromLua(ud)
					if err != nil {
						return history, &arboreal.ErrorSignal{
							ErrorMessage: err.Error(),
							ErrorType:    arboreal.StateErrorTypeLuaSyntax,
						}
					}
				}
			}

			l.Pop(2)

			return h, s
		},
	}

	ud := l.NewUserData()
	ud.Value = &state

	l.SetMetatable(ud, l.GetTypeMetatable("behavior"))
	l.Push(ud)

	return 1
}

func arborealNewBehaviorTree(l *lua.LState) int {
	name := l.CheckString(1)
	description := l.CheckString(2)
	example := l.CheckString(3)

	id := l.Context().Value("id").(func() string)
	if id == nil {
		panic("id generator is nil")
	}

	tree := arboreal.CreateBehaviorTreeWithId(name, description, example, id())

	ud := l.NewUserData()
	ud.Value = &tree

	l.SetMetatable(ud, l.GetTypeMetatable("behavior_tree"))
	l.Push(ud)

	return 1
}

func arborealNewPlanner(l *lua.LState) int {
	name := l.CheckString(1)
	description := l.CheckString(2)

	var behaviors []arboreal.Step
	if l.GetTop() > 2 {
		for i := 3; i <= l.GetTop(); i++ {
			b := checkArborealBehavior(l, i)

			// The executive plans over steps, and a tree is the only one;
			// a bare state would fail inside Plan, so refuse it here.
			step, ok := b.(arboreal.Step)
			if !ok {
				l.ArgError(i, "a planner's behaviors must be trees")
				return 0
			}

			behaviors = append(behaviors, step)
		}
	}

	id := l.Context().Value("id").(func() string)
	if id == nil {
		panic("id generator is nil")
	}

	exec := arboreal.CreateTodoListExecutiveWithId(name, description, id(), behaviors...)

	ud := l.NewUserData()
	ud.Value = exec

	l.SetMetatable(ud, l.GetTypeMetatable("planner"))
	l.Push(ud)
	return 1
}

func arborealNewLLMCompletionState(l *lua.LState) int {
	var options arboreal.LLMCompletionOptions

	if l.GetTop() == 1 {
		o := l.CheckTable(1)

		o.ForEach(func(k, v lua.LValue) {
			if k.String() == "name" {
				options.Name = v.String()
			}

			if k.String() == "description" {
				options.Description = v.String()
			}

			if k.String() == "system" {
				options.System = v.String()
			}

			if k.String() == "model" {
				options.Model = v.String()
			}

			if k.String() == "annotation" {
				options.Annotation = v.String()
			}

			if k.String() == "extra_context" {
				table, ok := v.(*lua.LTable)
				if !ok {
					l.RaiseError("extra_context is not a table")
				} else {
					table.ForEach(func(k, v lua.LValue) {
						options.ExtraContext = append(options.ExtraContext, v.String())
					})
				}
			}

			if k.String() == "client_id" {
				options.ClientID = v.String()
			}
		})
	}

	id := l.Context().Value("id").(func() string)
	if id == nil {
		panic("id generator is nil")
	}

	options.Id = id()

	state := arboreal.LLMCompletionState(options)

	ud := l.NewUserData()
	ud.Value = &state

	l.SetMetatable(ud, l.GetTypeMetatable("behavior"))
	l.Push(ud)

	return 1
}
