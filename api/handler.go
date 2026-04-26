package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jpfielding/llm-council/council"
	"github.com/jpfielding/llm-council/storage"
	"github.com/jpfielding/llm-council/web"
	"github.com/google/uuid"
)

type Handler struct {
	store    *storage.Store
	council  *council.Council
	models   []string
	chairman string
	tmpl     *template.Template
}

type Config struct {
	Store    *storage.Store
	Council  *council.Council
	Models   []string
	Chairman string
}

func New(cfg Config) (*Handler, error) {
	tmpl, err := template.ParseFS(web.FS, "templates/index.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Handler{
		store:    cfg.Store,
		council:  cfg.Council,
		models:   cfg.Models,
		chairman: cfg.Chairman,
		tmpl:     tmpl,
	}, nil
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	staticSub, err := fs.Sub(web.FS, "static")
	if err != nil {
		panic(fmt.Errorf("fs.Sub static: %w", err))
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))

	mux.HandleFunc("GET /{$}", h.handleIndex)
	mux.HandleFunc("GET /api/health", h.handleHealth)
	mux.HandleFunc("GET /api/config", h.handleConfig)
	mux.HandleFunc("GET /api/conversations", withTimeout(10*time.Second, h.handleListConversations))
	mux.HandleFunc("POST /api/conversations", withTimeout(10*time.Second, h.handleCreateConversation))
	mux.HandleFunc("GET /api/conversations/{conversation_id}", withTimeout(10*time.Second, h.handleGetConversation))
	mux.HandleFunc("POST /api/conversations/{conversation_id}/message", h.handleSendMessage)
	mux.HandleFunc("POST /api/conversations/{conversation_id}/message/stream", h.handleSendMessageStream)

	return corsMiddleware(mux)
}

func withTimeout(d time.Duration, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		h(w, r.WithContext(ctx))
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.Execute(w, nil); err != nil {
		slog.Error("template execute", "err", err)
	}
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"council_models": h.models,
		"chairman":       h.chairman,
	})
}

func (h *Handler) handleListConversations(w http.ResponseWriter, r *http.Request) {
	metas, err := h.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, metas)
}

func (h *Handler) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	id := uuid.NewString()
	conv := &storage.Conversation{
		ID:        id,
		CreatedAt: time.Now().UTC(),
		Title:     "",
		Messages:  []storage.Message{},
	}
	if err := h.store.Save(conv); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, conv)
}

func (h *Handler) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("conversation_id")
	conv, err := h.store.Load(id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, conv)
}

type sendMessageRequest struct {
	Content string `json:"content"`
}

// handleSendMessage runs the council and returns a single JSON response.
func (h *Handler) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("conversation_id")
	var req sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	mu := h.store.Lock(id)
	mu.Lock()
	defer mu.Unlock()

	conv, err := h.store.Load(id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	conv.Messages = append(conv.Messages, storage.Message{Role: "user", Content: req.Content})
	history := conv.Messages[:len(conv.Messages)-1]

	result, err := h.council.Run(r.Context(), req.Content, history, conv.Title, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	stage3 := result.Stage3
	conv.Messages = append(conv.Messages, storage.Message{
		Role:   "assistant",
		Stage1: result.Stage1,
		Stage2: result.Stage2,
		Stage3: &stage3,
	})
	if conv.Title == "" && result.Title != "" {
		conv.Title = result.Title
	}
	if err := h.store.Save(conv); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, conv)
}

// handleSendMessageStream runs the council and streams SSE events as each stage completes.
func (h *Handler) handleSendMessageStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("conversation_id")
	var req sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	mu := h.store.Lock(id)
	mu.Lock()
	defer mu.Unlock()

	conv, err := h.store.Load(id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	conv.Messages = append(conv.Messages, storage.Message{Role: "user", Content: req.Content})
	history := conv.Messages[:len(conv.Messages)-1]

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)

	sendEvent := func(eventType string, payload any) error {
		data, err := json.Marshal(map[string]any{"type": eventType, "payload": payload})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		return rc.Flush()
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	bufSize := len(h.models)*2 + 4
	if bufSize < 8 {
		bufSize = 8
	}
	events := make(chan council.Event, bufSize)

	type runResult struct {
		result *council.Result
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		res, err := h.council.Run(ctx, req.Content, history, conv.Title, events)
		done <- runResult{result: res, err: err}
	}()

	var runErr error
	for ev := range events {
		if err := sendEvent(string(ev.Type), ev.Payload); err != nil {
			slog.Warn("sse client disconnected", "err", err)
			cancel()
			runErr = err
			break
		}
	}
	rr := <-done
	if runErr == nil && rr.err != nil && !errors.Is(rr.err, context.Canceled) {
		_ = sendEvent(string(council.EventError), map[string]string{"message": rr.err.Error()})
		runErr = rr.err
	}

	if runErr == nil && rr.result != nil {
		stage3 := rr.result.Stage3
		conv.Messages = append(conv.Messages, storage.Message{
			Role:   "assistant",
			Stage1: rr.result.Stage1,
			Stage2: rr.result.Stage2,
			Stage3: &stage3,
		})
		if conv.Title == "" && rr.result.Title != "" {
			conv.Title = rr.result.Title
		}
		if err := h.store.Save(conv); err != nil {
			slog.Error("save conversation", "err", err)
		}
	}
}
