package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// Spec E1 copy: the two accept kinds' native prompts.
func TestNativePromptForAcceptKinds(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	bug, err := s.Items.Create(ctx, items.CreateInput{Type: items.Bug, Title: "Login loop"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		got, err := s.nativePromptFor(ctx, tx, Request{ID: "req_e1", Kind: KindAcceptEpic, ItemID: ep.ID}, "", nil)
		if err != nil {
			return err
		}
		want := NativePrompt{Header: "Accept epic", Question: `Accept EPIC-1 "Build it" as done? ⟦swarm:req_e1⟧`, Options: approveOptions}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("accept_epic prompt = %+v, want %+v", got, want)
		}
		got, err = s.nativePromptFor(ctx, tx, Request{ID: "req_f1", Kind: KindAcceptFix, ItemID: bug.ID}, "", nil)
		if err != nil {
			return err
		}
		want = NativePrompt{Header: "Accept fix",
			Question: fmt.Sprintf(`Accept the fix for %s "Login loop" as done? ⟦swarm:req_f1⟧`, bug.Key), Options: approveOptions}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("accept_fix prompt = %+v, want %+v", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
