package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

type Conversation struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Title     string    `json:"title"`
	Messages  []Message `json:"messages"`
}

type Message struct {
	Role    string        `json:"role"`
	Content string        `json:"content,omitempty"`
	Stage1  []Stage1Entry `json:"stage1,omitempty"`
	Stage2  []Stage2Entry `json:"stage2,omitempty"`
	Stage3  *Stage3Entry  `json:"stage3,omitempty"`
}

type Stage1Entry struct {
	Model    string `json:"model"`
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
}

type Stage2Entry struct {
	Model          string  `json:"model"`
	Rankings       string  `json:"rankings"`
	AggregateScore float64 `json:"aggregate_score"`
	Error          string  `json:"error,omitempty"`
}

type Stage3Entry struct {
	Model    string `json:"model"`
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
}

type ConversationMeta struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	Title        string    `json:"title"`
	MessageCount int       `json:"message_count"`
}

type Store struct {
	dir   string
	rwmu  sync.RWMutex
	locks sync.Map // id -> *sync.Mutex
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Lock(id string) *sync.Mutex {
	m, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

func (s *Store) Save(c *Conversation) error {
	s.rwmu.Lock()
	defer s.rwmu.Unlock()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	final := s.path(c.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (s *Store) Load(id string) (*Conversation, error) {
	s.rwmu.RLock()
	defer s.rwmu.RUnlock()

	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var c Conversation
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &c, nil
}

func (s *Store) Delete(id string) error {
	s.rwmu.Lock()
	defer s.rwmu.Unlock()
	err := os.Remove(s.path(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	s.locks.Delete(id)
	return nil
}

func (s *Store) List() ([]ConversationMeta, error) {
	s.rwmu.RLock()
	defer s.rwmu.RUnlock()

	matches, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return nil, err
	}
	metas := make([]ConversationMeta, 0, len(matches))
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var c Conversation
		if err := json.Unmarshal(data, &c); err != nil {
			continue
		}
		metas = append(metas, ConversationMeta{
			ID:           c.ID,
			CreatedAt:    c.CreatedAt,
			Title:        c.Title,
			MessageCount: len(c.Messages),
		})
	}
	slices.SortFunc(metas, func(a, b ConversationMeta) int {
		if a.CreatedAt.After(b.CreatedAt) {
			return -1
		}
		if a.CreatedAt.Before(b.CreatedAt) {
			return 1
		}
		return 0
	})
	return metas, nil
}

var ErrNotFound = errors.New("conversation not found")
