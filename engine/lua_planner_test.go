package engine

import (
	"strings"
	"testing"
)

func TestPlannerRefusesABehaviorThatIsNotAStep(t *testing.T) {
	err := TestInRuntime(`
local s = arboreal.state("a", "a", function(history) return history, nil end)
local p = arboreal.planner("p", "plans", s)
`)

	if err == nil {
		t.Fatal("arboreal.planner accepted a bare state as a behavior, want an argument error")
	}
	if !strings.Contains(err.Error(), "must be trees") {
		t.Fatalf("error = %q, want it to say a planner's behaviors must be trees", err)
	}
}

func TestPlannerAcceptsATree(t *testing.T) {
	err := TestInRuntime(`
local tree = arboreal.tree("chat", "chat with the user", "hi there")
tree:add(arboreal.state("a", "a", function(history) return history, nil end))
local p = arboreal.planner("p", "plans", tree)
`)

	if err != nil {
		t.Fatalf("arboreal.planner refused a tree: %v", err)
	}
}

func TestPlannerNamesTheOffendingArgument(t *testing.T) {
	err := TestInRuntime(`
local tree = arboreal.tree("chat", "chat with the user", "hi there")
tree:add(arboreal.state("a", "a", function(history) return history, nil end))
local inner = arboreal.planner("inner", "plans too")
local p = arboreal.planner("p", "plans", tree, inner)
`)

	if err == nil {
		t.Fatal("arboreal.planner accepted a nested planner as a behavior, want an argument error")
	}
	// A nested executive is a Behavior but not a Step, and the error must
	// point at it, not at the tree before it.
	if !strings.Contains(err.Error(), "bad argument #4") || !strings.Contains(err.Error(), "must be trees") {
		t.Fatalf("error = %q, want it to name argument #4 and say a planner's behaviors must be trees", err)
	}
}
