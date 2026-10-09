package local

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/workflow"
)

// Bus fans events out to connected UI windows.
type Bus struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[chan []byte]struct{}{}} }

func (b *Bus) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 2048)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *Bus) PublishRaw(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- data:
		default: // a stuck UI window shouldn't block the core
		}
	}
}

// PublishLocal sends a core-originated event (type "local.*").
func (b *Bus) PublishLocal(typ string, data any) {
	ev := api.Event{Type: typ, CreatedAt: time.Now().UTC()}
	if data != nil {
		ev.Data = mustMarshal(data)
	}
	raw, _ := json.Marshal(ev)
	b.PublishRaw(raw)
}

func readAll(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}

func stageDescriptions() []stageDescription {
	out := make([]stageDescription, len(workflow.Stages))
	for i, d := range workflow.Stages {
		out[i] = stageDescription{ID: d.ID, Title: d.Title, Owner: d.Owner, Primary: d.Primary}
	}
	return out
}
