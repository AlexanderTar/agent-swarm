package execx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RunFor is Run with a caller-chosen timeout. §12.4 gives the plugin commands 120 s,
// which Run's fixed 30 s cap would cut short. Stdin is closed, as in Run.
func RunFor(d time.Duration) Runner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return out, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
}
