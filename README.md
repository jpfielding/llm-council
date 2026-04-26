# llm-council

[![CI](https://github.com/jpfielding/llm-council/actions/workflows/ci.yml/badge.svg)](https://github.com/jpfielding/llm-council/actions/workflows/ci.yml)

A pure-Go port of [karpathy/llm-council](https://github.com/karpathy/llm-council).

Ask a question; a council of LLMs answers it independently, anonymously ranks each other, and a chairman model synthesizes the final answer. The whole app — API, SSE streaming, web UI, static assets, and `marked`/`DOMPurify` — ships as one static binary. No Node.js, no CGO, one external Go dependency (`github.com/google/uuid`).

---

## How it works

Each turn runs three stages, all orchestrated server-side:

1. **Stage 1 — Council responses.** Every model in `COUNCIL_MODELS` is queried in parallel. The user's question and prior history are passed to each.
2. **Stage 2 — Anonymous peer ranking.** Stage 1 responses are relabeled `Response A`, `Response B`, … and shown back to each council member. Each returns a ranked list; the server parses the rankings and computes a per-response aggregate score (lower is better). Unranked entries are assigned `n+1` so one missing entry cannot poison the average.
3. **Stage 3 — Chairman synthesis.** `CHAIRMAN_MODEL` receives the original question, all Stage 1 responses (still labeled), and the aggregate rankings, and produces the final answer.

A short title is generated in parallel by `TITLE_MODEL` on the first turn of a conversation.

Results are persisted as JSON files under `DATA_DIR/<uuid>.json`, one file per conversation, written atomically (`.tmp` + `rename`). A `sync.Map` of per-conversation mutexes serializes concurrent writes to the same conversation without blocking unrelated ones.

---

## Quick start

```bash
cp .env.example .env
# edit .env and set OPENROUTER_API_KEY

go build -buildvcs=false -o llm-council ./cmd/server
./llm-council
```

Open <http://localhost:8080>.

### Docker

```bash
docker build -t llm-council .
docker run --rm -p 8080:8080 \
  -e OPENROUTER_API_KEY=sk-or-... \
  -v "$PWD/data:/data" \
  llm-council
```

The image is multi-stage and lands on `gcr.io/distroless/static-debian12:nonroot`.

---

## Configuration

All configuration is via environment variables. A `.env` file in the working directory is loaded at startup; real environment variables always win over `.env`.

| Variable | Default | Notes |
|---|---|---|
| `OPENROUTER_API_KEY` | *(required)* | Get one at <https://openrouter.ai/keys>. |
| `PORT` | `8080` | HTTP listen port. |
| `DATA_DIR` | `./data` | Where conversation JSON files are written. |
| `COUNCIL_MODELS` | `openai/gpt-4o,google/gemini-2.5-flash,anthropic/claude-sonnet-4-5,x-ai/grok-3-mini` | Comma-separated OpenRouter model IDs. Whitespace and empty entries are trimmed. |
| `CHAIRMAN_MODEL` | `anthropic/claude-sonnet-4-5` | Synthesizes the final Stage 3 answer. |
| `TITLE_MODEL` | `google/gemini-2.5-flash` | Generates the conversation title. |
| `AUTH_TOKEN` | *(empty)* | If set, `/api/*` (except `/api/health`) requires `Authorization: Bearer <token>` or `?token=<token>`. Constant-time compared. |

---

## HTTP API

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/health` | Always reachable; returns `{"status":"ok"}`. |
| `GET` | `/api/config` | Returns `{"council_models":[...], "chairman":"..."}`. |
| `GET` | `/api/conversations` | Lists conversation metadata, newest first. |
| `POST` | `/api/conversations` | Creates an empty conversation, returns it. |
| `GET` | `/api/conversations/{id}` | Returns the full conversation. |
| `DELETE` | `/api/conversations/{id}` | Deletes a conversation. |
| `POST` | `/api/conversations/{id}/message` | Runs the council synchronously. Returns the updated conversation. |
| `POST` | `/api/conversations/{id}/message/stream` | Runs the council and streams SSE events as each stage completes. |

Request body for both `message` endpoints:

```json
{"content": "your question"}
```

### SSE frames

Each event is a single `data:` line followed by a blank line. The payload is stage-specific JSON.

| `type` | When it fires | `payload` |
|---|---|---|
| `stage1_start` | After Stage 1 completes, before Stage 2 begins | `[]Stage1Entry` |
| `stage2_complete` | After rankings are computed | `[]Stage2Entry` |
| `stage3_complete` | After the chairman response lands | `Stage3Entry` |
| `title_complete` | When a title is generated (first turn only) | `{"title":"..."}` |
| `error` | On fatal council failure | `{"message":"..."}` |

The client disconnecting cancels the underlying `context.Context`, which cancels all in-flight OpenRouter calls — no goroutine leaks.

### Example

```bash
# Unauthenticated server
curl http://localhost:8080/api/health

# Create a conversation
ID=$(curl -s -X POST http://localhost:8080/api/conversations | jq -r .id)

# Stream a response
curl -N -X POST http://localhost:8080/api/conversations/$ID/message/stream \
  -H 'Content-Type: application/json' \
  -d '{"content":"What is the meaning of life?"}'
```

With `AUTH_TOKEN=secret`, add `-H 'Authorization: Bearer secret'` (or append `?token=secret` for EventSource-style clients that cannot set headers).

---

## Reliability

- **OpenRouter retries.** `Complete()` retries transient failures (HTTP 429, 5xx, network errors) with exponential backoff (2s, 4s). Non-retryable errors (4xx besides 429, decode failures, empty content) fail fast.
- **Partial-council degradation.** A Stage 1 failure for one model is logged and recorded on the conversation, but Stage 2 and Stage 3 still run with the surviving responses. Total failure of every model is required to abort the turn.
- **Atomic persistence.** Writes go to `<id>.json.tmp` then `os.Rename` to `<id>.json`.
- **Per-conversation mutex.** Concurrent writes to the same conversation serialize; other conversations proceed unblocked.
- **SSE backpressure.** The event channel is sized `max(8, 2·len(models)+4)` so Stage 1 goroutines never block on a slow client.
- **Bounded non-streaming handlers.** `/api/conversations*` routes are wrapped in `http.TimeoutHandler(10s)`. The SSE route deliberately has `WriteTimeout: 0` since LLM calls can take minutes.

---

## Security

- `AUTH_TOKEN` gates all of `/api/*` except `/api/health`. The check is a `subtle.ConstantTimeCompare` so it doesn't leak length via timing.
- LLM output is rendered through `marked` and then sanitized by `DOMPurify` before being inserted into the DOM. A model attempting to emit a `<script>` tag or inline event handler gets it stripped.
- There is no multi-user auth, rate limiting, or CSRF protection — this is a single-user tool. Put it behind your own reverse proxy if you need any of that.
- Conversation files are stored unencrypted on disk under `DATA_DIR`.

---

## Architecture

```
cmd/server/      Entry point; signal.NotifyContext for graceful shutdown.
config/          .env + env-var loader, with validation.
storage/         JSON file store, atomic writes, per-conversation mutex via sync.Map.
openrouter/      HTTP client for OpenRouter with retry/backoff.
council/         3-stage orchestration, parallel goroutines, ranking parser.
api/             net/http handlers (Go 1.22+ patterns), SSE, CORS, auth middleware.
web/             //go:embed of templates/ and static/ (HTML, JS, CSS, marked, DOMPurify).
```

Notable design choices:

- **No `internal/`.** All packages are publicly importable — consistent with the project preference for flat layout.
- **Stdlib mux only.** Routes use Go 1.22+ patterns (`POST /api/conversations/{id}/message/stream`) with `r.PathValue("id")`. No router dependency.
- **Embedded static assets via `fs.Sub`.** Without `fs.Sub`, `/static/app.js` would resolve to `static/app.js` inside the embedded FS and 404. `fs.Sub` strips the prefix.
- **Vanilla JS front-end.** `web/static/app.js` is ~400 lines. It parses SSE by accumulating on `\n` (chunk boundaries do not align with events) and dispatches on a blank line.

---

## Development

```bash
go vet ./...
go test -race -timeout 60s ./...
go build -buildvcs=false -o llm-council ./cmd/server
```

`council/integration_test.go` stands up an `httptest` server that impersonates OpenRouter and exercises the full 3-stage flow, including partial Stage 1 failure with retries exhausted.

### CI

GitHub Actions runs `go vet`, race-enabled tests, and `go build` on every push and PR to `main`. See `.github/workflows/ci.yml`.

---

## License

MIT. See [LICENSE](LICENSE) if present, otherwise treat this repo as MIT-licensed.
