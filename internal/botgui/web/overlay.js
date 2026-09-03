const chat = document.querySelector("#chat");
const query = new URLSearchParams(location.search);
const limit = Math.min(20, Math.max(1, Number(query.get("limit")) || 10));
const ttlSeconds = Math.min(900, Math.max(15, Number(query.get("ttl")) || 120));
const ignoredAuthors = new Set(["botrix", "kickbot"]);
let rendered = new Set();

const platformIcon = platform => ({
  twitch: `<svg viewBox="0 0 24 24" aria-hidden="true"><rect width="24" height="24" rx="5" fill="#9146ff"/><path fill="#fff" d="M5 4h15v11l-4 4h-4l-3 3v-3H5V4zm3 3v9h3v3l3-3h3V7H8z"/><path fill="#9146ff" d="M11 9h2v5h-2zm4 0h2v5h-2z"/></svg>`,
  kick: `<svg viewBox="0 0 24 24" aria-hidden="true"><rect width="24" height="24" rx="5" fill="#53fc18"/><path fill="#081008" d="M5 5h5v5h2l3-5h5l-5 7 5 7h-5l-3-5h-2v5H5V5z"/></svg>`,
  youtube: `<svg viewBox="0 0 24 24" aria-hidden="true"><rect y="3" width="24" height="18" rx="5" fill="#ff0033"/><path fill="#fff" d="m10 8 7 4-7 4V8z"/></svg>`,
}[platform] || `<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="10" fill="#8aa0b8"/><path fill="#fff" d="M11 16h2v2h-2zm-2-7c.2-2.1 1.7-3.2 3.8-3.2 2.2 0 3.7 1.2 3.7 3.1 0 1.4-.7 2.2-1.9 3-.9.6-1.3 1.1-1.3 2.1h-2c0-1.5.5-2.3 1.7-3.2.9-.7 1.3-1.1 1.3-1.9 0-.8-.6-1.4-1.6-1.4s-1.6.5-1.7 1.5H9z"/></svg>`);

function safeColor(value) {
  return /^#[0-9a-f]{6}$/i.test(value || "") ? value : "";
}

function ignoredAuthor(message) {
  return ignoredAuthors.has((message.author_display_name || "").trim().toLowerCase());
}

function messageText(message) {
  const text = message.text || message.membership?.level || "";
  if (message.paid?.display) return text ? `${message.paid.display} · ${text}` : message.paid.display;
  return text;
}

function messageNode(message) {
  const row = document.createElement("div");
  row.className = `message ${message.platform || "unknown"} ${message.event_type || "message"} entering`;
  row.dataset.id = message.id;
  row.dataset.timestamp = message.timestamp;

  const identity = document.createElement("span");
  identity.className = "identity";

  const platform = document.createElement("span");
  platform.className = `platform platform-${message.platform || "unknown"}`;
  platform.innerHTML = platformIcon(message.platform);

  const author = document.createElement("span");
  author.className = "author";
  author.textContent = message.author_display_name || "Someone";
  const color = safeColor(message.author_color);
  if (color) author.style.color = color;

  const text = document.createElement("span");
  text.className = "text";
  text.textContent = messageText(message);

  identity.append(platform, author);
  row.append(identity, text);
  return row;
}

function refresh(messages) {
  const now = Date.now();
  const recent = (Array.isArray(messages) ? messages : [])
    .filter(message => message && message.id && message.event_type !== "moderation" && message.event_type !== "system")
    .filter(message => !ignoredAuthor(message))
    .filter(message => now - Date.parse(message.timestamp) <= ttlSeconds * 1000)
    .slice(0, limit)
    .reverse();

  const next = new Set(recent.map(message => message.id));
  for (const node of [...chat.children]) {
    if (!next.has(node.dataset.id)) node.remove();
  }
  for (const message of recent) {
    if (!rendered.has(message.id)) chat.append(messageNode(message));
  }
  rendered = next;
}

async function poll() {
  try {
    const response = await fetch("/api/overlay/chat", { cache: "no-store" });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const payload = await response.json();
    refresh(payload.recent_chat);
  } catch {
    // A temporary server or network interruption should leave the last useful
    // messages on screen rather than replacing chat with an error banner.
  }
}

poll();
setInterval(poll, 1000);
