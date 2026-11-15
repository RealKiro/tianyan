"use strict";

const $ = (id) => document.getElementById(id);
let ws;
let roster = new Map();      // id -> student
let selected = new Set();
let fullId = null;
let controlOn = false;
let lastMoveSent = 0;

// ---------- WebSocket ----------
function connect() {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  ws = new WebSocket(proto + "//" + location.host + "/ws");
  ws.onmessage = (e) => handle(JSON.parse(e.data));
  ws.onclose = () => setTimeout(connect, 1000);
  ws.onerror = () => ws.close();
}

function send(obj) {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(obj));
}

function handle(m) {
  if (m.type === "roster") {
    roster = new Map(m.students.map((s) => [s.id, s]));
    render();
  } else if (m.type === "frame") {
    const st = roster.get(m.id);
    if (m.mode === "thumb") {
      const el = $("c-" + m.id);
      const img = el && el.querySelector("img");
      if (img && img.dataset.src !== m.jpg) {
        img.dataset.src = m.jpg;
        img.src = "data:image/jpeg;base64," + m.jpg;
      }
    } else if (m.mode === "full" && m.id === fullId) {
      $("fullImg").src = "data:image/jpeg;base64," + m.jpg;
    }
  } else if (m.type === "event" || m.type === "chat") {
    addMessage((m.from || m.id) + ": " + m.text);
  }
}

// ---------- rendering ----------
function render() {
  const grid = $("grid");
  const ids = [...roster.keys()].sort((a, b) => (roster.get(a).name || "").localeCompare(roster.get(b).name || ""));
  grid.innerHTML = "";
  for (const id of ids) {
    const st = roster.get(id);
    const card = document.createElement("div");
    card.className = "card" + (st.online ? "" : " offline") + (selected.has(id) ? " selected" : "");
    card.id = "c-" + id;
    const badges = [];
    if (st.raising) badges.push('<span class="badge raise">✋</span>');
    if (st.muted) badges.push('<span class="badge">🔇</span>');
    if (st.blacked) badges.push('<span class="badge">⬛</span>');
    if (st.locked) badges.push('<span class="badge">🔒</span>');
    card.innerHTML =
      '<div class="badges">' + badges.join("") + "</div>" +
      '<img alt="">' +
      '<div class="meta"><div class="name">' + esc(st.name || id) + "</div>" +
      '<div class="ip">' + esc(st.ip || "") + " · " + esc(st.mac || "") + " · " + (st.online ? "在线" : "离线") + "</div></div>";
    card.onclick = (e) => {
      if (e.ctrlKey || e.metaKey) {
        if (selected.has(id)) selected.delete(id); else selected.add(id);
      } else {
        selected = new Set([id]);
      }
      render();
    };
    card.ondblclick = () => openFull(id);
    grid.appendChild(card);
  }
  renderList();
  updateStatusBar();
}

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function renderList() {
  const list = $("studentList");
  list.innerHTML = "";
  const groups = {};
  for (const st of roster.values()) {
    const g = st.group || "默认组";
    (groups[g] = groups[g] || []).push(st);
  }
  for (const [group, arr] of Object.entries(groups)) {
    const gh = document.createElement("div");
    gh.className = "list-group";
    gh.textContent = group + " (" + arr.length + ")";
    list.appendChild(gh);
    for (const st of arr) {
      const row = document.createElement("div");
      row.className = "list-row" + (selected.has(st.id) ? " selected" : "") + (st.online ? "" : " offline");
      row.innerHTML =
        '<span class="dot ' + (st.online ? "on" : "off") + '">' + (st.online ? "●" : "○") + "</span>" +
        '<span class="lname">' + esc(st.name || st.id) + "</span>" +
        '<span class="lip">' + esc(st.ip || "") + "</span>" +
        (st.raising ? '<span class="chip">✋</span>' : "") +
        (st.muted ? '<span class="chip">🔇</span>' : "");
      row.onclick = (e) => {
        if (e.ctrlKey || e.metaKey) {
          if (selected.has(st.id)) selected.delete(st.id); else selected.add(st.id);
        } else {
          selected = new Set([st.id]);
        }
        render();
      };
      row.ondblclick = () => openFull(st.id);
      list.appendChild(row);
    }
  }
}

