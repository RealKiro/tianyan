"use strict";

const $ = (id) => document.getElementById(id);
let ws;
let roster = new Map();      // id -> student
let selected = new Set();
let fullId = null;
let controlOn = false;
let lastMoveSent = 0;
const fileLog = [];

// 屏幕广播状态（服务器推送）
let demoState = { active: false, fullscreen: false, source: "", sourceName: "" };
// 监控筛选（参考 Veyon Monitoring 的搜索/分组/排序/隐藏离线）
let query = "", groupFilter = "", sortBy = "name", hideOffline = false;

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
    refreshGroupOptions();
    render();
  } else if (m.type === "frame") {
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
  } else if (m.type === "demo") {
    demoState = { active: m.active, fullscreen: m.fullscreen, source: m.source, sourceName: m.source_name || "教师屏幕" };
    render();
  } else if (m.type === "broadcast_req") {
    renderBroadcastReqs(m.reqs || []);
  } else if (m.type === "event") {
    if (m.kind === "screenshot") {
      const text = "📸 截图已保存（" + (m.from || m.id) + "）：" + m.text;
      addMessage(text);
      logFile(text);
    } else if (m.kind === "error") {
      addMessage("⚠️ " + (m.from || "教师") + "：" + m.text);
    } else {
      const text = (m.from || m.id) + ": " + m.text;
      addMessage(text);
      if (/作业|文件|提交/.test(m.text || "")) logFile(text);
    }
  } else if (m.type === "chat") {
    const text = (m.from || m.id) + ": " + m.text;
    addMessage(text);
    if (/作业|文件|提交/.test(m.text || "")) logFile(text);
  }
}

// ---------- rendering ----------
// filteredStudents applies the search/group/sort/hide-offline controls and
// returns [id, student] pairs in display order.
function filteredStudents() {
  let arr = [...roster.entries()];
  const q = query.trim().toLowerCase();
  arr = arr.filter(([id, st]) => {
    if (hideOffline && !st.online) return false;
    if (groupFilter && (st.group || "默认组") !== groupFilter) return false;
    if (!q) return true;
    const hay = ((st.name || "") + " " + (st.ip || "") + " " + (st.student_id || "") + " " + (st.user || "")).toLowerCase();
    return hay.includes(q);
  });
  arr.sort((a, b) => {
    if (sortBy === "ip") return (a[1].ip || "").localeCompare(b[1].ip || "", undefined, { numeric: true });
    if (sortBy === "id") return (a[1].student_id || a[0]).localeCompare(b[1].student_id || b[0], undefined, { numeric: true });
    return (a[1].name || a[0]).localeCompare(b[1].name || b[0], "zh");
  });
  return arr;
}

