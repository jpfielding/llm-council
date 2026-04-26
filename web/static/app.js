'use strict';

const state = {
  conversations: [],
  currentId: null,
  currentConv: null,
  councilModels: [],
  chairman: '',
  busy: false,
  authToken: localStorage.getItem('auth_token') || '',
};

const $ = (id) => document.getElementById(id);

function authHeaders(extra) {
  const h = Object.assign({}, extra || {});
  if (state.authToken) h['Authorization'] = 'Bearer ' + state.authToken;
  return h;
}

function promptForToken() {
  const t = prompt('This server requires an access token. Enter it to continue:', state.authToken || '');
  if (t !== null) {
    state.authToken = t.trim();
    if (state.authToken) {
      localStorage.setItem('auth_token', state.authToken);
    } else {
      localStorage.removeItem('auth_token');
    }
    return true;
  }
  return false;
}

function modelShortName(m) {
  const parts = m.split('/');
  return parts[parts.length - 1];
}

async function fetchJSON(url, opts) {
  const o = Object.assign({}, opts || {});
  o.headers = authHeaders(o.headers);
  let resp = await fetch(url, o);
  if (resp.status === 401 && promptForToken()) {
    o.headers = authHeaders(opts && opts.headers);
    resp = await fetch(url, o);
  }
  if (!resp.ok) {
    const text = await resp.text().catch(() => '');
    throw new Error(`${resp.status} ${resp.statusText}: ${text}`);
  }
  return resp.json();
}

async function loadCouncilConfig() {
  const cfg = await fetchJSON('/api/config');
  state.councilModels = cfg.council_models || [];
  state.chairman = cfg.chairman || '';
  renderCouncilInfo();
}

function renderCouncilInfo() {
  const el = $('council-info');
  const members = state.councilModels.map((m) => {
    const isChair = m === state.chairman;
    return `<div class="member ${isChair ? 'chairman' : ''}">${isChair ? '★ ' : '• '}${modelShortName(m)}${isChair ? ' (chair)' : ''}</div>`;
  }).join('');
  el.innerHTML = '<div style="font-weight:600;margin-bottom:4px;">Council</div>' + members;
}

async function loadConversations() {
  state.conversations = await fetchJSON('/api/conversations');
  renderConvList();
}

function renderConvList() {
  const el = $('conv-list');
  if (state.conversations.length === 0) {
    el.innerHTML = '<div class="empty-state" style="padding:30px 20px;">No conversations yet</div>';
    return;
  }
  el.innerHTML = state.conversations.map((c) => `
    <div class="conv-item ${c.id === state.currentId ? 'active' : ''}" data-id="${c.id}">
      <div class="conv-row">
        <div class="conv-title">${escapeHTML(c.title || 'New conversation')}</div>
        <button class="conv-delete" data-delete-id="${c.id}" title="Delete">×</button>
      </div>
      <div class="conv-meta">${c.message_count} message${c.message_count === 1 ? '' : 's'}</div>
    </div>
  `).join('');
  el.querySelectorAll('.conv-item').forEach((node) => {
    node.addEventListener('click', (e) => {
      if (e.target.classList.contains('conv-delete')) return;
      selectConversation(node.dataset.id);
    });
  });
  el.querySelectorAll('.conv-delete').forEach((btn) => {
    btn.addEventListener('click', async (e) => {
      e.stopPropagation();
      const id = btn.dataset.deleteId;
      if (!confirm('Delete this conversation?')) return;
      await deleteConversation(id);
    });
  });
}

async function deleteConversation(id) {
  if (state.busy) return;
  let resp = await fetch(`/api/conversations/${id}`, { method: 'DELETE', headers: authHeaders() });
  if (resp.status === 401 && promptForToken()) {
    resp = await fetch(`/api/conversations/${id}`, { method: 'DELETE', headers: authHeaders() });
  }
  if (!resp.ok && resp.status !== 404) {
    alert('Failed to delete: ' + resp.status);
    return;
  }
  if (state.currentId === id) {
    state.currentId = null;
    state.currentConv = null;
  }
  await loadConversations();
  renderMessages();
}

async function selectConversation(id) {
  if (state.busy) return;
  state.currentId = id;
  state.currentConv = await fetchJSON(`/api/conversations/${id}`);
  renderConvList();
  renderMessages();
}

