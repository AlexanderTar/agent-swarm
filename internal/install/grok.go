package install

import (
	"context"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// CheckGrok is doctor's grok block. Unit 1 only has the skills check; the rest
// lands with the writer.
func CheckGrok(_ context.Context, c Config, _ execx.Runner) []Check {
	return []Check{CheckSkills(c, KindGrok)}
}