function render() {
  const grid = $("grid");
  grid.innerHTML = "";
  for (const [id, st] of filteredStudents()) {
    const card = document.createElement("div");
    card.className = "card" + (st.online ? "" : " offline") + (selected.has(id) ? " selected" : "");
    card.id = "c-" + id;
    const badges = [];
    if (demoState.active && demoState.source === id) badges.push('<span class="badge demo-src">📣 源</span>');
    else if (st.demoing) badges.push('<span class="badge demo">📺</span>');
    if (st.exempt) badges.push('<span class="badge exempt">🔓</span>');
    if (st.raising) badges.push('<span class="badge raise">✋</span>');
    if (st.muted) badges.push('<span class="badge">🔇</span>');
    if (st.blacked || st.locked) badges.push('<span class="badge lock">🔒</span>');
    const info = [st.ip || "", st.user || "", st.online ? "在线" : "离线"].filter(Boolean).join(" · ");
    card.innerHTML =
      '<div class="badges">' + badges.join("") + "</div>" +
      '<img alt="">' +
      '<div class="meta"><div class="name">' + esc(st.name || id) + "</div>" +
      '<div class="ip">' + esc(info) + "</div></div>";
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
  updateDemoStatus();
}

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function renderList() {
  const list = $("studentList");
  list.innerHTML = "";
  const groups = {};
  for (const [, st] of filteredStudents()) {
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
        '<span class="lip">' + esc(st.ip || "") + (st.user ? " · " + esc(st.user) : "") + "</span>" +
        (st.demoing ? '<span class="chip">📺</span>' : "") +
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

function updateDemoStatus() {
  const el = $("demoStatus");
  if (!el) return;
  if (demoState.active) {
    el.textContent = "📺 正在广播：" + demoState.sourceName + "（" + (demoState.fullscreen ? "全屏 · 已锁键鼠" : "窗口") + "）";
    el.classList.remove("hidden");
  } else {
    el.textContent = "";
    el.classList.add("hidden");
  }
}

// refreshGroupOptions rebuilds the group filter dropdown from the roster.
function refreshGroupOptions() {
  const sel = $("grpFilter");
  if (!sel) return;
  const groups = new Set();
  for (const st of roster.values()) groups.add(st.group || "默认组");
  sel.innerHTML = '<option value="">全部分组</option>' +
    [...groups].sort().map((g) => '<option value="' + esc(g) + '">' + esc(g) + "</option>").join("");
  if ([...groups].includes(groupFilter)) sel.value = groupFilter;
  else { sel.value = ""; groupFilter = ""; }
}

function addMessage(text) {
  const box = $("messages");
  const d = document.createElement("div");
  d.className = "msg";
  d.textContent = new Date().toLocaleTimeString() + " " + text;
  box.appendChild(d);
  box.scrollTop = box.scrollHeight;
}

function logFile(text) {
  fileLog.unshift(new Date().toLocaleTimeString() + " " + text);
  if (fileLog.length > 50) fileLog.pop();
  renderFileLog();
}

function renderFileLog() {
  const el = $("sec-files");
  if (!el) return;
  el.innerHTML = fileLog.length
    ? fileLog.map((t) => '<div class="file-item">' + esc(t) + "</div>").join("")
    : '<div class="hint">暂无文件分发/接收记录</div>';
}

function notReady(name) {
  addMessage("提示：" + name + " 功能将在后续版本提供");
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

function modal(title, bodyHTML, onOk, okLabel) {
  $("modalTitle").textContent = title;
  $("modalBody").innerHTML = bodyHTML;
  $("modalOk").textContent = okLabel || "确定";
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
    case "broadcast": openBroadcast(); break;
    case "screenshot": cmd(ids, { screenshot: {} }); break;
    case "lockScreen": openLockScreen(ids); break;
    case "exam": notReady("考试"); break;
    case "fileSubmit": notReady("文件提交（收集作业）"); break;
    case "muteAll": cmd(ids, { mute: { on: true } }); break;
    case "unmuteAll": cmd(ids, { mute: { on: false } }); break;
    case "remoteCmd": openRemoteCmd(); break;
    case "shutdown": if (confirm("确认对所选学生执行关机？")) cmd(ids, { power: { action: "shutdown", delay: 0 } }); break;
    case "reboot": if (confirm("确认对所选学生执行重启？")) cmd(ids, { power: { action: "reboot", delay: 0 } }); break;
    case "wake": {
      const mac = promptText("输入学生 MAC 地址（远程开机 / Wake-on-LAN）");
      if (mac) fetch("/api/wol", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mac }) });
      break;
    }
    case "sendFile": {
      $("fileInput").onchange = async () => {
        const f = $("fileInput").files[0];
        if (!f) return;
        const fd = new FormData();
        fd.append("file", f);
        const ids2 = selOrAll();
        fd.append("ids", ids2.join(","));
        await fetch("/api/sendfile", { method: "POST", body: fd });
        logFile("已分发文件 " + f.name + " → " + ids2.length + " 台");
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
  }
}

// renderBroadcastReqs shows the pending student broadcast requests in the
// approval section; the teacher must approve before a student screen is
// relayed to the class.
function renderBroadcastReqs(reqs) {
  const box = $("broadcastReqs");
  const badge = $("reqBadge");
  if (!box) return;
  if (!reqs.length) {
    box.innerHTML = '<div class="hint">学生请求广播自己的屏幕时，会在此等待教师批准</div>';
    if (badge) badge.classList.add("hidden");
    return;
  }
  if (badge) {
    badge.textContent = reqs.length;
    badge.classList.remove("hidden");
  }
  box.innerHTML = reqs.map((r) =>
    '<div class="req-item">' +
    '<div class="req-name">' + esc(r.name || r.id) + '<span class="req-ip">' + esc(r.ip || "") + "</span></div>" +
    '<div class="req-actions">' +
    '<button data-reply="full" data-id="' + esc(r.id) + '">📺 全屏广播</button>' +
    '<button data-reply="win" data-id="' + esc(r.id) + '">🪟 窗口广播</button>' +
    '<button data-reply="deny" data-id="' + esc(r.id) + '" class="deny">✖ 拒绝</button>' +
    "</div></div>").join("");
  box.querySelectorAll("button[data-reply]").forEach((b) => {
    b.onclick = () => {
      const id = b.dataset.id;
      if (b.dataset.reply === "deny") {
        send({ type: "broadcast_reply", id, approved: false, fullscreen: false });
        addMessage("已拒绝 " + id + " 的广播请求");
      } else {
        const fullscreen = b.dataset.reply === "full";
        send({ type: "broadcast_reply", id, approved: true, fullscreen });
        addMessage("已批准 " + id + " 的广播请求（" + (fullscreen ? "全屏" : "窗口") + "）");
      }
    };
  });
  // 展开所在 section 便于教师看到
  const sec = box.closest(".section");
  if (sec) sec.classList.add("open");
}

// openBroadcast shows the screen broadcast dialog (Veyon demo equivalents:
// share own screen / share selected user's screen, fullscreen or window).
function openBroadcast() {
  if (demoState.active) {
    modal("屏幕广播",
      '<div class="hint">当前正在广播：<b>' + esc(demoState.sourceName) + "</b>（" +
      (demoState.fullscreen ? "全屏 · 已锁定学生键鼠" : "窗口模式") + "）</div>",
      () => cmd([], { demo: { on: false } }),
      "⏹ 停止广播");
    return;
  }
  const online = [...roster.values()].filter((s) => s.online);
  const opts = online.map((s) =>
    '<option value="' + esc(s.id) + '">' + esc(s.name || s.id) + (s.ip ? "（" + esc(s.ip) + "）" : "") + "</option>").join("");
  const body =
    '<label>广播源</label><select id="demoSource">' +
    '<option value="teacher">🖥️ 教师机屏幕</option>' +
    opts +
    "</select>" +
    '<label>显示方式</label><select id="demoMode">' +
    '<option value="full">全屏广播（置顶显示并拦截学生键鼠）</option>' +
    '<option value="win">窗口模式（学生可切换窗口，不锁定）</option>' +
    "</select>" +
    '<div class="hint">广播期间学生端缩略图监控自动暂停，将带宽让给广播流；广播目标为全部在线学生。</div>';
  modal("屏幕广播", body, () => {
    let source = $("demoSource").value;
    if (source === "teacher") source = "";
    cmd([], { demo: { on: true, fullscreen: $("demoMode").value === "full", source } });
  }, "📺 开始广播");
}

// openLockScreen shows the merged lock-screen dialog: custom message
// (default "请保持安静") and an optional wallpaper image that is pushed to
// the students first via file distribution.
function openLockScreen(ids) {
  const body =
    '<label>锁屏提示文字</label><input type="text" id="lsText" value="请保持安静">' +
    '<label>锁屏壁纸（可选，图片文件）</label><input type="file" id="lsWallpaper" accept="image/*">' +
    '<div class="hint">锁屏将覆盖学生整个桌面并拦截键盘鼠标；默认黑色背景显示提示文字。</div>' +
    '<div style="margin-top:10px"><button id="lsRelease" style="padding:7px 12px">🔓 解除锁屏</button></div>';
  modal("锁屏", body, () => {
    const text = $("lsText").value.trim() || "请保持安静";
    const file = $("lsWallpaper").files[0];
    const send = (wallpaper) => {
      cmd(ids, { lockscreen: { on: true, text, wallpaper: wallpaper || "" } });
      addMessage("已对 " + (selected.size ? selected.size + " 台" : "全体") + " 执行锁屏" + (wallpaper ? "（含壁纸）" : ""));
    };
    if (!file) {
      send("");
      return;
    }
    // Push the wallpaper through file distribution, then lock with it.
    const ext = (file.name.split(".").pop() || "jpg").toLowerCase();
    const fd = new FormData();
    fd.append("file", file, "lockscreen-wallpaper." + ext);
    fd.append("ids", ids.join(","));
    fetch("/api/sendfile", { method: "POST", body: fd })
      .then(() => send("lockscreen-wallpaper." + ext))
      .catch(() => {
        addMessage("⚠️ 壁纸分发失败，已使用黑色背景锁屏");
        send("");
      });
  }, "🔒 开始锁屏");
  // Secondary release action inside the dialog.
  $("lsRelease").onclick = () => {
    cmd(ids, { lockscreen: { on: false } });
    addMessage("已解除 " + (selected.size ? selected.size + " 台" : "全体") + " 的锁屏");
    $("modal").classList.add("hidden");
  };
}

function openRemoteCmd() {
  const body =
    '<div class="rc-grid">' +
    '<button onclick="rcOpenUrl()">🌐 打开网页</button>' +
    '<button onclick="rcOpenApp()">📦 打开应用</button>' +
    '<button onclick="rcCloseApp()">❌ 关闭应用</button>' +
    '<button onclick="rcCloseWindow()">🚫 关闭网页</button>' +
    '<button onclick="rcUnlockScreen()">🔓 解除锁屏</button>' +
    '<button onclick="rcUnmute()">🔊 取消静音</button>' +
    '<button onclick="rcChat()">💬 发送消息</button>' +
    "</div>";
  modal("远程命令", body, () => true);
}

function rcOpenUrl() { const u = promptText("要打开的网址"); if (u) cmd(selOrAll(), { open: { target: /^https?:\/\//.test(u) ? u : "https://" + u } }); }
function rcOpenApp() { const p = promptText("应用路径或可执行文件名"); if (p) cmd(selOrAll(), { open: { target: p } }); }
function rcCloseApp() { const p = promptText("要关闭的进程名（如 notepad.exe）"); if (p) cmd(selOrAll(), { close: { kind: "app", target: p } }); }
function rcCloseWindow() { const t = promptText("要关闭的窗口标题关键词"); if (t) cmd(selOrAll(), { close: { kind: "window", target: t } }); }
function rcUnlockScreen() { cmd(selOrAll(), { lockscreen: { on: false } }); }
function rcUnmute() { cmd(selOrAll(), { mute: { on: false } }); }
function rcChat() { const t = promptText("发送给学生的消息"); if (t) cmd(selOrAll(), { chat: { text: t } }); }

function openBlocklist(ids, preset) {
  const mode = preset || "black";
  const body =
    '<label>模式（程序与网页共用）</label><select id="blMode">' +
    '<option value="black"' + (mode === "black" ? " selected" : "") + ">黑名单（屏蔽列表内）</option>" +
    '<option value="white"' + (mode === "white" ? " selected" : "") + ">白名单（仅允许列表内）</option>" +
    '<option value="off">关闭管控</option>' +
    "</select>" +
    '<label>应用控制（进程名，每行一个）</label><textarea id="blApps">steam.exe\nwegame.exe\nminecraft.exe</textarea>' +
    '<label>网页控制（域名，每行一个）</label><textarea id="blSites">bilibili.com\ndouyin.com\niqiyi.com\nv.qq.com</textarea>' +
    '<label><input type="checkbox" id="blUsb" checked> 侦测并禁止从U盘运行游戏程序</label>';
  modal("规则控制", body, () => {
    const m = $("blMode").value;
    const apps = $("blApps").value.split("\n").map((s) => s.trim()).filter(Boolean);
    const sites = $("blSites").value.split("\n").map((s) => s.trim()).filter(Boolean);
    cmd(ids, { blocklist: { mode: m, apps, sites, block_usb_games: $("blUsb").checked } });
  });
}

function setupSidebar() {
  document.querySelectorAll(".section-header").forEach((h) => {
    h.onclick = (e) => {
      if (e.target.closest("button")) return;
      h.parentElement.classList.toggle("open");
    };
  });
  document.querySelectorAll("[data-rule]").forEach((a) => {
    a.onclick = () => openBlocklist(selOrAll(), a.dataset.rule);
  });
  document.querySelectorAll("[data-exempt]").forEach((b) => {
    b.onclick = () => {
      const on = b.dataset.exempt === "on";
      const ids = selOrAll();
      cmd(ids, { block_exempt: { on } });
      addMessage((on ? "🔓 已解除" : "🔒 已恢复") + " " + (selected.size ? selected.size + " 名学生的策略限制" : "全体学生的策略限制"));
    };
  });
  document.querySelectorAll("[data-export]").forEach((b) => { b.onclick = () => runTool("exportCsv"); });
  document.querySelectorAll("[data-import]").forEach((b) => { b.onclick = () => runTool("importData"); });
}

function setupFilters() {
  $("q").oninput = (e) => { query = e.target.value; render(); };
  $("grpFilter").onchange = (e) => { groupFilter = e.target.value; render(); };
  $("sortBy").onchange = (e) => { sortBy = e.target.value; render(); };
  $("hideOffline").onchange = (e) => { hideOffline = e.target.checked; render(); };
}

// ---------- full view + remote control ----------
function openFull(id) {
  fullId = id;
  controlOn = false;
  $("full").classList.remove("hidden");
  $("fullControl").textContent = "🖱️ 控制: 关";
  const st = roster.get(id);
  $("fullTitle").textContent = (st ? st.name : id) + " · 远程监控/操控";
  $("fullExempt").textContent = st && st.exempt ? "🔒 恢复限制" : "🔓 解除限制";
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
  $("fullShot").onclick = () => { if (fullId) cmd([fullId], { screenshot: {} }); };
  $("fullExempt").onclick = () => {
    if (!fullId) return;
    const st = roster.get(fullId);
    const on = !(st && st.exempt);
    cmd([fullId], { block_exempt: { on } });
    $("fullExempt").textContent = on ? "🔒 恢复限制" : "🔓 解除限制";
  };
  $("fullLockScreen").onclick = () => { const t = promptText("锁屏提示文字", "请保持安静"); if (fullId) cmd([fullId], { lockscreen: { on: true, text: t } }); };
  $("fullMute").onclick = () => cmd([fullId], { mute: { on: true } });
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
setupSidebar();
setupFull();
setupFilters();
connect();
