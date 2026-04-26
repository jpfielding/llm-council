package council

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/jpfielding/llm-council/openrouter"
	"github.com/jpfielding/llm-council/storage"
)

type Config struct {
	Models     []string
	Chairman   string
	TitleModel string
}

type Council struct {
	cfg    Config
	client *openrouter.Client
}

func New(cfg Config, client *openrouter.Client) *Council {
	return &Council{cfg: cfg, client: client}
}

type EventType string

const (
	EventStage1Start    EventType = "stage1_start"
	EventStage2Complete EventType = "stage2_complete"
	EventStage3Complete EventType = "stage3_complete"
	EventTitleComplete  EventType = "title_complete"
	EventError          EventType = "error"
)

type Event struct {
	Type    EventType `json:"type"`
	Payload any       `json:"payload"`
}

type Result struct {
	Stage1 []storage.Stage1Entry
	Stage2 []storage.Stage2Entry
	Stage3 storage.Stage3Entry
	Title  string
}

// Run executes stages 1-3 (+ title) and emits events. Returns the final Result.
// It always closes `events` on return (when non-nil).
// If existingTitle is non-empty, title generation is skipped.
func (c *Council) Run(ctx context.Context, question string, history []storage.Message, existingTitle string, events chan<- Event) (*Result, error) {
	if events != nil {
		defer close(events)
	}
	result := &Result{}

	// Stage 1
	stage1 := c.runStage1(ctx, question, history)
	result.Stage1 = stage1
	emit(events, Event{Type: EventStage1Start, Payload: stage1})

	if err := ctx.Err(); err != nil {
		return result, err
	}

	// Title generation (non-fatal, runs concurrently with stage 2+3)
	titleCh := make(chan string, 1)
	if existingTitle == "" {
		go func() {
			title := c.generateTitle(ctx, question, stage1)
			titleCh <- title
		}()
	} else {
		titleCh <- existingTitle
		close(titleCh)
	}

	// Stage 2
	stage2, labelToModel := c.runStage2(ctx, stage1)
	result.Stage2 = stage2
	emit(events, Event{Type: EventStage2Complete, Payload: stage2})

	if err := ctx.Err(); err != nil {
		return result, err
	}

	// Stage 3
	stage3 := c.runStage3(ctx, question, stage1, stage2, labelToModel)
	result.Stage3 = stage3
	emit(events, Event{Type: EventStage3Complete, Payload: stage3})

	// Title
	select {
	case title := <-titleCh:
		if existingTitle == "" {
			result.Title = title
			emit(events, Event{Type: EventTitleComplete, Payload: map[string]string{"title": title}})
		}
	case <-ctx.Done():
	}

	return result, nil
}

func emit(ch chan<- Event, ev Event) {
	if ch == nil {
		return
	}
	select {
	case ch <- ev:
	default:
		// channel full — block to preserve ordering
		ch <- ev
	}
}

func (c *Council) runStage1(ctx context.Context, question string, history []storage.Message) []storage.Stage1Entry {
	results := make([]storage.Stage1Entry, len(c.cfg.Models))
	var wg sync.WaitGroup
	for i, model := range c.cfg.Models {
		wg.Add(1)
		go func(idx int, m string) {
			defer wg.Done()
			msgs := buildConversationMessages(question, history)
			resp, err := c.client.Complete(ctx, m, msgs)
			if err != nil {
				slog.Warn("stage1 failure", "model", m, "err", err)
				results[idx] = storage.Stage1Entry{Model: m, Error: err.Error()}
				return
			}
			results[idx] = storage.Stage1Entry{Model: m, Response: resp}
		}(i, model)
	}
	wg.Wait()
	return results
}

