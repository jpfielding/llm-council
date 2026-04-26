package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const endpoint = "https://openrouter.ai/api/v1/chat/completions"

// maxRetries for transient errors (429, 5xx, network). Total attempts = maxRetries + 1.
const maxRetries = 2

type Client struct {
	apiKey     string
	httpClient *http.Client
	referer    string
	title      string
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
}

type chatChoice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 120 * time.Second},
		referer:    "http://localhost",
		title:      "llm-council-go",
	}
}

func (c *Client) Complete(ctx context.Context, model string, messages []ChatMessage) (string, error) {
	body, err := json.Marshal(chatRequest{Model: model, Messages: messages})
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<attempt) * time.Second // 2s, 4s
			slog.Warn("retrying openrouter", "model", model, "attempt", attempt, "backoff", backoff, "prev_err", lastErr)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
			}
		}
		content, retry, err := c.attempt(ctx, model, body)
		if err == nil {
			return content, nil
		}
		lastErr = err
		if !retry {
			return "", err
		}
	}
	return "", fmt.Errorf("openrouter %s: exceeded retries: %w", model, lastErr)
}

// attempt runs one HTTP call. Returns (content, retryable, err).
func (c *Client) attempt(ctx context.Context, model string, body []byte) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", false, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", c.referer)
	req.Header.Set("X-Title", c.title)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Network-level error: retryable unless context is done
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		return "", true, fmt.Errorf("http %s: %w", model, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", true, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		var errResp chatResponse
		if json.Unmarshal(raw, &errResp) == nil && errResp.Error != nil {
			return "", retry, fmt.Errorf("openrouter %s: %d: %s", model, resp.StatusCode, errResp.Error.Message)
		}
		snippet := string(raw)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return "", retry, fmt.Errorf("openrouter %s: %d: %s", model, resp.StatusCode, snippet)
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", false, fmt.Errorf("decode: %w", err)
	}
	if cr.Error != nil {
		return "", false, fmt.Errorf("openrouter %s: %s", model, cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", false, fmt.Errorf("openrouter %s: no choices returned", model)
	}
	content := cr.Choices[0].Message.Content
	if content == "" {
		return "", false, fmt.Errorf("openrouter %s: empty content", model)
	}
	return content, false, nil
}
