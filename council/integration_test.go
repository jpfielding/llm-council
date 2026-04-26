package council

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpfielding/llm-council/openrouter"
)

// mockOpenRouter returns responses tailored to the model and prompt content,
// so we can validate each stage of the council workflow independently.
func mockOpenRouter(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)

		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Decide response content by inspecting the prompt.
		lastContent := ""
		if len(body.Messages) > 0 {
			lastContent = body.Messages[len(body.Messages)-1].Content
		}
		var reply string
		switch {
		case strings.Contains(lastContent, "Generate a very short"):
			reply = "Test Title About Life"
		case strings.Contains(lastContent, "=== Response A ===") || strings.Contains(lastContent, "Rank them now"):
			// Stage 2: return a valid ranking list. Each model picks a slightly
			// different order so aggregate scores differ per label.
			switch body.Model {
			case "model/a":
				reply = "1. Response A\n2. Response B\n3. Response C"
			case "model/b":
				reply = "1) Response B\n2) Response A\n3) Response C"
			case "model/c":
				reply = "1. **Response A**\n2. **Response C**\n3. **Response B**"
			default:
				reply = "1. Response A\n2. Response B\n3. Response C"
			}
		case body.Model == "model/chairman":
			reply = "# Final Answer\n\nSynthesized chairman response to: " + lastContent[:min(40, len(lastContent))]
		default:
			// Stage 1: model-specific response
			reply = fmt.Sprintf("Response from %s: %s", body.Model, lastContent)
		}

		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": reply}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestCouncilRunEndToEnd(t *testing.T) {
	srv, calls := mockOpenRouter(t)

	client := openrouter.NewClient("test-key")
	client.SetEndpoint(srv.URL)
	client.SetTimeout(5 * time.Second)

	c := New(Config{
		Models:     []string{"model/a", "model/b", "model/c"},
		Chairman:   "model/chairman",
		TitleModel: "model/title",
	}, client)

	events := make(chan Event, 16)
	result, err := c.Run(context.Background(), "What is the meaning of life?", nil, "", events)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Drain events and collect their types
	var eventTypes []EventType
	for ev := range events {
		eventTypes = append(eventTypes, ev.Type)
	}

	// Verify event sequence: stage1 -> stage2 -> stage3 -> title
	wantSeq := []EventType{EventStage1Start, EventStage2Complete, EventStage3Complete, EventTitleComplete}
	if len(eventTypes) != len(wantSeq) {
		t.Fatalf("got %d events (%v), want %d (%v)", len(eventTypes), eventTypes, len(wantSeq), wantSeq)
	}
	for i, want := range wantSeq {
		if eventTypes[i] != want {
			t.Errorf("event %d: got %s want %s", i, eventTypes[i], want)
		}
	}

	// Stage 1: all three models should have responses
	if len(result.Stage1) != 3 {
		t.Fatalf("stage1: got %d responses, want 3", len(result.Stage1))
	}
	for _, s := range result.Stage1 {
		if s.Error != "" {
			t.Errorf("stage1 model %s has error: %s", s.Model, s.Error)
		}
		if !strings.Contains(s.Response, "Response from "+s.Model) {
			t.Errorf("stage1 model %s: unexpected response %q", s.Model, s.Response)
		}
	}

	// Stage 2: each model produced a ranking + aggregate score
	if len(result.Stage2) != 3 {
		t.Fatalf("stage2: got %d entries, want 3", len(result.Stage2))
	}
	for _, s := range result.Stage2 {
		if s.Error != "" {
			t.Errorf("stage2 model %s has error: %s", s.Model, s.Error)
		}
		if s.Rankings == "" {
			t.Errorf("stage2 model %s: empty rankings", s.Model)
		}
		if s.AggregateScore <= 0 {
			t.Errorf("stage2 model %s: aggregate score should be > 0, got %f", s.Model, s.AggregateScore)
		}
	}

	// Aggregate scores: model/a should rank best (1, 2, 1 from mocks -> avg 1.33)
	var scoreA, scoreB, scoreC float64
	for _, s := range result.Stage2 {
		switch s.Model {
		case "model/a":
			scoreA = s.AggregateScore
		case "model/b":
			scoreB = s.AggregateScore
		case "model/c":
			scoreC = s.AggregateScore
		}
	}
	if !(scoreA < scoreB) {
		t.Errorf("expected model/a (%.2f) to rank better than model/b (%.2f)", scoreA, scoreB)
	}
	if !(scoreA < scoreC) {
		t.Errorf("expected model/a (%.2f) to rank better than model/c (%.2f)", scoreA, scoreC)
	}

	// Stage 3: chairman synthesis
	if result.Stage3.Model != "model/chairman" {
		t.Errorf("stage3 model: got %q want %q", result.Stage3.Model, "model/chairman")
	}
	if !strings.Contains(result.Stage3.Response, "Synthesized chairman response") {
		t.Errorf("stage3 response unexpected: %q", result.Stage3.Response)
	}

	// Title
	if result.Title != "Test Title About Life" {
		t.Errorf("title: got %q", result.Title)
	}

	// Total call count: 3 (stage1) + 3 (stage2) + 1 (stage3) + 1 (title) = 8
	if got := atomic.LoadInt64(calls); got != 8 {
		t.Errorf("total OpenRouter calls: got %d want 8", got)
	}
}

func TestCouncilRunHandlesPartialStage1Failures(t *testing.T) {
	// Mock: model/b returns 500, others succeed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		if body.Model == "model/b" {
			http.Error(w, `{"error":{"message":"server exploded"}}`, http.StatusInternalServerError)
			return
		}
		lastContent := ""
		if len(body.Messages) > 0 {
			lastContent = body.Messages[len(body.Messages)-1].Content
		}
		var reply string
		if body.Model == "model/chairman" {
			reply = "synthesized"
		} else if strings.Contains(lastContent, "Rank them now") {
			reply = "1. Response A\n2. Response B"
		} else if strings.Contains(lastContent, "Generate a very short") {
			reply = "T"
		} else {
			reply = "response from " + body.Model
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
		})
	}))
	defer srv.Close()

	client := openrouter.NewClient("test-key")
	client.SetEndpoint(srv.URL)
	client.SetTimeout(10 * time.Second) // allow time for retries (2s + 4s)

	c := New(Config{
		Models:     []string{"model/a", "model/b", "model/c"},
		Chairman:   "model/chairman",
		TitleModel: "model/title",
	}, client)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := c.Run(ctx, "question", nil, "existing-title", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// model/b should be marked errored in stage1
	var bErr string
	for _, s := range result.Stage1 {
		if s.Model == "model/b" {
			bErr = s.Error
		}
	}
	if bErr == "" {
		t.Error("expected model/b to have an error after retries exhausted")
	}

	// Stage 3 should still run (with the 2 valid responses)
	if result.Stage3.Response == "" {
		t.Error("stage3 should still run when some stage1 models fail")
	}

	// Title skipped because existingTitle was provided
	if result.Title != "" {
		t.Errorf("expected empty Title (existing provided), got %q", result.Title)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
