const chat = document.querySelector("#chat");
const query = new URLSearchParams(location.search);
const limit = Math.min(20, Math.max(1, Number(query.get("limit")) || 10));
const ttlSeconds = Math.min(900, Math.max(15, Number(query.get("ttl")) || 120));
const mutedAuthors = new Set(
  (query.get("mute") || "")
    .split(",")
    .map(value => value.trim().toLocaleLowerCase())
    .filter(Boolean),
);
let rendered = new Set();

const platformLabel = platform => ({ kick: "K", twitch: "T", youtube: "YT" }[platform] || "?");

function safeColor(value) {
  return /^#[0-9a-f]{6}$/i.test(value || "") ? value : "";
}

function messageNode(message) {
  const row = document.createElement("div");
  row.className = `message ${message.platform || "unknown"} ${message.event_type || "message"} entering`;
  row.dataset.id = message.id;
  row.dataset.timestamp = message.timestamp;

  const platform = document.createElement("span");
  platform.className = "platform";
  platform.textContent = platformLabel(message.platform);

  const author = document.createElement("span");
  author.className = "author";
  author.textContent = message.author_display_name || "Someone";
  const color = safeColor(message.author_color);
  if (color) author.style.color = color;

  const text = document.createElement("span");
  text.className = "text";
  text.textContent = message.text || message.membership?.level || message.paid?.display || "";

  row.append(platform, author, text);
  return row;
}

function refresh(messages) {
  const now = Date.now();
  const recent = (Array.isArray(messages) ? messages : [])
    .filter(message => message && message.id && message.event_type !== "moderation" && message.event_type !== "system")
    .filter(message => {
      const login = String(message.author_login || "").toLocaleLowerCase();
      const displayName = String(message.author_display_name || "").toLocaleLowerCase();
      return !mutedAuthors.has(login) && !mutedAuthors.has(displayName);
    })
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