func buildConversationMessages(question string, history []storage.Message) []openrouter.ChatMessage {
	msgs := make([]openrouter.ChatMessage, 0, len(history)+1)
	for _, m := range history {
		if m.Role == "user" {
			msgs = append(msgs, openrouter.ChatMessage{Role: "user", Content: m.Content})
		} else if m.Role == "assistant" && m.Stage3 != nil {
			msgs = append(msgs, openrouter.ChatMessage{Role: "assistant", Content: m.Stage3.Response})
		}
	}
	msgs = append(msgs, openrouter.ChatMessage{Role: "user", Content: question})
	return msgs
}

func (c *Council) runStage2(ctx context.Context, stage1 []storage.Stage1Entry) ([]storage.Stage2Entry, map[string]string) {
	valid := make([]storage.Stage1Entry, 0, len(stage1))
	for _, s := range stage1 {
		if s.Response != "" {
			valid = append(valid, s)
		}
	}
	if len(valid) < 2 {
		out := make([]storage.Stage2Entry, len(c.cfg.Models))
		for i, m := range c.cfg.Models {
			out[i] = storage.Stage2Entry{Model: m, Error: "not enough valid stage 1 responses to rank"}
		}
		return out, nil
	}

	labels := make([]string, len(valid))
	labelToModel := make(map[string]string, len(valid))
	for i, s := range valid {
		labels[i] = string(rune('A' + i))
		labelToModel[labels[i]] = s.Model
	}

	prompt := buildStage2Prompt(valid, labels)

	allRankings := make([]map[string]int, len(c.cfg.Models))
	rawTexts := make([]string, len(c.cfg.Models))
	errs := make([]string, len(c.cfg.Models))

	var wg sync.WaitGroup
	for i, model := range c.cfg.Models {
		wg.Add(1)
		go func(idx int, m string) {
			defer wg.Done()
			text, err := c.client.Complete(ctx, m, []openrouter.ChatMessage{
				{Role: "system", Content: stage2System},
				{Role: "user", Content: prompt},
			})
			if err != nil {
				errs[idx] = err.Error()
				return
			}
			rawTexts[idx] = text
			allRankings[idx] = parseRankings(text, labels)
		}(i, model)
	}
	wg.Wait()

	// Compute aggregate score per label
	aggScores := computeAggregateScores(allRankings, labels)

	// Each model's stage2 entry records its own raw ranking text + a composite
	// "street cred" score = aggregate score of the label this model authored
	out := make([]storage.Stage2Entry, len(c.cfg.Models))
	for i, m := range c.cfg.Models {
		entry := storage.Stage2Entry{
			Model:    m,
			Rankings: rawTexts[i],
		}
		if errs[i] != "" {
			entry.Error = errs[i]
		}
		// Find label this model was assigned in Stage 1 (if it produced a valid response)
		for label, modelName := range labelToModel {
			if modelName == m {
				entry.AggregateScore = aggScores[label]
				break
			}
		}
		out[i] = entry
	}
	return out, labelToModel
}

const stage2System = `You are evaluating multiple responses to a user question. You will be shown several labeled responses (Response A, Response B, etc.). Rank them from best to worst based on accuracy, insight, and helpfulness.

Output format — ONLY a ranked list, nothing else:
1. Response X
2. Response Y
3. Response Z

Do not repeat labels. Do not add commentary. Just the ranked list.`

func buildStage2Prompt(responses []storage.Stage1Entry, labels []string) string {
	var sb strings.Builder
	sb.WriteString("Here are the responses to rank:\n\n")
	for i, entry := range responses {
		fmt.Fprintf(&sb, "=== Response %s ===\n%s\n\n", labels[i], entry.Response)
	}
	sb.WriteString("Rank them now in the specified format.")
	return sb.String()
}

var rankingLineRe = regexp.MustCompile(`^\s*(\d+)[.)]\s+(?:\*\*)?(?:[Rr]esponse\s+)?([A-Za-z])\b`)

