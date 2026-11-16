package dbmodels

import (
	"errors"
	"sort"
	"time"

	"gorm.io/gorm"
)

// ChatRoute is one routing-table entry: on chat ChatID, the topic Topic is
// answered by chat TargetChatID (an instance, never a role). There is at most
// one entry per (chat, topic): the latest write wins.
type ChatRoute struct {
	ChatID       string    `gorm:"column:chat_id;primaryKey;size:64" json:"chatId"`
	Topic        string    `gorm:"column:topic;primaryKey;size:64" json:"topic"`
	TargetChatID string    `gorm:"column:target_chat_id;size:64;index:idx_chat_routes_target" json:"targetChatId"`
	Note         string    `gorm:"column:note;size:200" json:"note,omitempty"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// TableName keeps the table name explicit.
func (ChatRoute) TableName() string { return "chat_routes" }

// ErrRouteLimit is returned by SetRoute when a chat already has the maximum
// number of entries and the topic is a new one.
var ErrRouteLimit = errors.New("too many routes on this chat")

// ErrRouteNotFound is returned by DeleteRoute for an unknown (chat, topic).
var ErrRouteNotFound = errors.New("route not found")

// maxAncestorDepth bounds the walk up a (possibly corrupt, cyclic) tree.
const maxAncestorDepth = 1024

// SetRoute stores the entry for (chatID, topic), replacing an existing one
// (replaced is then true). A new topic is refused with ErrRouteLimit when the
// chat already has max entries.
func (r *SessionRepository) SetRoute(chatID, topic, target, note string, max int) (replaced bool, err error) {
	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	err = r.db.Transaction(func(tx *gorm.DB) error {
		var cur ChatRoute
		ferr := tx.Where("chat_id = ? AND topic = ?", chatID, topic).First(&cur).Error
		switch {
		case ferr == nil:
			replaced = true
			return tx.Model(&ChatRoute{}).Where("chat_id = ? AND topic = ?", chatID, topic).
				Updates(map[string]any{"target_chat_id": target, "note": note, "updated_at": time.Now()}).Error
		case !errors.Is(ferr, gorm.ErrRecordNotFound):
			return ferr
		}
		var n int64
		if err := tx.Model(&ChatRoute{}).Where("chat_id = ?", chatID).Count(&n).Error; err != nil {
			return err
		}
		if int(n) >= max {
			return ErrRouteLimit
		}
		now := time.Now()
		return tx.Create(&ChatRoute{ChatID: chatID, Topic: topic, TargetChatID: target, Note: note, CreatedAt: now, UpdatedAt: now}).Error
	})
	return replaced, err
}

// DeleteRoute removes the entry of (chatID, topic) or returns ErrRouteNotFound.
func (r *SessionRepository) DeleteRoute(chatID, topic string) error {
	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()
	res := r.db.Delete(&ChatRoute{}, "chat_id = ? AND topic = ?", chatID, topic)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrRouteNotFound
	}
	return nil
}

// EffectiveRoute is a table entry together with the chat that defines it.
type EffectiveRoute struct {
	ChatRoute
	// DefinedAt is the chat the winning entry is stored on.
	DefinedAt string
}

// EffectiveRoutes returns the routing table of chatID: the entries of chatID
// and of its ancestors up to the root, where for each topic the nearest chat
// wins (chatID itself first). It uses two queries: one recursive ancestor
// CTE, then the routes of those chats. root is the root chat of the spawn tree
// (the last ancestor), which the caller uses for the implicit `user` entry.
// Routes are sorted by topic.
func (r *SessionRepository) EffectiveRoutes(chatID string) (routes []EffectiveRoute, root string, err error) {
	table, err := r.sessionTable()
	if err != nil {
		return nil, "", err
	}
	var chain []string
	err = r.db.Raw(`WITH RECURSIVE up(chat_id, parent_chat_id, d) AS (
SELECT chat_id, parent_chat_id, 0 FROM `+table+` WHERE chat_id = ?
UNION ALL
SELECT s.chat_id, s.parent_chat_id, up.d + 1 FROM `+table+` s JOIN up ON s.chat_id = up.parent_chat_id WHERE up.d < ?
) SELECT chat_id FROM up ORDER BY d`, chatID, maxAncestorDepth).Scan(&chain).Error
	if err != nil {
		return nil, "", err
	}
	if len(chain) == 0 {
		return nil, "", nil
	}
	// A cyclic tree repeats chats; keep the first occurrence (the nearest).
	rank := make(map[string]int, len(chain))
	uniq := make([]string, 0, len(chain))
	for _, id := range chain {
		if _, ok := rank[id]; !ok {
			rank[id] = len(uniq)
			uniq = append(uniq, id)
		}
	}
	root = uniq[len(uniq)-1]
	var rows []ChatRoute
	if err := r.db.Where("chat_id IN ?", uniq).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	best := map[string]ChatRoute{}
	for _, row := range rows {
		if cur, ok := best[row.Topic]; !ok || rank[row.ChatID] < rank[cur.ChatID] {
			best[row.Topic] = row
		}
	}
	routes = make([]EffectiveRoute, 0, len(best))
	for _, row := range best {
		routes = append(routes, EffectiveRoute{ChatRoute: row, DefinedAt: row.ChatID})
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Topic < routes[j].Topic })
	return routes, root, nil
}

// SessionsMeta loads the session rows (without transcripts) of ids in one
// query, keyed by chat id. Unknown ids are absent.
func (r *SessionRepository) SessionsMeta(ids []string) (map[string]*Session, error) {
	out := make(map[string]*Session, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var sessions []Session
	if err := r.db.Where("chat_id IN ?", ids).Find(&sessions).Error; err != nil {
		return nil, err
	}
	for i := range sessions {
		out[sessions[i].ChatID] = &sessions[i]
	}
	return out, nil
}