function updateStatusBar() {
  let online = 0;
  for (const st of roster.values()) if (st.online) online++;
  $("statusText").textContent = "在线 " + online + " / 共 " + roster.size + " 台";
}

function addMessage(text) {
  const box = $("messages");
  const d = document.createElement("div");
  d.className = "msg";
  d.textContent = new Date().toLocaleTimeString() + " " + text;
  box.appendChild(d);
  box.scrollTop = box.scrollHeight;
}

function selOrAll() {
  return selected.size ? [...selected] : [...roster.keys()];
}

// ---------- commands ----------
function cmd(ids, c) {
  send({ type: "cmd", ids, cmd: c });
}

function promptText(title, def) {
  return prompt(title, def || "");
}

function modal(title, bodyHTML, onOk) {
  $("modalTitle").textContent = title;
  $("modalBody").innerHTML = bodyHTML;
  $("modal").classList.remove("hidden");
  $("modalOk").onclick = () => {
    const val = onOk();
    if (val !== false) $("modal").classList.add("hidden");
  };
  $("modalCancel").onclick = () => $("modal").classList.add("hidden");
}

function setupToolbar() {
  document.querySelectorAll("[data-cmd]").forEach((b) => {
    b.onclick = () => runTool(b.dataset.cmd);
  });
}

function runTool(name) {
  const ids = selOrAll();
  switch (name) {
    case "muteAll": cmd(ids, { mute: { on: true } }); break;
    case "unmuteAll": cmd(ids, { mute: { on: false } }); break;
    case "blackAll": {
      const t = promptText("黑屏提示文字（留空为纯黑）", "请保持安静");
      cmd(ids, { black: { on: true, text: t } });
      break;
    }
    case "unblackAll": cmd(ids, { black: { on: false } }); break;
    case "lockAll": cmd(ids, { lock_input: { on: true } }); break;
    case "openUrl": {
      const u = promptText("要打开的网址（自动补 https://）");
      if (!u) return;
      cmd(ids, { open: { target: /^https?:\/\//.test(u) ? u : "https://" + u } });
      break;
    }
    case "openApp": {
      const p = promptText("要打开的应用路径或可执行文件名");
      if (!p) return;
      cmd(ids, { open: { target: p } });
      break;
    }
    case "closeApp": {
      const p = promptText("要关闭的进程名（如 notepad.exe）");
      if (!p) return;
      cmd(ids, { close: { kind: "app", target: p } });
      break;
    }
    case "closeWindow": {
      const t = promptText("要关闭的窗口标题关键词（如 bilibili、抖音）");
      if (!t) return;
      cmd(ids, { close: { kind: "window", target: t } });
      break;
    }
    case "shutdown": if (confirm("确认对所选学生执行关机？")) cmd(ids, { power: { action: "shutdown", delay: 0 } }); break;
    case "reboot": if (confirm("确认对所选学生执行重启？")) cmd(ids, { power: { action: "reboot", delay: 0 } }); break;
    case "sendFile": {
      $("fileInput").onchange = async () => {
        const f = $("fileInput").files[0];
        if (!f) return;
        const fd = new FormData();
        fd.append("file", f);
        fd.append("ids", selOrAll().join(","));
        await fetch("/api/sendfile", { method: "POST", body: fd });
        $("fileInput").value = "";
      };
      $("fileInput").click();
      break;
    }
    case "blocklist": openBlocklist(ids); break;
    case "exportCsv": location.href = "/api/export?format=csv"; break;
    case "importData": {
      $("importFile").onchange = async () => {
        const f = $("importFile").files[0];
        if (!f) return;
        await fetch("/api/import", { method: "POST", body: f });
        $("importFile").value = "";
      };
      $("importFile").click();
      break;
    }
    case "wake": {
      const mac = promptText("输入学生 MAC 地址（远程开机 / Wake-on-LAN）");
      if (!mac) return;
      fetch("/api/wol", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mac }) });
      break;
    }
  }
}

