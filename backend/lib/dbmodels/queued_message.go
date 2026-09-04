package dbmodels

import (
	"errors"
	"fmt"
	"time"

	"uuid"

	"gorm.io/gorm"
)

// QueuedMessage represents a message waiting in line to be executed in a session.
type QueuedMessage struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	ChatID    string    `gorm:"column:chat_id;size:64;index:idx_chat_created,priority:1" json:"chatId"`
	Prompt    string    `gorm:"column:prompt;type:text" json:"prompt"`
	Model     string    `gorm:"column:model;size:128" json:"model,omitempty"`
	CreatedAt time.Time `gorm:"column:created_at;index:idx_chat_created,priority:2" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

const (
	// MaxQueuedMessagesPerSession specifies the maximum number of queued messages allowed per session.
	MaxQueuedMessagesPerSession = 3
)

var (
	// ErrQueueFull is returned when attempting to enqueue beyond the session capacity limit.
	ErrQueueFull = errors.New("queue is full, maximum 3 messages allowed")
	// ErrQueuedMessageNotFound is returned when a requested queued message does not exist.
	ErrQueuedMessageNotFound = errors.New("queued message not found")
)

// GetQueuedMessages returns all queued messages for a chat ordered deterministically by created_at ASC, id ASC.
func (r *SessionRepository) GetQueuedMessages(chatID string) ([]QueuedMessage, error) {
	var msgs []QueuedMessage
	err := r.db.Where("chat_id = ?", chatID).
		Order("created_at ASC, id ASC").
		Find(&msgs).Error
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// GetQueuedMessage retrieves a specific queued message by chatID and msgID.
func (r *SessionRepository) GetQueuedMessage(chatID string, msgID string) (*QueuedMessage, error) {
	var msg QueuedMessage
	err := r.db.Where("chat_id = ? AND id = ?", chatID, msgID).First(&msg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrQueuedMessageNotFound
		}
		return nil, err
	}
	return &msg, nil
}

// GetChatIDsWithQueuedMessages returns a deduplicated slice of chat IDs that currently have queued messages.
func (r *SessionRepository) GetChatIDsWithQueuedMessages() ([]string, error) {
	var chatIDs []string
	err := r.db.Model(&QueuedMessage{}).Distinct("chat_id").Pluck("chat_id", &chatIDs).Error
	if err != nil {
		return nil, err
	}
	return chatIDs, nil
}

// PeekNextQueuedMessage inspects the oldest queued message for a session without removing it.
// Returns nil, nil if the queue is empty.
func (r *SessionRepository) PeekNextQueuedMessage(chatID string) (*QueuedMessage, error) {
	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	var msg QueuedMessage
	err := r.db.Where("chat_id = ?", chatID).Order("created_at ASC, id ASC").First(&msg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &msg, nil
}

// EnqueueMessage validates the session queue limit and inserts a new queued message in a transaction.
func (r *SessionRepository) EnqueueMessage(chatID string, prompt string, model string) (*QueuedMessage, error) {
	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	var createdMsg QueuedMessage
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&QueuedMessage{}).Where("chat_id = ?", chatID).Count(&count).Error; err != nil {
			return err
		}
		if count >= MaxQueuedMessagesPerSession {
			return ErrQueueFull
		}

		now := time.Now()
		msg := QueuedMessage{
			ID:        fmt.Sprintf("qmsg-%s", uuid.NewV7().String()),
			ChatID:    chatID,
			Prompt:    prompt,
			Model:     model,
			CreatedAt: now,
			UpdatedAt: now,
		}

		if err := tx.Create(&msg).Error; err != nil {
			return err
		}
		createdMsg = msg
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &createdMsg, nil
}

// UpdateQueuedMessage updates the prompt of an existing queued message in a transaction.
func (r *SessionRepository) UpdateQueuedMessage(chatID string, msgID string, newPrompt string) (*QueuedMessage, error) {
	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	var updatedMsg QueuedMessage
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("chat_id = ? AND id = ?", chatID, msgID).First(&updatedMsg).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrQueuedMessageNotFound
			}
			return err
		}

		updatedMsg.Prompt = newPrompt
		updatedMsg.UpdatedAt = time.Now()

		return tx.Save(&updatedMsg).Error
	})
	if err != nil {
		return nil, err
	}

	return &updatedMsg, nil
}

// DeleteQueuedMessage deletes a specific queued message from a session.
func (r *SessionRepository) DeleteQueuedMessage(chatID string, msgID string) error {
	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	result := r.db.Delete(&QueuedMessage{}, "chat_id = ? AND id = ?", chatID, msgID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrQueuedMessageNotFound
	}
	return nil
}

// PopNextQueuedMessage atomically retrieves and removes the oldest queued message in a session.
// Returns nil, nil if the queue is empty.
func (r *SessionRepository) PopNextQueuedMessage(chatID string) (*QueuedMessage, error) {
	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	var msg QueuedMessage
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("chat_id = ?", chatID).Order("created_at ASC, id ASC").First(&msg).Error; err != nil {
			return err
		}
		return tx.Delete(&QueuedMessage{}, "chat_id = ? AND id = ?", chatID, msg.ID).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	return &msg, nil
}

// ClearQueuedMessages removes all queued messages for a chat ID and returns the number of deleted records.
func (r *SessionRepository) ClearQueuedMessages(chatID string) (int64, error) {
	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	result := r.db.Delete(&QueuedMessage{}, "chat_id = ?", chatID)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}
