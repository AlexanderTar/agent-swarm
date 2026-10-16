package runtime

// Test-only handles on unexported per-kind tables, for the external
// runtime_test.TestKindParity (which imports packages that import runtime).
var (
	LoginCommand      = loginCommand
	TodosNext         = todosNext
	QuestionHookKinds = questionHookKinds
)
