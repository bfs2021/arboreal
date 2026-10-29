package engine

import (
	"fmt"
	lua "github.com/yuin/gopher-lua"
	"github.com/zenful-ai/arboreal"
)

// ToDoList Executive

var arborealPlanner = LuaTypeExport{
	Name: "planner",
	Methods: map[string]lua.LGFunction{
		"name":             arborealBehaviorNameGetter,
		"description":      arborealBehaviorDescriptionGetter,
		"preamble":         arborealPreambleGetterAndSetter,
		"summary_preamble": arborealSummaryPreambleGetterAndSetter,
		"call":             arborealBehaviorCallFn,
		"copy":             arborealBehaviorCopy,
		"plan":             arborealPlannerPlan,
		"execute":          arborealPlannerExecute,
		"output":           arborealTodoListExecOutput,
		"oob":              arborealOOBHandlerGetterAndSetter,
		"client_id":        arborealPlannerClientIDGetterAndSetter,
	},
	Index: true,
}

func arborealPlannerFromLua(ud *lua.LUserData) (*arboreal.TodoListExecutive, error) {
	if vv, ok := ud.Value.(*arboreal.TodoListExecutive); ok {
		return vv, nil
	}

	return nil, fmt.Errorf("failed to convert userdata to arboreal planner")
}

func checkArborealPlanner(l *lua.LState) *arboreal.TodoListExecutive {
	ud := l.CheckUserData(1)

	behavior, err := arborealPlannerFromLua(ud)
	if err != nil {
		l.ArgError(1, err.Error())
		return nil
	}

	return behavior
}

func arborealPlannerPlan(l *lua.LState) int {
	exec := checkArborealPlanner(l)

	messages := checkArborealAnnotatedMessages(l, 2)

	exec.Plan(l.Context(), *messages)
	return 0
}

func arborealPlannerExecute(l *lua.LState) int {
	exec := checkArborealPlanner(l)

	messages := checkArborealAnnotatedMessages(l, 2)

	exec.Execute(l.Context(), *messages)
	return 0
}

func arborealTodoListExecOutput(l *lua.LState) int {
	exec := checkArborealPlanner(l)

	l.Push(lua.LString(exec.Output))
	return 1
}

func arborealPreambleGetterAndSetter(l *lua.LState) int {
	exec := checkArborealPlanner(l)

	// Getter
	if l.GetTop() == 1 {
		l.Push(lua.LString(exec.Preamble))
		return 1
	} else {
		preamble := l.CheckString(2)
		exec.Preamble = preamble
		return 0
	}
}

func arborealSummaryPreambleGetterAndSetter(l *lua.LState) int {
	exec := checkArborealPlanner(l)

	// Getter
	if l.GetTop() == 1 {
		l.Push(lua.LString(exec.SummaryPreamble))
		return 1
	} else {
		preamble := l.CheckString(2)
		exec.SummaryPreamble = preamble
		return 0
	}
}

func arborealOOBHandlerGetterAndSetter(l *lua.LState) int {
	exec := checkArborealPlanner(l)

	if l.GetTop() == 1 {
		ud := l.NewUserData()
		ud.Value = exec.OutOfBoundsHandler

		l.SetMetatable(ud, l.GetTypeMetatable("behavior"))
		l.Push(ud)
		return 1
	} else {
		oobHandler := checkArborealBehavior(l, 2)

		exec.OutOfBoundsHandler = oobHandler
		return 0
	}
}

func arborealPlannerClientIDGetterAndSetter(l *lua.LState) int {
	planner := checkArborealPlanner(l)

	if l.GetTop() == 2 {
		planner.ClientID = l.CheckString(2)
	} else {
		l.Push(lua.LString(planner.ClientID))
		return 1
	}

	return 0
}
