# llm-council (Go port)

Pure Go port of [karpathy/llm-council](https://github.com/karpathy/llm-council). A council of LLMs answer a question, rank each other, and a chairman model synthesizes the final answer. Single binary — no Node.js, no CGO.

## Setup

```bash
cp .env.example .env
# edit .env and set OPENROUTER_API_KEY
go build -o llm-council ./cmd/server
./llm-council
```

Open <http://localhost:8080>.

## Configuration

All via env vars (or `.env`):

| Var | Default |
|---|---|
| `OPENROUTER_API_KEY` | *(required)* |
| `PORT` | `8080` |
| `DATA_DIR` | `./data` |
| `COUNCIL_MODELS` | `openai/gpt-4o,google/gemini-2.5-flash,anthropic/claude-sonnet-4-5,x-ai/grok-3-mini` |
| `CHAIRMAN_MODEL` | `anthropic/claude-sonnet-4-5` |
| `TITLE_MODEL` | `google/gemini-2.5-flash` |
| `AUTH_TOKEN` | *(empty = no auth)* — if set, required on /api/* via `Authorization: Bearer <token>` or `?token=<token>` |

## API

- `GET /api/health`
- `GET /api/config` — returns council model list and chairman
- `GET /api/conversations` — list metadata
- `POST /api/conversations` — create empty conversation
- `GET /api/conversations/{id}` — full conversation
- `DELETE /api/conversations/{id}` — delete a conversation
- `POST /api/conversations/{id}/message` — run council synchronously, return full result
- `POST /api/conversations/{id}/message/stream` — run council, stream SSE events

SSE event types: `stage1_start`, `stage2_complete`, `stage3_complete`, `title_complete`, `error`.

## Architecture

- `config/` — env + `.env` loader
- `storage/` — JSON file store, atomic writes, per-conversation mutex
- `openrouter/` — HTTP client for OpenRouter
- `council/` — 3-stage orchestration with parallel goroutines
- `api/` — HTTP handlers, SSE streaming
- `web/` — embedded templates + static assets (HTML, vanilla JS, CSS, `marked.min.js`)
- `cmd/server/` — entry point