function openBlocklist(ids) {
  const body =
    '<label>模式</label><select id="blMode">' +
    '<option value="black">黑名单（屏蔽列表内）</option>' +
    '<option value="off">关闭管控</option>' +
    "</select>" +
    '<label>应用黑名单（进程名，每行一个）</label><textarea id="blApps">steam.exe\nwegame.exe\nminecraft.exe</textarea>' +
    '<label>网站黑名单（域名，每行一个）</label><textarea id="blSites">bilibili.com\ndouyin.com\niqiyi.com\nv.qq.com</textarea>' +
    '<label><input type="checkbox" id="blUsb" checked> 侦测并禁止从U盘运行游戏程序</label>';
  modal("黑白名单管控", body, () => {
    const mode = $("blMode").value;
    const apps = $("blApps").value.split("\n").map((s) => s.trim()).filter(Boolean);
    const sites = $("blSites").value.split("\n").map((s) => s.trim()).filter(Boolean);
    cmd(ids, { blocklist: { mode, apps, sites, block_usb_games: $("blUsb").checked } });
  });
}

// ---------- full view + remote control ----------
function openFull(id) {
  fullId = id;
  controlOn = false;
  $("full").classList.remove("hidden");
  $("fullControl").textContent = "🖱️ 控制: 关";
  const st = roster.get(id);
  $("fullTitle").textContent = (st ? st.name : id) + " · 远程监控/操控";
  send({ type: "view", id, mode: "full" });
}

function closeFull() {
  if (fullId) send({ type: "view", id: fullId, mode: "thumb" });
  fullId = null;
  $("full").classList.add("hidden");
  $("fullImg").src = "";
}

function setupFull() {
  $("fullClose").onclick = closeFull;
  $("fullControl").onclick = () => {
    controlOn = !controlOn;
    $("fullControl").textContent = controlOn ? "🖱️ 控制: 开" : "🖱️ 控制: 关";
  };
  $("fullBlack").onclick = () => { const t = promptText("黑屏文字", "请保持安静"); cmd([fullId], { black: { on: true, text: t } }); };
  $("fullMute").onclick = () => cmd([fullId], { mute: { on: true } });
  $("fullLock").onclick = () => cmd([fullId], { lock_input: { on: true } });
  $("fullShutdown").onclick = () => { if (confirm("确认关机该学生机？")) cmd([fullId], { power: { action: "shutdown", delay: 0 } }); };
  $("fullOpen").onclick = () => { const u = promptText("打开网址"); if (u) cmd([fullId], { open: { target: /^https?:\/\//.test(u) ? u : "https://" + u } }); };
  $("fullMsg").onclick = () => { const t = promptText("发送消息给该学生"); if (t) cmd([fullId], { chat: { text: t } }); };

  const img = $("fullImg");
  const pos = (e) => {
    const r = img.getBoundingClientRect();
    const scale = img.naturalWidth / r.width;
    return { x: Math.round((e.clientX - r.left) * scale), y: Math.round((e.clientY - r.top) * scale) };
  };
  img.onmousemove = (e) => {
    if (!controlOn || !fullId) return;
    const now = Date.now();
    if (now - lastMoveSent < 30) return;
    lastMoveSent = now;
    const p = pos(e);
    send({ type: "input", id: fullId, input: { kind: "mouse", x: p.x, y: p.y } });
  };
  img.onmousedown = (e) => {
    if (!controlOn || !fullId) return;
    e.preventDefault();
    const p = pos(e);
    send({ type: "input", id: fullId, input: { kind: "mouse", x: p.x, y: p.y, btn: e.button === 2 ? "right" : "left", down: true } });
  };
  img.onmouseup = (e) => {
    if (!controlOn || !fullId) return;
    e.preventDefault();
    const p = pos(e);
    send({ type: "input", id: fullId, input: { kind: "mouse", x: p.x, y: p.y, btn: e.button === 2 ? "right" : "left", down: false } });
  };
  img.oncontextmenu = (e) => e.preventDefault();
  img.onwheel = (e) => {
    if (!controlOn || !fullId) return;
    e.preventDefault();
    send({ type: "input", id: fullId, input: { kind: "wheel", dy: Math.sign(e.deltaY) } });
  };
  document.addEventListener("keydown", keyHandler);
  document.addEventListener("keyup", keyHandler);
}

function keyHandler(e) {
  if (!controlOn || !fullId) return;
  const tag = (e.target.tagName || "").toLowerCase();
  if (tag === "input" || tag === "textarea") return;
  e.preventDefault();
  send({ type: "input", id: fullId, input: { kind: "key", key: e.code, down: e.type === "keydown" } });
}

// ---------- boot ----------
setupToolbar();
setupFull();
connect();
