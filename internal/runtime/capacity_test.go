package runtime

import "testing"

func TestOperationReason(t *testing.T) {
	for key, want := range map[string]string{
		"capacity:ses_1": "capacity",
		"resume:ses_1":   "resume",
		"h1":             "",
		"":               "",
		"capacityx":      "",
	} {
		if got := OperationReason(key); got != want {
			t.Errorf("OperationReason(%q) = %q, want %q", key, got, want)
		}
	}
}
