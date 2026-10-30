package dbmodels

import (
	"fmt"

	"gorm.io/gorm"
)

func AutoMigrate(db *gorm.DB) error {
	// There is deliberately no backfill of the pre-offload workflow_runs
	// dag_spec/input columns into session files: those files belong on the
	// chat's runner, the backend must not create them in its own data dirs, and
	// the old code cleared the columns afterwards (losing the data when the
	// runner never saw the files). The legacy columns are left untouched.
	return db.AutoMigrate(&Session{}, &WorkflowRun{}, &QueuedMessage{}, &FinalizedRun{}, &User{}, &RunnerAssignment{})
}

// BackfillRunnerNodeID binds every session that has no runner yet to nodeID.
// It is idempotent and meant to run once at startup, with the first entry of
// runner.allowed_nodes, for chats created before sessions were bound to a
// runner.
func BackfillRunnerNodeID(db *gorm.DB, nodeID string) error {
	if nodeID == "" {
		return nil
	}
	if err := db.Model(&Session{}).
		// Cron sessions get their runner from the workflow definition.
		Where("(runner_node_id = '' OR runner_node_id IS NULL) AND chat_id NOT LIKE 'wf-cron-%'").
		Update("runner_node_id", nodeID).Error; err != nil {
		return fmt.Errorf("backfill session runner: %w", err)
	}
	return nil
}
