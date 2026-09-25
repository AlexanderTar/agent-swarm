# P9 fix round 3 scoped re-review — remaining finding

Range `4820c88..b6079f9`. Binding builder-continuity directive and the
named test improvements are addressed. One Important issue remains:

`applyEscalate` (`internal/runtime/workflow.go:1292`) finishes exhausted
agents, but leaves their `worktree_reservations` rows unreleased. Reclaim's
reservation gate (`internal/runtime/reconcile.go:1030`) still rejects the
worktree even though `liveDescendants` is zero. Reviewer scratch probe at
`/var/folders/32/hcklzbvx2wg0nxfxc2m0nzgh0000gn/T/p9-rereview3.ynwf3g9l/internal/runtime/zz_rereview3_probe_test.go`
found one retained reservation; manually releasing it changed eligible
reclaim candidates from 0 to 1. Release reservations for retired agents
on escalation and extend the exhausted-retry test to assert the reclaim
gate outcome. Preserve the atomic builder-bound retry transition.
