package scheduler

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Phase string

const (
	PhaseAttempted Phase = "attempted"
	PhaseAcked     Phase = "acked"
	PhaseFailed    Phase = "failed"
	PhaseSkipped   Phase = "skipped"
)

// Record is one line in the append-only dispatch log.
type Record struct {
	TS      time.Time `json:"ts"`
	ID      string    `json:"id"`
	Version string    `json:"version"`
	Phase   Phase     `json:"phase"`
	Nonce   string    `json:"nonce"`
	Pane    string    `json:"pane,omitempty"`
	Surface string    `json:"surface,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Error   string    `json:"error,omitempty"`
}

func recordKey(id, version string) string { return id + "|" + version }

// Ledger is an append-only JSONL file. It is deliberately human readable so the
// outbox can be inspected and backed up with ordinary tools.
type Ledger struct {
	path   string
	mu     sync.Mutex
	tail   []Record
	acked  map[string]Record
	latest map[string]Record
}

func OpenLedger(dir string) (*Ledger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	l := &Ledger{
		path:   filepath.Join(dir, "dispatch-log.jsonl"),
		acked:  map[string]Record{},
		latest: map[string]Record{},
	}
	f, err := os.Open(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return l, nil
		}
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		l.replay(r)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return l, nil
}

func (l *Ledger) replay(r Record) {
	key := recordKey(r.ID, r.Version)
	l.latest[key] = r
	switch r.Phase {
	case PhaseAcked:
		l.acked[key] = r
	case PhaseFailed, PhaseSkipped:
		delete(l.acked, key)
	}
	l.tail = append(l.tail, r)
	if len(l.tail) > 200 {
		l.tail = l.tail[len(l.tail)-200:]
	}
}

func (l *Ledger) Append(r Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.TS.IsZero() {
		r.TS = time.Now()
	}
	if r.Nonce == "" {
		r.Nonce = newNonce()
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	l.replay(r)
	return nil
}

// Acked reports a delivery confirmed for this exact revision, which is the
// guard that keeps a restart from sending the same message twice.
func (l *Ledger) Acked(id, version string) (Record, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.acked[recordKey(id, version)]
	return r, ok
}

// Open returns attempts that never reached a terminal phase. After a crash these
// are the sends whose outcome is unknown.
func (l *Ledger) Open(id, version string) (Record, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.latest[recordKey(id, version)]
	if !ok || r.Phase != PhaseAttempted {
		return Record{}, false
	}
	return r, true
}

func (l *Ledger) Tail(n int) []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.tail) {
		n = len(l.tail)
	}
	out := make([]Record, n)
	copy(out, l.tail[len(l.tail)-n:])
	return out
}

func (l *Ledger) Path() string { return l.path }

func newNonce() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