async function newConversation() {
  if (state.busy) return;
  const conv = await fetchJSON('/api/conversations', { method: 'POST' });
  state.currentId = conv.id;
  state.currentConv = conv;
  await loadConversations();
  renderMessages();
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function md(s) {
  if (!s) return '';
  try {
    const html = marked.parse(s);
    if (typeof DOMPurify !== 'undefined') {
      return DOMPurify.sanitize(html);
    }
    return html;
  } catch { return escapeHTML(s); }
}

function renderMessages() {
  const el = $('messages');
  if (!state.currentConv) {
    el.innerHTML = '<div class="empty-state">Create or select a conversation to begin.</div>';
    return;
  }
  if (state.currentConv.messages.length === 0) {
    el.innerHTML = '<div class="empty-state">Ask the council a question below.</div>';
    return;
  }
  el.innerHTML = state.currentConv.messages.map(renderMessage).join('');
  el.scrollTop = el.scrollHeight;
}

function renderMessage(msg, idx) {
  if (msg.role === 'user') {
    return `<div class="message user"><div class="text">${escapeHTML(msg.content)}</div></div>`;
  }
  return `<div class="message assistant" data-idx="${idx}">
    ${renderStage1(msg.stage1 || [])}
    ${renderStage2(msg.stage2 || [], msg.stage1 || [])}
    ${renderStage3(msg.stage3)}
  </div>`;
}

function renderStage1(stage1) {
  if (!stage1.length) return '';
  const tabs = stage1.map((s, i) => `
    <button class="tab-btn ${i === 0 ? 'active' : ''} ${s.error ? 'errored' : ''}" data-idx="${i}">
      ${escapeHTML(modelShortName(s.model))}
    </button>
  `).join('');
  const panes = stage1.map((s, i) => `
    <div class="tab-content" data-idx="${i}" style="${i === 0 ? '' : 'display:none;'}">
      ${s.error
        ? `<div class="stage-error">⚠ ${escapeHTML(s.error)}</div>`
        : `<div class="markdown-body">${md(s.response)}</div>`}
    </div>
  `).join('');
  return `<div class="stage stage-1">
    <div class="stage-header">Stage 1 — Council Responses</div>
    <div class="tabs">${tabs}</div>
    ${panes}
  </div>`;
}

function renderStage2(stage2, stage1) {
  if (!stage2.length) return '';
  // Build label->model mapping from stage1 (same order as backend)
  const validStage1 = stage1.filter((s) => s.response);
  const labelToModel = {};
  const modelToLabel = {};
  validStage1.forEach((s, i) => {
    const label = String.fromCharCode(65 + i);
    labelToModel[label] = s.model;
    modelToLabel[s.model] = label;
  });

  // Find best (lowest) score
  const validScores = stage2.filter((s) => !s.error).map((s) => s.aggregate_score);
  const bestScore = validScores.length ? Math.min(...validScores) : null;

  const rows = stage2.map((s) => {
    const label = modelToLabel[s.model] || '—';
    const isBest = !s.error && s.aggregate_score === bestScore;
    return `<tr class="${isBest ? 'best' : ''}">
      <td>${escapeHTML(modelShortName(s.model))}</td>
      <td>${label}</td>
      <td>${s.error ? '—' : s.aggregate_score.toFixed(2)}</td>
      <td>${s.error ? `<span class="stage-error">${escapeHTML(s.error)}</span>` : ''}</td>
    </tr>`;
  }).join('');

  // De-anonymize raw rankings (Response A -> model short name)
  const deanonymize = (text) => {
    if (!text) return '';
    let out = text;
    Object.entries(labelToModel).forEach(([label, model]) => {
      const short = modelShortName(model);
      const re = new RegExp(`Response\\s+${label}\\b`, 'gi');
      out = out.replace(re, `**${short}**`);
    });
    return out;
  };

  const rawDetails = stage2.filter((s) => s.rankings || s.error).map((s) => `
    <details class="stage-2-raw">
      <summary>${escapeHTML(modelShortName(s.model))} evaluation</summary>
      <div class="markdown-body">${md(deanonymize(s.rankings))}</div>
    </details>
  `).join('');

  return `<div class="stage stage-2">
    <div class="stage-header">Stage 2 — Peer Rankings (lower = better)</div>
    <table class="rankings-table">
      <thead><tr><th>Model</th><th>Label</th><th>Aggregate Score</th><th></th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    <div class="stage-2-details">${rawDetails}</div>
  </div>`;
}

function renderStage3(stage3) {
  if (!stage3) return '';
  return `<div class="stage stage-3">
    <div class="stage-header">Stage 3 — Chairman Synthesis</div>
    <div class="stage-3-final">
      <div class="stage-3-chairman">Chairman: ${escapeHTML(modelShortName(stage3.model))}</div>
      ${stage3.error
        ? `<div class="stage-error">⚠ ${escapeHTML(stage3.error)}</div>`
        : `<div class="markdown-body">${md(stage3.response)}</div>`}
    </div>
  </div>`;
}

// Tab switching (event delegation)
$('messages').addEventListener('click', (e) => {
  const btn = e.target.closest('.tab-btn');
  if (!btn) return;
  const container = btn.closest('.stage-1');
  const idx = btn.dataset.idx;
  container.querySelectorAll('.tab-btn').forEach((b) => b.classList.toggle('active', b.dataset.idx === idx));
  container.querySelectorAll('.tab-content').forEach((c) => {
    c.style.display = c.dataset.idx === idx ? '' : 'none';
  });
});

// SSE stream reader
async function streamMessage(id, content, onEvent) {
  const doFetch = () => fetch(`/api/conversations/${id}/message/stream`, {
    method: 'POST',
    headers: authHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ content }),
  });
  let resp = await doFetch();
  if (resp.status === 401 && promptForToken()) {
    resp = await doFetch();
  }
  if (!resp.ok) {
    const text = await resp.text().catch(() => '');
    throw new Error(`${resp.status} ${resp.statusText}: ${text}`);
  }
  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  let dataBuf = '';
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    const lines = buf.split('\n');
    buf = lines.pop();
    for (const rawLine of lines) {
      const line = rawLine.replace(/\r$/, '');
      if (line.startsWith('data: ')) {
        dataBuf += line.slice(6);
      } else if (line === '' && dataBuf) {
        try {
          onEvent(JSON.parse(dataBuf));
        } catch (err) {
          console.error('bad sse frame', dataBuf, err);
        }
        dataBuf = '';
      }
    }
  }
}