func parseRankings(text string, labels []string) map[string]int {
	valid := make(map[string]bool, len(labels))
	for _, l := range labels {
		valid[strings.ToUpper(l)] = true
	}
	result := make(map[string]int, len(labels))
	for _, line := range strings.Split(text, "\n") {
		m := rankingLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		pos, _ := strconv.Atoi(m[1])
		if pos <= 0 {
			continue
		}
		label := strings.ToUpper(m[2])
		if !valid[label] {
			continue
		}
		if _, exists := result[label]; exists {
			continue
		}
		result[label] = pos
	}
	for _, l := range labels {
		if _, ok := result[l]; !ok {
			result[l] = len(labels) + 1
		}
	}
	return result
}

func computeAggregateScores(allRankings []map[string]int, labels []string) map[string]float64 {
	totals := make(map[string]float64, len(labels))
	counts := make(map[string]int, len(labels))
	for _, r := range allRankings {
		if r == nil {
			continue
		}
		for _, l := range labels {
			if pos, ok := r[l]; ok {
				totals[l] += float64(pos)
				counts[l]++
			}
		}
	}
	out := make(map[string]float64, len(labels))
	for _, l := range labels {
		if counts[l] > 0 {
			out[l] = totals[l] / float64(counts[l])
		} else {
			out[l] = float64(len(labels) + 1)
		}
	}
	return out
}

func (c *Council) runStage3(ctx context.Context, question string, stage1 []storage.Stage1Entry, stage2 []storage.Stage2Entry, labelToModel map[string]string) storage.Stage3Entry {
	var sb strings.Builder
	sb.WriteString("You are the Chairman of a council of AI models. Multiple council members have answered a user's question, and have ranked each other's responses. Your job: synthesize the single best final answer, drawing on the strongest points from the council.\n\n")
	fmt.Fprintf(&sb, "USER QUESTION:\n%s\n\n", question)
	sb.WriteString("COUNCIL RESPONSES:\n")
	for _, s := range stage1 {
		if s.Response == "" {
			continue
		}
		fmt.Fprintf(&sb, "\n--- %s ---\n%s\n", s.Model, s.Response)
	}

	if len(labelToModel) > 0 {
		sb.WriteString("\nPEER RANKINGS (lower score = ranked higher by peers):\n")
		for label, model := range labelToModel {
			for _, s2 := range stage2 {
				if s2.Model == model {
					fmt.Fprintf(&sb, "- %s (label %s): %.2f\n", model, label, s2.AggregateScore)
					break
				}
			}
		}
	}

	sb.WriteString("\nNow write the final synthesized answer directly to the user. Do not mention the council or the rankings. Write as if you are answering the user directly.")

	resp, err := c.client.Complete(ctx, c.cfg.Chairman, []openrouter.ChatMessage{
		{Role: "user", Content: sb.String()},
	})
	if err != nil {
		slog.Error("stage3 failure", "err", err)
		return storage.Stage3Entry{Model: c.cfg.Chairman, Error: err.Error()}
	}
	return storage.Stage3Entry{Model: c.cfg.Chairman, Response: resp}
}

func (c *Council) generateTitle(ctx context.Context, question string, stage1 []storage.Stage1Entry) string {
	fallback := question
	if len(fallback) > 60 {
		fallback = fallback[:60] + "..."
	}
	if c.cfg.TitleModel == "" {
		return fallback
	}

	prompt := fmt.Sprintf("Generate a very short (3-6 words) conversation title for this question. Output ONLY the title, nothing else.\n\nQuestion: %s", question)
	resp, err := c.client.Complete(ctx, c.cfg.TitleModel, []openrouter.ChatMessage{
		{Role: "user", Content: prompt},
	})
	if err != nil {
		slog.Warn("title generation failed", "err", err)
		return fallback
	}
	title := strings.TrimSpace(resp)
	title = strings.Trim(title, `"'`)
	if title == "" {
		return fallback
	}
	if len(title) > 80 {
		title = title[:80]
	}
	return title
}
