package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// SetCombos replaces the logical Combo config through compile-before-commit
// and atomic snapshot publication. Members retain caller order.
func (m *Manager) SetCombos(ctx context.Context, combos []Combo) (*RuntimeSnapshot, error) {
	return m.Update(ctx, func(candidate *Candidate) error {
		candidate.Combos = cloneComboList(combos)
		return nil
	})
}

// PutCombo creates or replaces one Combo by ID, preserving member positions.
// An update matches by ID only so a rename cannot collide with an unrelated
// Combo; a create rejects a duplicate name.
func (m *Manager) PutCombo(ctx context.Context, combo Combo) (*RuntimeSnapshot, error) {
	creating := combo.ID == ""
	if creating {
		combo.ID = uuid.NewString()
	}
	return m.Update(ctx, func(candidate *Candidate) error {
		for i := range candidate.Combos {
			if candidate.Combos[i].ID == combo.ID {
				candidate.Combos[i] = combo
				return nil
			}
			if creating && candidate.Combos[i].Name == combo.Name {
				return fmt.Errorf("combo %q already exists", combo.Name)
			}
		}
		candidate.Combos = append(candidate.Combos, combo)
		return nil
	})
}

// DeleteCombo deletes by ID or name and recompiles/publishes in one mutation.
func (m *Manager) DeleteCombo(ctx context.Context, key string) (*RuntimeSnapshot, error) {
	return m.Update(ctx, func(candidate *Candidate) error {
		for i, combo := range candidate.Combos {
			if combo.ID == key || combo.Name == key {
				candidate.Combos = append(candidate.Combos[:i], candidate.Combos[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("combo %q not found", key)
	})
}

func cloneComboList(combos []Combo) []Combo {
	cloned := make([]Combo, len(combos))
	for i, combo := range combos {
		combo.Members = append([]ComboMember(nil), combo.Members...)
		cloned[i] = combo
	}
	return cloned
}

func persistCombos(ctx context.Context, tx *sql.Tx, combos []Combo) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM combos"); err != nil {
		return fmt.Errorf("replace combos: %w", err)
	}
	for _, combo := range combos {
		id := combo.ID
		if id == "" {
			return fmt.Errorf("combo id is required")
		}
		fusion := 0
		if combo.FusionEnabled {
			fusion = 1
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO combos(id,name,strategy,sticky_limit,fusion_enabled,judge_model) VALUES(?,?,?,?,?,?)", id, combo.Name, combo.Strategy, combo.StickyLimit, fusion, nullString(combo.JudgeModel)); err != nil {
			return fmt.Errorf("persist combo %q: %w", combo.Name, err)
		}
		members := append([]ComboMember(nil), combo.Members...)
		sort.SliceStable(members, func(i, j int) bool { return members[i].Position < members[j].Position })
		for position, member := range members {
			selected := 0
			if member.Selected {
				selected = 1
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO combo_models(id,combo_id,provider_id,model_id,position,selected) VALUES(?,?,?,?,?,?)", uuid.NewString(), id, member.ProviderID, member.ModelID, position, selected); err != nil {
				return fmt.Errorf("persist combo member %q: %w", member.ModelID, err)
			}
		}
	}
	return nil
}

func loadCombos(ctx context.Context, db *sql.DB) ([]Combo, error) {
	rows, err := db.QueryContext(ctx, "SELECT id,name,strategy,sticky_limit,fusion_enabled,COALESCE(judge_model,'') FROM combos ORDER BY name,id")
	if err != nil {
		return nil, fmt.Errorf("load combos: %w", err)
	}
	defer rows.Close()
	var combos []Combo
	index := make(map[string]int)
	for rows.Next() {
		var combo Combo
		var sticky int64
		var fusion int
		if err := rows.Scan(&combo.ID, &combo.Name, &combo.Strategy, &sticky, &fusion, &combo.JudgeModel); err != nil {
			return nil, err
		}
		if sticky > 0 {
			combo.StickyLimit = uint64(sticky)
		}
		combo.FusionEnabled = fusion != 0
		index[combo.ID] = len(combos)
		combos = append(combos, combo)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	members, err := db.QueryContext(ctx, "SELECT combo_id,provider_id,model_id,position,selected FROM combo_models ORDER BY combo_id,position,id")
	if err != nil {
		return nil, fmt.Errorf("load combo members: %w", err)
	}
	defer members.Close()
	for members.Next() {
		var id string
		var member ComboMember
		var selected int
		if err := members.Scan(&id, &member.ProviderID, &member.ModelID, &member.Position, &selected); err != nil {
			return nil, err
		}
		member.Selected = selected != 0
		if comboIndex, ok := index[id]; ok {
			combos[comboIndex].Members = append(combos[comboIndex].Members, member)
		}
	}
	if err := members.Err(); err != nil {
		return nil, err
	}
	return combos, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
