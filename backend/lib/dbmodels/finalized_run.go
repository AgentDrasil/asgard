package dbmodels

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FinalizedRun records a runner run_id whose RunResult was already finalized,
// so a replayed result (e.g. after a backend restart) is ignored.
type FinalizedRun struct {
	RunID       string    `gorm:"primaryKey;size:128" json:"runId"`
	FinalizedAt time.Time `gorm:"column:finalized_at" json:"finalizedAt"`
}

// FinalizedRunRepository persists finalized runner run ids.
type FinalizedRunRepository struct {
	db *gorm.DB
}

// NewFinalizedRunRepository creates a repository backed by db.
func NewFinalizedRunRepository(db *gorm.DB) *FinalizedRunRepository {
	return &FinalizedRunRepository{db: db}
}

// MarkFinalized records runID and reports whether it was newly recorded
// (false means it had been finalized before).
func (r *FinalizedRunRepository) MarkFinalized(ctx context.Context, runID string) (bool, error) {
	res := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&FinalizedRun{RunID: runID, FinalizedAt: time.Now()})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// IsFinalized reports whether runID was finalized before.
func (r *FinalizedRunRepository) IsFinalized(ctx context.Context, runID string) (bool, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&FinalizedRun{}).Where("run_id = ?", runID).Count(&n).Error
	return n > 0, err
}
