package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const endpoint = "https://openrouter.ai/api/v1/chat/completions"

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
	reqBody := chatRequest{Model: model, Messages: messages}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", c.referer)
	req.Header.Set("X-Title", c.title)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http %s: %w", model, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp chatResponse
		if json.Unmarshal(raw, &errResp) == nil && errResp.Error != nil {
			return "", fmt.Errorf("openrouter %s: %d: %s", model, resp.StatusCode, errResp.Error.Message)
		}
		snippet := string(raw)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return "", fmt.Errorf("openrouter %s: %d: %s", model, resp.StatusCode, snippet)
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if cr.Error != nil {
		return "", fmt.Errorf("openrouter %s: %s", model, cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("openrouter %s: no choices returned", model)
	}
	content := cr.Choices[0].Message.Content
	if content == "" {
		return "", fmt.Errorf("openrouter %s: empty content", model)
	}
	return content, nil
}
