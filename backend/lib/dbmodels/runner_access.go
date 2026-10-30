package dbmodels

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// User is an identity seen at login (OIDC subject). It exists so an admin can
// pick users when assigning runners.
type User struct {
	Subject   string `gorm:"primaryKey" json:"subject"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	FirstSeen time.Time
	LastSeen  time.Time
}

// RunnerAssignment grants one user the right to start chats on one runner.
// A runner has no assignments until an admin creates them, which means that
// nobody can use it.
type RunnerAssignment struct {
	NodeID      string `gorm:"primaryKey;column:node_id"`
	UserSubject string `gorm:"primaryKey;column:user_subject;index"`
	AssignedBy  string
	CreatedAt   time.Time
}

// RunnerAccessRepository stores users and runner assignments.
type RunnerAccessRepository struct {
	db *gorm.DB
}

// NewRunnerAccessRepository creates a repository on db.
func NewRunnerAccessRepository(db *gorm.DB) *RunnerAccessRepository {
	return &RunnerAccessRepository{db: db}
}

// UpsertUser records a login, keeping FirstSeen of an existing user.
func (r *RunnerAccessRepository) UpsertUser(subject, name, email string) error {
	if subject == "" {
		return errors.New("empty subject")
	}
	now := time.Now().UTC()
	u := User{Subject: subject, Name: name, Email: email, FirstSeen: now, LastSeen: now}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "subject"}},
		DoUpdates: clause.Assignments(map[string]any{"name": name, "email": email, "last_seen": now}),
	}).Create(&u).Error
}

// ListUsers returns all known users ordered by email then subject.
func (r *RunnerAccessRepository) ListUsers() ([]User, error) {
	var out []User
	err := r.db.Order("email, subject").Find(&out).Error
	return out, err
}

// UserExists reports whether subject has logged in before.
func (r *RunnerAccessRepository) UserExists(subject string) (bool, error) {
	var n int64
	err := r.db.Model(&User{}).Where("subject = ?", subject).Count(&n).Error
	return n > 0, err
}

// Assign grants subject access to nodeID. It is idempotent.
func (r *RunnerAccessRepository) Assign(nodeID, subject, by string) error {
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(subject) == "" {
		return errors.New("node id and user subject are required")
	}
	a := RunnerAssignment{NodeID: nodeID, UserSubject: subject, AssignedBy: by, CreatedAt: time.Now().UTC()}
	if err := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&a).Error; err != nil {
		return fmt.Errorf("assign runner: %w", err)
	}
	return nil
}

// Unassign revokes the access of subject to nodeID. It is idempotent.
func (r *RunnerAccessRepository) Unassign(nodeID, subject string) error {
	return r.db.Where("node_id = ? AND user_subject = ?", nodeID, subject).
		Delete(&RunnerAssignment{}).Error
}

// IsAssigned reports whether subject may use nodeID.
func (r *RunnerAccessRepository) IsAssigned(nodeID, subject string) (bool, error) {
	if nodeID == "" || subject == "" {
		return false, nil
	}
	var n int64
	err := r.db.Model(&RunnerAssignment{}).
		Where("node_id = ? AND user_subject = ?", nodeID, subject).Count(&n).Error
	return n > 0, err
}

// RunnersForUser returns the node ids assigned to subject.
func (r *RunnerAccessRepository) RunnersForUser(subject string) (map[string]bool, error) {
	out := map[string]bool{}
	if subject == "" {
		return out, nil
	}
	var rows []RunnerAssignment
	if err := r.db.Where("user_subject = ?", subject).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, a := range rows {
		out[a.NodeID] = true
	}
	return out, nil
}

// AssignmentsByNode returns node id -> assigned user subjects.
func (r *RunnerAccessRepository) AssignmentsByNode() (map[string][]string, error) {
	var rows []RunnerAssignment
	if err := r.db.Order("created_at, user_subject").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, a := range rows {
		out[a.NodeID] = append(out[a.NodeID], a.UserSubject)
	}
	return out, nil
}

// SetAssignments replaces the set of users assigned to nodeID with subjects
// in one transaction, returning the subjects that were added and removed.
func (r *RunnerAccessRepository) SetAssignments(nodeID string, subjects []string, by string) (added, removed []string, err error) {
	want := map[string]bool{}
	for _, s := range subjects {
		if s = strings.TrimSpace(s); s != "" {
			want[s] = true
		}
	}
	err = r.db.Transaction(func(tx *gorm.DB) error {
		var rows []RunnerAssignment
		if err := tx.Where("node_id = ?", nodeID).Find(&rows).Error; err != nil {
			return err
		}
		have := map[string]bool{}
		for _, a := range rows {
			have[a.UserSubject] = true
			if !want[a.UserSubject] {
				removed = append(removed, a.UserSubject)
			}
		}
		for _, s := range removed {
			if err := tx.Where("node_id = ? AND user_subject = ?", nodeID, s).Delete(&RunnerAssignment{}).Error; err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		for s := range want {
			if have[s] {
				continue
			}
			a := RunnerAssignment{NodeID: nodeID, UserSubject: s, AssignedBy: by, CreatedAt: now}
			if err := tx.Create(&a).Error; err != nil {
				return err
			}
			added = append(added, s)
		}
		return nil
	})
	return added, removed, err
}
