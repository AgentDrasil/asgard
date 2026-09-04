package dbmodels

import (
	"fmt"

	"gorm.io/gorm"
)

func AutoMigrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&Session{}, &WorkflowRun{}, &QueuedMessage{}); err != nil {
		return err
	}
	return backfillLegacyWorkflowRuns(db)
}

// backfillLegacyWorkflowRuns migrates existing workflow_runs records where dag_spec or input
// was stored in the database column into offloaded files on disk.
func backfillLegacyWorkflowRuns(db *gorm.DB) error {
	if !db.Migrator().HasTable("workflow_runs") {
		return nil
	}
	if !db.Migrator().HasColumn(&WorkflowRun{}, "dag_spec_path") || !db.Migrator().HasColumn(&WorkflowRun{}, "input_path") {
		return nil
	}

	type legacyRunRow struct {
		RunID       string `gorm:"column:run_id"`
		SessionID   string `gorm:"column:session_id"`
		DAGSpec     string `gorm:"column:dag_spec"`
		Input       string `gorm:"column:input"`
		DAGSpecPath string `gorm:"column:dag_spec_path"`
		InputPath   string `gorm:"column:input_path"`
		NodeStates  string `gorm:"column:node_states"`
	}

	var legacyRows []legacyRunRow
	// Only query if dag_spec column exists in the underlying table schema
	if !db.Migrator().HasColumn("workflow_runs", "dag_spec") {
		return nil
	}

	err := db.Table("workflow_runs").
		Where("(dag_spec_path = '' OR dag_spec_path IS NULL) AND (dag_spec != '' AND dag_spec IS NOT NULL)").
		Or("(input_path = '' OR input_path IS NULL) AND (input != '' AND input IS NOT NULL)").
		Find(&legacyRows).Error
	if err != nil {
		return fmt.Errorf("backfill scan legacy workflow runs: %w", err)
	}

	for _, row := range legacyRows {
		sessionDir := defaultSessionDir(row.SessionID)
		states, decodeErr := DecodeNodeStates(row.NodeStates)
		var offloadedStates map[string]NodeState
		if decodeErr == nil {
			offloadedStates = states
		}
		dagPath, inPath, offloadedStates, err := WriteOffloadedFiles(sessionDir, row.RunID, row.DAGSpec, row.Input, offloadedStates)
		if err != nil {
			return fmt.Errorf("backfill legacy workflow run %s: %w", row.RunID, err)
		}

		updates := map[string]any{
			"dag_spec_path": dagPath,
			"input_path":    inPath,
		}
		if decodeErr == nil {
			if encodedStates, err := EncodeNodeStates(offloadedStates); err == nil {
				updates["node_states"] = encodedStates
			}
		}
		// Clear legacy raw values in table to complete the migration
		updates["dag_spec"] = ""
		updates["input"] = ""

		if err := db.Table("workflow_runs").Where("run_id = ?", row.RunID).Updates(updates).Error; err != nil {
			return fmt.Errorf("update legacy workflow run %s: %w", row.RunID, err)
		}
	}

	return nil
}
