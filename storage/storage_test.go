package storage

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conv := &Conversation{
		ID:        "abc",
		CreatedAt: time.Now().UTC(),
		Title:     "hello",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Stage3: &Stage3Entry{Model: "m", Response: "hey"}},
		},
	}
	if err := s.Save(conv); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "hello" || len(got.Messages) != 2 {
		t.Errorf("unexpected loaded conversation: %+v", got)
	}
}

func TestLoadMissing(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	_, err := s.Load("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListSortedNewestFirst(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	base := time.Now().UTC()
	for i, id := range []string{"old", "new", "mid"} {
		var ts time.Time
		switch id {
		case "old":
			ts = base.Add(-2 * time.Hour)
		case "mid":
			ts = base.Add(-1 * time.Hour)
		case "new":
			ts = base
		}
		c := &Conversation{ID: id, CreatedAt: ts, Title: id, Messages: make([]Message, i)}
		if err := s.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	metas, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 3 {
		t.Fatalf("got %d, want 3", len(metas))
	}
	want := []string{"new", "mid", "old"}
	for i, m := range metas {
		if m.ID != want[i] {
			t.Errorf("idx %d: got %q want %q", i, m.ID, want[i])
		}
	}
}

func TestPerConversationLockIsolation(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	l1 := s.Lock("a")
	l2 := s.Lock("b")
	if l1 == l2 {
		t.Error("locks for different ids should differ")
	}
	l1again := s.Lock("a")
	if l1 != l1again {
		t.Error("same id should return same lock pointer")
	}

	// Ensure concurrent lock of same id serializes
	l1.Lock()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		l1again.Lock()
		defer l1again.Unlock()
		close(done)
	}()

	select {
	case <-done:
		t.Error("second lock acquired while first held")
	case <-time.After(50 * time.Millisecond):
	}
	l1.Unlock()
	wg.Wait()
}

func TestDelete(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	c := &Conversation{ID: "tmp", CreatedAt: time.Now(), Title: "t", Messages: []Message{}}
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("tmp"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load("tmp")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete, expected ErrNotFound, got %v", err)
	}
	// Idempotent: deleting again should return ErrNotFound
	if err := s.Delete("tmp"); !errors.Is(err, ErrNotFound) {
		t.Errorf("re-delete: expected ErrNotFound, got %v", err)
	}
}
