package api

import (
	"sync"
	"time"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

const (
	defaultRingBufferCapacity = 200
	defaultTopicIdleDuration  = 10 * time.Minute
	defaultGCPollInterval     = 1 * time.Minute

	EventTypeMessage     = "message"
	EventTypeStatus      = "status"
	EventTypeTitle       = "title"
	EventTypeArtifact    = "artifact"
	EventTypeDone        = "done"
	EventTypeResync      = "resync"
	EventTypeAuthExpired = "auth_expired"
	EventTypeQueue       = "queue"
)

// SessionEvent is the unified event structure broadcasted via EventHub and SSE.
type SessionEvent struct {
	EventID   int64                 `json:"eventId"`
	ChatID    string                `json:"chatId"`
	Type      string                `json:"type"` // message | status | title | artifact | done | resync | auth_expired
	Message   *dbmodels.ChatMessage `json:"message,omitempty"`
	Payload   map[string]any        `json:"payload,omitempty"`
	Timestamp int64                 `json:"timestamp"`
}

type subscriber struct {
	id   int64
	ch   chan SessionEvent
	done chan struct{}
}

type sessionTopic struct {
	mu           sync.Mutex
	chatID       string
	nextEventID  int64
	events       []SessionEvent
	head         int
	count        int
	capacity     int
	subscribers  map[int64]*subscriber
	nextSubID    int64
	lastActivity time.Time
}

func newSessionTopic(chatID string, capacity int) *sessionTopic {
	if capacity <= 0 {
		capacity = defaultRingBufferCapacity
	}
	return &sessionTopic{
		chatID:       chatID,
		nextEventID:  time.Now().UnixMilli(),
		events:       make([]SessionEvent, capacity),
		capacity:     capacity,
		subscribers:  make(map[int64]*subscriber),
		lastActivity: time.Now(),
	}
}

func (t *sessionTopic) oldestEventID() int64 {
	if t.count == 0 {
		return 0
	}
	return t.events[t.head].EventID
}

func (t *sessionTopic) newestEventID() int64 {
	if t.count == 0 {
		return 0
	}
	idx := (t.head + t.count - 1) % t.capacity
	return t.events[idx].EventID
}

func (t *sessionTopic) publish(ev SessionEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.nextEventID++
	ev.EventID = t.nextEventID
	if ev.Timestamp == 0 {
		ev.Timestamp = time.Now().UnixMilli()
	}
	t.lastActivity = time.Now()

	// Append to ring buffer
	if t.count < t.capacity {
		idx := (t.head + t.count) % t.capacity
		t.events[idx] = ev
		t.count++
	} else {
		t.events[t.head] = ev
		t.head = (t.head + 1) % t.capacity
	}

	// Broadcast to active subscribers
	for id, sub := range t.subscribers {
		select {
		case sub.ch <- ev:
		default:
			// Subscriber channel is full; disconnect to avoid blocking publisher
			delete(t.subscribers, id)
			close(sub.done)
		}
	}
}

// SessionEventHub manages per-chat event publishing and SSE subscriptions.
type SessionEventHub struct {
	topicsMu sync.RWMutex
	topics   map[string]*sessionTopic
	capacity int
	gcTicker *time.Ticker
	stopGC   chan struct{}
}

// NewSessionEventHub creates a new SessionEventHub instance with background topic GC.
func NewSessionEventHub() *SessionEventHub {
	return NewSessionEventHubWithCapacity(defaultRingBufferCapacity)
}

// NewSessionEventHubWithCapacity creates a new SessionEventHub with custom ring buffer capacity.
func NewSessionEventHubWithCapacity(capacity int) *SessionEventHub {
	hub := &SessionEventHub{
		topics:   make(map[string]*sessionTopic),
		capacity: capacity,
		gcTicker: time.NewTicker(defaultGCPollInterval),
		stopGC:   make(chan struct{}),
	}

	go hub.runGC()
	return hub
}

func (h *SessionEventHub) runGC() {
	for {
		select {
		case <-h.gcTicker.C:
			h.gc()
		case <-h.stopGC:
			return
		}
	}
}

func (h *SessionEventHub) gc() {
	h.topicsMu.Lock()
	defer h.topicsMu.Unlock()

	now := time.Now()
	for chatID, t := range h.topics {
		t.mu.Lock()
		noSubs := len(t.subscribers) == 0
		idle := now.Sub(t.lastActivity) > defaultTopicIdleDuration
		t.mu.Unlock()

		if noSubs && idle {
			delete(h.topics, chatID)
		}
	}
}

func (h *SessionEventHub) getOrCreateTopic(chatID string) *sessionTopic {
	h.topicsMu.RLock()
	t, ok := h.topics[chatID]
	h.topicsMu.RUnlock()
	if ok {
		return t
	}

	h.topicsMu.Lock()
	defer h.topicsMu.Unlock()
	if t, ok = h.topics[chatID]; ok {
		return t
	}
	t = newSessionTopic(chatID, h.capacity)
	h.topics[chatID] = t
	return t
}

// Publish broadcasts an event to the specified chat's topic.
func (h *SessionEventHub) Publish(chatID string, ev SessionEvent) {
	if chatID == "" {
		return
	}
	ev.ChatID = chatID
	t := h.getOrCreateTopic(chatID)
	t.publish(ev)
}

// Subscribe registers a listener on the chat's event stream. If lastEventID > 0,
// missed events are replayed. If requested lastEventID was evicted from the ring
// buffer, or the topic is empty (e.g. after GC), a resync event is sent immediately.
// Returns the event channel, a done channel closed when the subscriber is disconnected,
// and a cancel function.
func (h *SessionEventHub) Subscribe(chatID string, lastEventID int64) (<-chan SessionEvent, <-chan struct{}, func()) {
	h.topicsMu.Lock()
	t, ok := h.topics[chatID]
	if !ok {
		t = newSessionTopic(chatID, h.capacity)
		h.topics[chatID] = t
	}
	t.mu.Lock()
	h.topicsMu.Unlock()
	defer t.mu.Unlock()

	t.lastActivity = time.Now()
	subID := t.nextSubID
	t.nextSubID++

	bufCap := t.capacity
	if bufCap < 128 {
		bufCap = 128
	}

	sub := &subscriber{
		id:   subID,
		ch:   make(chan SessionEvent, bufCap),
		done: make(chan struct{}),
	}

	if lastEventID > 0 {
		oldestID := t.oldestEventID()
		newestID := t.newestEventID()
		if t.count == 0 || lastEventID < oldestID || lastEventID > newestID {
			// Evicted from ring buffer, empty, or topic was GC'd: notify client to resync
			sub.ch <- SessionEvent{
				EventID:   newestID,
				ChatID:    chatID,
				Type:      "resync",
				Timestamp: time.Now().UnixMilli(),
			}
		} else {
			// Deliver missed events under topic lock before registering, guaranteeing strict monotonic order
			for i := 0; i < t.count; i++ {
				idx := (t.head + i) % t.capacity
				ev := t.events[idx]
				if ev.EventID > lastEventID {
					sub.ch <- ev
				}
			}
		}
	}

	t.subscribers[subID] = sub

	cancel := func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if s, ok := t.subscribers[subID]; ok {
			delete(t.subscribers, subID)
			close(s.done)
		}
	}

	return sub.ch, sub.done, cancel
}

// Close gracefully stops the background GC ticker and closes all active subscriber connections.
func (h *SessionEventHub) Close() {
	if h.gcTicker != nil {
		h.gcTicker.Stop()
	}
	select {
	case <-h.stopGC:
	default:
		close(h.stopGC)
	}

	h.topicsMu.Lock()
	defer h.topicsMu.Unlock()
	for _, t := range h.topics {
		t.mu.Lock()
		for id, sub := range t.subscribers {
			delete(t.subscribers, id)
			close(sub.done)
		}
		t.mu.Unlock()
	}
}
