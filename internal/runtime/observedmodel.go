package runtime

import (
	"context"
	"database/sql"
	"errors"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
)

// RecordObservedModel records a model/effort the agent's CLI reported for a
// live session (a human ran /model or /effort in it) on the agent's row, so a
// resume or wake relaunch keeps it. It is idempotent: an observation that is
// canonically equal to the row (aliases, dated ids, launch slugs of the
// current model) writes nothing and raises no event. An empty model is ignored
// and an empty effort never overwrites. Advisor columns are left alone.
func (s *Store) RecordObservedModel(ctx context.Context, sessionID, model, effort, source string) (bool, error) {
	if model == "" {
		return false, nil
	}
	changed := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var agentID string
		if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM sessions WHERE id = ?`, sessionID).Scan(&agentID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		a, err := s.agentByIDTx(ctx, tx, agentID)
		if err != nil {
			return err
		}
		newModel, newEffort, ok := s.observedPair(ctx, a, model, effort)
		if !ok {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET model = ?, effort = ?, kind_reason = ? WHERE id = ?`,
			newModel, newEffort, joinReason(a.KindReason, "changed in session ("+source+")"), a.ID); err != nil {
			return err
		}
		changed = true
		return s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID)
	})
	return changed && err == nil, err
}

// observedPair returns the model/effort to store for an observation, and
// false when it is canonically equal to what the agent already has.
func (s *Store) observedPair(ctx context.Context, a Agent, model, effort string) (string, string, bool) {
	var models []catalog.CatalogModel
	if s.Catalog != nil {
		models, _, _ = s.Catalog.ModelsFor(ctx, a.Kind)
	}
	// canon resolves an id to its catalog model (when known), else keeps it raw.
	canon := func(id string) (catalog.CatalogModel, string, bool) { return catalog.FromLaunchID(models, id) }

	om, slugEffort, found := canon(model)
	if effort == "" {
		effort = slugEffort
	}
	newModel := model
	if found && model != "default" { // cursor's "default" (Auto) is stored as reported
		newModel = om.ID
	}
	curModel, _, curFound := canon(a.Model)
	sameModel := newModel == a.Model
	if found && curFound {
		sameModel = om.ID == curModel.ID
	}

	newEffort := a.Effort
	switch {
	case found && om.IsDefault && len(om.Efforts) == 0:
		newEffort = "" // Auto has no effort control
	case effort != "":
		newEffort = effort
	}
	// an unset row effort means the model's default effort
	curEffort := a.Effort
	if curEffort == "" && curFound {
		curEffort = curModel.DefaultEffort
	}
	cmpNew := newEffort
	if cmpNew == "" && found {
		cmpNew = om.DefaultEffort
	}
	if sameModel && cmpNew == curEffort {
		return "", "", false
	}
	if sameModel {
		newModel = a.Model // keep the row's own spelling of the same model
	}
	return newModel, newEffort, true
}