async function submitMessage(content) {
  if (!state.currentId) {
    await newConversation();
  }
  state.busy = true;
  $('send-btn').disabled = true;

  // Optimistically append user message
  const assistantMsg = { role: 'assistant', stage1: [], stage2: [], stage3: null };
  state.currentConv.messages.push({ role: 'user', content });
  state.currentConv.messages.push(assistantMsg);
  const assistantIdx = state.currentConv.messages.length - 1;

  // Pending placeholders for each council model
  assistantMsg.stage1 = state.councilModels.map((m) => ({ model: m, response: '', pending: true }));
  renderMessages();
  showLoadingPlaceholder(assistantIdx, 'Stage 1 — querying council');

  try {
    await streamMessage(state.currentId, content, (ev) => {
      if (ev.type === 'stage1_start') {
        assistantMsg.stage1 = ev.payload;
        showLoadingPlaceholder(assistantIdx, 'Stage 2 — peer rankings');
      } else if (ev.type === 'stage2_complete') {
        assistantMsg.stage2 = ev.payload;
        showLoadingPlaceholder(assistantIdx, 'Stage 3 — chairman synthesizing');
      } else if (ev.type === 'stage3_complete') {
        assistantMsg.stage3 = ev.payload;
        clearLoadingPlaceholder(assistantIdx);
      } else if (ev.type === 'title_complete') {
        state.currentConv.title = ev.payload.title || state.currentConv.title;
      } else if (ev.type === 'error') {
        const msg = (ev.payload && ev.payload.message) || JSON.stringify(ev.payload);
        showBanner('Council error: ' + msg, 'error');
      }
      renderMessages();
    });
    await loadConversations();
  } catch (err) {
    console.error(err);
    showBanner('Request failed: ' + err.message, 'error');
  } finally {
    state.busy = false;
    $('send-btn').disabled = false;
  }
}

function showLoadingPlaceholder(idx, label) {
  const messages = $('messages');
  const existing = messages.querySelector('.stream-status');
  if (existing) existing.remove();
  const el = document.createElement('div');
  el.className = 'stream-status message';
  el.innerHTML = `<span class="loading-pulse">${escapeHTML(label)}</span>`;
  messages.appendChild(el);
  messages.scrollTop = messages.scrollHeight;
}
function clearLoadingPlaceholder() {
  const existing = document.querySelector('.stream-status');
  if (existing) existing.remove();
}

function showBanner(msg, kind) {
  let banner = document.getElementById('banner');
  if (!banner) {
    banner = document.createElement('div');
    banner.id = 'banner';
    document.getElementById('main').prepend(banner);
  }
  banner.className = 'banner ' + (kind || 'info');
  banner.textContent = msg;
  banner.style.display = 'block';
  clearTimeout(banner._timer);
  banner._timer = setTimeout(() => {
    banner.style.display = 'none';
  }, 8000);
}

// Event wiring
$('new-conv-btn').addEventListener('click', newConversation);
$('input-form').addEventListener('submit', (e) => {
  e.preventDefault();
  const val = $('input').value.trim();
  if (!val) return;
  $('input').value = '';
  submitMessage(val);
});
$('input').addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    $('input-form').requestSubmit();
  }
});

// Init
(async () => {
  try {
    await loadCouncilConfig();
    await loadConversations();
    renderMessages();
  } catch (err) {
    console.error('init', err);
    $('messages').innerHTML = `<div class="empty-state stage-error">Init failed: ${escapeHTML(err.message)}</div>`;
  }
})();
