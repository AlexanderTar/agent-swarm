# Implementation Plan: Fix Chore Creation & Lift Brief Character Limit

## Task Breakdown

### Task 1: Allow `chore` SpikeIntent in `internal/items`
- **Files**:
  - `internal/items/store_test.go`
  - `internal/items/store.go`
- **Steps**:
  1. Add test `TestCreateSpikeWithChoreIntent` in `internal/items/store_test.go`:
     ```go
     func TestCreateSpikeWithChoreIntent(t *testing.T) {
         s := newStore(t)
         ctx := context.Background()
         it, err := s.Create(ctx, items.CreateInput{
             Type:        items.Spike,
             Title:       "Maintenance chore",
             SpikeIntent: "chore",
         }, items.User("test"))
         if err != nil {
             t.Fatalf("expected chore spike creation to succeed, got: %v", err)
         }
         if it.SpikeIntent != "chore" {
             t.Fatalf("expected spike_intent 'chore', got: %q", it.SpikeIntent)
         }
     }
     ```
  2. Run `go test -v -run TestCreateSpikeWithChoreIntent ./internal/items/...` and observe failure.
  3. Modify `internal/items/store.go` line 217:
     ```go
     if in.Type == Spike && in.SpikeIntent != "feature" && in.SpikeIntent != "debug" && in.SpikeIntent != "chore" {
         return Item{}, errf(CodeBadRequest, "Spikes start with an intent. Use New spike.")
     }
     ```
  4. Re-run `go test -v -run TestCreateSpikeWithChoreIntent ./internal/items/...` and verify pass.

---

### Task 2: Lift 600-Character Brief Limit in `internal/items` & DB Schema
- **Files**:
  - `internal/db/schema/0005_lift_brief_limit.sql`
  - `internal/db/db.go`
  - `internal/db/db_test.go`
  - `internal/items/store.go`
  - `internal/items/store_test.go`
- **Steps**:
  1. Create `internal/db/schema/0005_lift_brief_limit.sql` recreating `items` table without `CHECK (length(brief) <= 600)`.
  2. Update `const SchemaVersion = 5` in `internal/db/db.go`.
  3. Update `TestCheckConstraints` in `internal/db/db_test.go`:
     - Remove `"long brief"` from failure expectations.
     - Add assertion that inserting an item with a 2,000-character brief succeeds.
  4. Run `go test -v ./internal/db/...` to verify migrations and constraints.
  5. In `internal/items/store.go`, remove `if utf8.RuneCountInString(brief) > 600` from `validateText`.
  6. In `internal/items/store_test.go`:
     - Update `TestStoreValidate` or add `TestCreateItemWithLongBrief` verifying briefs > 600 runes (e.g. 2,000 runes) succeed.
  7. Run `go test -v ./internal/items/...` and verify all tests pass.

---

### Task 3: Update Web Frontend Limit
- **Files**:
  - `web/src/logic/newItem.ts`
  - `web/src/panels/Details.tsx`
  - `web/src/mock/daemon.ts`
  - `web/src/panels/NewItemSheet.test.tsx`
  - `web/src/logic/newItem.test.ts`
- **Steps**:
  1. Remove `BRIEF_MAX = 600` from `web/src/logic/newItem.ts`.
  2. In `web/src/panels/Details.tsx`, remove `maxLength={600}` on `Editable label={C.brief}`.
  3. In `web/src/mock/daemon.ts`, remove `.slice(0, 600)`.
  4. Update web tests.

---

### Task 4: Runtime End-to-End Verification & Installation
- **Files**:
  - `internal/runtime/agents_test.go`
- **Steps**:
  1. Add test `TestStartSpikeWithChoreIntentAndLongBrief` in `internal/runtime/agents_test.go`:
     Verify `s.StartSpike` with `Intent: "chore"` and a 1,200-character request succeeds and returns `it.SpikeIntent == "chore"`.
  2. Run all tests: `go test ./internal/...`
  3. Compile and install daemon: `make build && make install-daemon`
  4. Restart daemon if running.
