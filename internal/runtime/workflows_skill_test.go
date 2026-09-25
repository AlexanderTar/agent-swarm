package runtime

import (
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
	"os"
	"strings"
	"testing"
)

func TestWorkflowsSkillExampleValidates(t *testing.T) {
	body, err := os.ReadFile("../../skills/swarm-workflows/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "```swarm-tree") {
		t.Fatal("skill needs a fenced swarm-tree example")
	}
	tree, err := ParseTree(text)
	if err != nil {
		t.Fatal(err)
	}
	errs, warnings := lintTree(tree)
	if len(errs) > 0 || len(warnings) > 0 {
		t.Fatalf("errors=%v warnings=%v", errs, warnings)
	}
	task := tree.Children[0].Children[0]
	resolved, err := workflow.Resolve(*task.Workflow, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Validate(workflow.LevelTask, resolved); err != nil {
		t.Fatal(err)
	}
}
