/*
  verify-code-relay 管理介面 — 共用行為

  CSP 不允許 inline 事件，所有行為都以 data-* 屬性綁定（事件委派在 document 上，
  動態加入的元素也有效）：

    data-confirm="訊息"            放在 <form> 或送出按鈕上：送出前先確認。
      data-confirm-ok="刪除"       確認按鈕文字（預設「確定」）；送出按鈕有 .btn-danger 時確認鈕為紅色。
    data-copy="文字"               點擊複製到剪貼簿；或 data-copy-from="#id" 複製該元素的 value／文字。
      data-copy-done="已複製金鑰"  成功提示（預設「已複製」）。
    data-modal-open="dialog-id"    開啟 <dialog class="modal">；dialog 內 data-modal-close 關閉，點背景也會關閉。
    data-dismiss                   關閉所在的 [data-flash]；flash 上 data-autohide="毫秒" 自動淡出。
    data-reveal="input-id"         顯示／隱藏密碼欄位。
    data-otp                       只保留數字、最多 6 位；加 data-autosubmit 則 6 位且表單有效時自動送出。
    data-busy-text="處理中…"       送出按鈕在表單送出期間顯示的文字（同時擋下重複送出）。

  <time datetime="RFC3339" data-localtime>  伺服器以 UTC 顯示（範本函式 localtime），載入後改寫成瀏覽器所在時區的
                                 「2026-10-20 15:04」；原本的 UTC 文字移到 title。

  伺服器退回表單時（欄位有 aria-invalid="true"），頁面載入後焦點會移到第一個出錯的欄位。

  頁面程式請使用 window.relay：
    relay.fetchJSON(url, body?)    有 body 時以 JSON POST，否則 GET；自動帶 X-CSRF-Token。
                                   回傳解析後的 JSON（呼叫端檢查 ok／error）；網路失敗、非 JSON
                                   回應時 reject（Error.message 為中文說明）；401 會導向登入頁。
    relay.toast(text, kind?)       kind：success（預設）| error | info
    relay.confirm(message, {ok, danger, title}) → Promise<boolean>
    relay.openModal(idOrElement)、relay.closeModal(idOrElement)
    relay.ready(fn)                DOM 就緒後執行
    relay.localTimes(root?)        動態加入 <time data-localtime> 後呼叫，改成本地時間
*/
(function () {
  "use strict";

  function csrfToken() {
    var m = document.querySelector('meta[name="csrf-token"]');
    return m ? m.getAttribute("content") || "" : "";
  }

  function ready(fn) {
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", fn);
    else fn();
  }

  var SVG_NS = "http://www.w3.org/2000/svg";
  var ICONS = {
    check: ["M20 6.5 9.5 17 4 11.5"],
    alert: ["M12 7.5v5.5M12 16.5h.01", "circle"],
    info: ["M12 11v5.5M12 7.5h.01", "circle"],
    x: ["M18 6 6 18M6 6l12 12"]
  };

  function icon(name) {
    var svg = document.createElementNS(SVG_NS, "svg");
    svg.setAttribute("class", "icon");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("fill", "none");
    svg.setAttribute("stroke", "currentColor");
    svg.setAttribute("stroke-width", "1.8");
    svg.setAttribute("stroke-linecap", "round");
    svg.setAttribute("stroke-linejoin", "round");
    svg.setAttribute("aria-hidden", "true");
    var spec = ICONS[name] || ICONS.info;
    if (spec[1] === "circle") {
      var c = document.createElementNS(SVG_NS, "circle");
      c.setAttribute("cx", "12");
      c.setAttribute("cy", "12");
      c.setAttribute("r", "9");
      svg.appendChild(c);
    }
    var p = document.createElementNS(SVG_NS, "path");
    p.setAttribute("d", spec[0]);
    svg.appendChild(p);
    return svg;
  }

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  /* ---------- fetchJSON ---------- */

  function fetchJSON(url, body) {
    var init = {
      method: body === undefined ? "GET" : "POST",
      credentials: "same-origin",
      headers: { "Accept": "application/json", "X-CSRF-Token": csrfToken() }
    };
    if (body !== undefined) {
      init.headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body === null ? {} : body);
    }
    return fetch(url, init).then(
      function (resp) {
        if (resp.status === 401) {
          window.location.assign("/login");
          throw new Error("登入已逾時，請重新登入");
        }
        return resp.json().then(
          function (data) { return data; },
          function () { throw new Error("伺服器回應無法解讀（HTTP " + resp.status + "）"); }
        );
      },
      function () { throw new Error("無法連線到伺服器，請確認網路後再試"); }
    );
  }

  /* ---------- toast ---------- */

  var toastBox = null;

  function toast(text, kind) {
    kind = kind || "success";
    if (!toastBox) {
      toastBox = el("div", "toasts");
      toastBox.setAttribute("aria-live", "polite");
      if (typeof toastBox.showPopover === "function") toastBox.setAttribute("popover", "manual");
      document.body.appendChild(toastBox);
    }
    if (toastBox.hasAttribute("popover")) {
      // re-show so the toasts are the newest top-layer element (above any open dialog)
      try { if (toastBox.matches(":popover-open")) toastBox.hidePopover(); toastBox.showPopover(); } catch (e) { /* ignore */ }
    }
    var t = el("div", "toast toast-" + kind);
    t.setAttribute("role", kind === "error" ? "alert" : "status");
    t.appendChild(icon(kind === "error" ? "alert" : kind === "info" ? "info" : "check"));
    t.appendChild(el("span", null, text));
    toastBox.appendChild(t);
    setTimeout(function () {
      leave(t, function () {
        if (!toastBox.firstChild && toastBox.hasAttribute("popover")) {
          try { toastBox.hidePopover(); } catch (e) { /* ignore */ }
        }
      });
    }, kind === "error" ? 6000 : 3200);
  }

  function leave(node, done) {
    if (!node || !node.parentNode) return;
    node.classList.add("is-leaving");
    setTimeout(function () {
      if (node.parentNode) node.parentNode.removeChild(node);
      if (done) done();
    }, 320);
  }

  /* ---------- modal ---------- */

  function dialogOf(x) {
    return typeof x === "string" ? document.getElementById(x) : x;
  }

  function openModal(x) {
    var d = dialogOf(x);
    if (!d) return;
    if (typeof d.showModal === "function") { if (!d.open) d.showModal(); }
    else d.setAttribute("open", "");
  }

  function closeModal(x, value) {
    var d = dialogOf(x);
    if (!d) return;
    if (typeof d.close === "function") { if (d.open) d.close(value); }
    else d.removeAttribute("open");
  }

  /* ---------- confirm ---------- */

  function confirmDialog(message, opts) {
    opts = opts || {};
    var probe = document.createElement("dialog");
    if (typeof probe.showModal !== "function") return Promise.resolve(window.confirm(message));
    return new Promise(function (resolve) {
      var d = el("dialog", "modal modal-confirm");
      d.setAttribute("aria-labelledby", "confirm-title");
      var head = el("div", "modal-head");
      var title = el("h2", "modal-title", opts.title || "請確認");
      title.id = "confirm-title";
      head.appendChild(title);
      var body = el("div", "modal-body", message);
      var foot = el("div", "modal-foot");
      var cancel = el("button", "btn btn-ghost", "取消");
      cancel.type = "button";
      var ok = el("button", "btn " + (opts.danger ? "btn-danger-solid" : "btn-primary"), opts.ok || "確定");
      ok.type = "button";
      foot.appendChild(cancel);
      foot.appendChild(ok);
      d.appendChild(head);
      d.appendChild(body);
      d.appendChild(foot);
      cancel.addEventListener("click", function () { d.close("cancel"); });
      ok.addEventListener("click", function () { d.close("ok"); });
      d.addEventListener("close", function () {
        resolve(d.returnValue === "ok");
        d.parentNode.removeChild(d);
      });
      document.body.appendChild(d);
      d.showModal();
      (opts.danger ? cancel : ok).focus();
    });
  }

  /* ---------- clipboard ---------- */

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).then(function () { return true; }, function () { return legacyCopy(text); });
    }
    return Promise.resolve(legacyCopy(text));
  }

  function legacyCopy(text) {
    var ta = el("textarea", "sr-only");
    ta.value = text;
    ta.setAttribute("readonly", "");
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
    document.body.removeChild(ta);
    return ok;
  }

  /* ---------- busy submit buttons ---------- */

  var busyLabels = new WeakMap();

  function setBusy(btn, on) {
    if (!btn) return;
    if (on) {
      btn.setAttribute("aria-busy", "true");
      if (btn.dataset.busyText && !busyLabels.has(btn)) {
        busyLabels.set(btn, Array.prototype.slice.call(btn.childNodes));
        btn.textContent = btn.dataset.busyText;
      }
    } else {
      btn.removeAttribute("aria-busy");
      var nodes = busyLabels.get(btn);
      if (nodes) {
        btn.textContent = "";
        nodes.forEach(function (n) { btn.appendChild(n); });
        busyLabels.delete(btn);
      }
    }
  }

  function resetForms() {
    document.querySelectorAll("form[data-submitting]").forEach(function (f) { delete f.dataset.submitting; });
    document.querySelectorAll('[aria-busy="true"]').forEach(function (b) { setBusy(b, false); });
  }

  /* ---------- event wiring ---------- */

  document.addEventListener("submit", function (e) {
    if (e.defaultPrevented) return;
    var form = e.target;
    var btn = e.submitter || null;
    var msg = (btn && btn.dataset.confirm) || form.dataset.confirm;
    if (msg && form.dataset.confirmed !== "1") {
      e.preventDefault();
      var danger = (btn && btn.classList.contains("btn-danger")) || form.hasAttribute("data-confirm-danger");
      var okLabel = (btn && btn.dataset.confirmOk) || form.dataset.confirmOk;
      confirmDialog(msg, { ok: okLabel, danger: danger }).then(function (yes) {
        if (!yes) return;
        form.dataset.confirmed = "1";
        if (typeof form.requestSubmit === "function") form.requestSubmit(btn || undefined);
        else form.submit();
      });
      return;
    }
    delete form.dataset.confirmed;
    if (form.dataset.submitting === "1") {
      e.preventDefault();
      return;
    }
    form.dataset.submitting = "1";
    setBusy(btn || form.querySelector('[type="submit"]'), true);
  });

  // a page restored from the back/forward cache must not stay "submitting"
  window.addEventListener("pageshow", function (e) { if (e.persisted) resetForms(); });

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!(t instanceof Element)) return;

    var copy = t.closest("[data-copy], [data-copy-from]");
    if (copy) {
      var text = copy.dataset.copy;
      if (text == null) {
        var src = document.querySelector(copy.dataset.copyFrom);
        text = src ? ("value" in src ? src.value : src.textContent) : "";
      }
      copyText(text).then(function (ok) {
        if (ok) {
          toast(copy.dataset.copyDone || "已複製");
          copy.classList.add("is-copied");
          setTimeout(function () { copy.classList.remove("is-copied"); }, 1600);
        } else {
          toast("無法自動複製，請手動選取後複製", "error");
        }
      });
      return;
    }

    var opener = t.closest("[data-modal-open]");
    if (opener) {
      e.preventDefault();
      openModal(opener.dataset.modalOpen);
      return;
    }
    var closer = t.closest("[data-modal-close]");
    if (closer) {
      e.preventDefault();
      closeModal(closer.closest("dialog"));
      return;
    }
    if (t.tagName === "DIALOG" && t.classList.contains("modal")) {
      // a click on the dialog box itself (not its content) is a click on the backdrop
      var r = t.getBoundingClientRect();
      if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) closeModal(t, "cancel");
      return;
    }

    var dismiss = t.closest("[data-dismiss]");
    if (dismiss) {
      leave(dismiss.closest("[data-flash]"));
      return;
    }

    var reveal = t.closest("[data-reveal]");
    if (reveal) {
      var input = document.getElementById(reveal.dataset.reveal);
      if (!input) return;
      var show = input.type === "password";
      input.type = show ? "text" : "password";
      reveal.setAttribute("aria-pressed", show ? "true" : "false");
      reveal.setAttribute("aria-label", show ? "隱藏內容" : "顯示內容");
      input.focus();
    }
  });

  document.addEventListener("input", function (e) {
    var t = e.target;
    if (!(t instanceof HTMLInputElement) || !t.hasAttribute("data-otp")) return;
    var digits = t.value.replace(/\D+/g, "").slice(0, 6);
    if (digits !== t.value) t.value = digits;
    if (t.getAttribute("aria-invalid") === "true") t.removeAttribute("aria-invalid");
    if (digits.length === 6 && t.hasAttribute("data-autosubmit") && t.form && t.dataset.autoSent !== digits) {
      if (t.form.checkValidity()) {
        t.dataset.autoSent = digits;
        if (typeof t.form.requestSubmit === "function") t.form.requestSubmit();
        else t.form.submit();
      }
    }
  });

  ready(function () {
    document.querySelectorAll("[data-flash][data-autohide]").forEach(function (f) {
      var ms = parseInt(f.dataset.autohide, 10) || 5000;
      var timer = setTimeout(function () { leave(f); }, ms);
      // keep it while the pointer or focus is on it
      f.addEventListener("mouseenter", function () { clearTimeout(timer); });
      f.addEventListener("focusin", function () { clearTimeout(timer); });
    });
    var cur = document.querySelector(".nav-item[aria-current]");
    var nav = cur && cur.parentElement;
    if (nav && nav.scrollWidth > nav.clientWidth) {
      nav.scrollLeft = cur.offsetLeft - (nav.clientWidth - cur.offsetWidth) / 2;
    }
  });

  // a form sent back with errors: put the cursor in the first field to fix. After "load",
  // because scrolling to a #fragment in the URL would otherwise take the focus away again.
  function focusFirstInvalid() {
    var bad = document.querySelector('main [aria-invalid="true"]');
    if (bad && typeof bad.focus === "function") bad.focus();
  }
  if (document.readyState === "complete") focusFirstInvalid();
  else window.addEventListener("load", function () { setTimeout(focusFirstInvalid, 0); });

  function pad(n) { return n < 10 ? "0" + n : String(n); }

  // localTimes rewrites server-rendered UTC times in the browser's time zone.
  function localTimes(root) {
    (root || document).querySelectorAll("time[data-localtime]").forEach(function (t) {
      var d = new Date(t.getAttribute("datetime"));
      if (isNaN(d.getTime())) return;
      if (!t.title) t.title = t.textContent;
      t.textContent = d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) + " " + pad(d.getHours()) + ":" + pad(d.getMinutes());
    });
  }
  ready(function () { localTimes(); });

  /* ---------- Telegram page ----------

    dialog.modal[data-open]                       伺服器退回表單時，載入後自動開啟該 dialog。
    data-getme-bot="名稱"                         載入後以 getMe 填入該 Bot 的 @username（或錯誤）。
    data-getme-input="輸入框 id" data-getme-out="結果 id"
                                                  「驗證」按鈕：以輸入框裡的 token 呼叫 getMe，結果寫進結果元素。
    data-chats-bot="select id" data-chats-input="輸入框 id"
                                                  「讀取最近對話」：開啟 #dlg-chats 列出該 Bot 最近的聊天室，
                                                  點選即把 chat_id 帶入輸入框。圖示取自 <template id="tpl-chat-*">。
    聊天室名稱等 Telegram 資料一律以 textContent 寫入。
  */

  var CHAT_TYPES = { "private": "私人對話", group: "群組", supergroup: "超級群組", channel: "頻道" };

  function byId(id) { return id ? document.getElementById(id) : null; }

  // setResult shows text in node: ok true (check icon), false (alert icon) or null (neutral);
  // note (optional) follows in a quieter span.
  function setResult(node, ok, text, note) {
    if (!node) return;
    node.textContent = "";
    node.classList.toggle("is-ok", ok === true);
    node.classList.toggle("is-err", ok === false);
    if (ok !== null) node.appendChild(icon(ok ? "check" : "alert"));
    if (text) node.appendChild(document.createTextNode(text));
    if (note) node.appendChild(el("span", "result-note", note));
  }

  // showBot puts getMe's answer d into node; prefix (optional) goes before the @username.
  function showBot(node, d, prefix) {
    if (d.ok) setResult(node, true, (prefix || "") + "@" + (d.username || "?"), d.first_name ? "· " + d.first_name : "");
    else setResult(node, false, d.error || "無法驗證");
  }

  ready(function () {
    document.querySelectorAll("dialog.modal[data-open]").forEach(function (d) { openModal(d); });
    document.querySelectorAll("[data-getme-bot]").forEach(function (n) {
      fetchJSON("/api/telegram/getme", { bot: n.dataset.getmeBot }).then(
        function (d) { showBot(n, d); },
        function (err) { setResult(n, false, err.message); }
      );
    });
  });

  var chats = { bot: "", input: null };

  function chatState(kind, title, text) {
    var box = el("div", "chats-state" + (kind === "error" ? " is-err" : ""));
    if (kind === "loading") box.appendChild(el("span", "spinner"));
    else {
      var i = byId(kind === "error" ? "" : "tpl-chat-private");
      box.appendChild(kind === "error" ? icon("alert") : (i ? i.content.cloneNode(true) : icon("info")));
    }
    box.appendChild(el("strong", null, title));
    if (text) box.appendChild(el("span", null, text));
    return box;
  }

  function chatRow(c) {
    var kind = c.type === "channel" ? "channel" : (c.type === "group" || c.type === "supergroup") ? "group" : "private";
    var b = el("button", "chat-row chat-" + kind);
    b.type = "button";
    b.dataset.chatId = String(c.id);
    var ic = el("span", "chat-icon");
    var tpl = byId("tpl-chat-" + kind);
    if (tpl) ic.appendChild(tpl.content.cloneNode(true));
    var text = el("span", "chat-text");
    text.appendChild(el("span", "chat-title", c.title || "（沒有名稱）"));
    text.appendChild(el("span", "chat-meta", (CHAT_TYPES[c.type] || c.type || "聊天室") + (c.username ? " · @" + c.username : "")));
    var id = el("span", "chat-id");
    id.appendChild(el("span", "chat-pick", "帶入"));
    id.appendChild(el("span", null, String(c.id)));
    b.appendChild(ic);
    b.appendChild(text);
    b.appendChild(id);
    return b;
  }

  function loadChats() {
    var d = byId("dlg-chats");
    if (!d) return;
    var list = d.querySelector("[data-chats-list]");
    var reload = d.querySelector("[data-chats-reload]");
    var sub = d.querySelector("[data-chats-sub]");
    if (sub) sub.textContent = "Bot " + chats.bot + " 最近收到訊息的聊天室";
    list.textContent = "";
    list.appendChild(chatState("loading", "正在讀取最近對話…"));
    list.setAttribute("aria-busy", "true");
    if (reload) reload.disabled = true;
    var bot = chats.bot;
    // getUpdates without an offset returns only the OLDEST pending updates (at most 100)
    // from the last 24 hours, so a busy bot's newest chats may be missing; the hint says so.
    fetchJSON("/api/telegram/chats", { bot: bot }).then(
      function (res) {
        if (bot !== chats.bot) return;
        list.textContent = "";
        if (!res.ok) {
          list.appendChild(chatState("error", "讀取失敗", res.error || "未知的錯誤"));
        } else if (!res.chats || !res.chats.length) {
          list.appendChild(chatState("empty", "沒有找到最近的對話", "對 Bot 傳一則訊息（群組裡可以傳 /start），再按「重新讀取」。"));
        } else {
          res.chats.forEach(function (c) { list.appendChild(chatRow(c)); });
        }
      },
      function (err) {
        if (bot !== chats.bot) return;
        list.textContent = "";
        list.appendChild(chatState("error", "讀取失敗", err.message));
      }
    ).then(function () {
      // an answer for another bot (the picker was reopened meanwhile) changes nothing
      if (bot !== chats.bot) return;
      list.removeAttribute("aria-busy");
      if (reload) reload.disabled = false;
      var first = list.querySelector(".chat-row");
      if (first && d.open) first.focus();
    });
  }

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!(t instanceof Element)) return;

    var check = t.closest("[data-getme-input]");
    if (check) {
      var input = byId(check.dataset.getmeInput);
      var out = byId(check.dataset.getmeOut);
      var token = input ? input.value.trim() : "";
      if (!token) {
        setResult(out, false, "請先貼上 token");
        if (input) input.focus();
        return;
      }
      check.setAttribute("aria-busy", "true");
      check.disabled = true;
      setResult(out, null, "正在向 Telegram 驗證…");
      fetchJSON("/api/telegram/getme", { token: token }).then(
        function (d) { showBot(out, d, "有效："); },
        function (err) { setResult(out, false, err.message); }
      ).then(function () {
        check.removeAttribute("aria-busy");
        check.disabled = false;
      });
      return;
    }

    var open = t.closest("[data-chats-bot]");
    if (open) {
      var sel = byId(open.dataset.chatsBot);
      chats.bot = sel ? sel.value : "";
      chats.input = byId(open.dataset.chatsInput);
      if (!chats.bot) {
        toast("請先選擇 Bot", "error");
        return;
      }
      openModal("dlg-chats");
      loadChats();
      return;
    }

    if (t.closest("[data-chats-reload]")) {
      loadChats();
      return;
    }

    var row = t.closest(".chat-row");
    if (row && chats.input) {
      chats.input.value = row.dataset.chatId;
      chats.input.removeAttribute("aria-invalid");
      closeModal("dlg-chats");
      chats.input.focus();
      toast("已帶入 chat_id " + row.dataset.chatId);
    }
  });

  // a new token makes the previous check result stale
  document.addEventListener("input", function (e) {
    var t = e.target;
    if (!(t instanceof HTMLInputElement) || !t.id) return;
    var btn = document.querySelector('[data-getme-input="' + t.id + '"]');
    if (btn) setResult(byId(btn.dataset.getmeOut), null, "");
  });

  /* ---------- mailbox form ----------

    form[data-mailbox-form] (data-name="名稱"：編輯時)
      input[name=preset][data-host][data-auth]   服務郵票：改選時帶入主機、埠號（清空＝993）與認證方式，
                                                 被改到的欄位短暫標示綠色。
      [data-probe]                               「測試連線」：以表單目前的值 POST /api/mailboxes/probe，
                                                 結果寫進 [data-probe-out]；成功時把伺服器的資料夾列進
                                                 [data-folder-list]（已勾選的保持勾選，伺服器上沒有的另外標示）。
      [data-folder-input] ＋ [data-folder-add]   手動加入資料夾（Enter 也可以）。
    資料夾名稱來自 IMAP 伺服器，一律以 textContent 寫入。
  */

  var PRESET_HOSTS = ["imap.gmail.com", "imap.mail.me.com", "outlook.office365.com"];
  var JUNK_RE = /spam|junk|bulk|垃圾/i;

  function mailboxForm(node) { return node && node.closest ? node.closest("form[data-mailbox-form]") : null; }

  function field(form, name) { return form.elements.namedItem(name); }

  function flashFilled(input) {
    if (!input) return;
    input.classList.remove("is-filled");
    void input.offsetWidth; // restart the animation
    input.classList.add("is-filled");
  }

  function applyPreset(radio) {
    var form = mailboxForm(radio);
    if (!form) return;
    var host = field(form, "host");
    var port = field(form, "port");
    var want = radio.dataset.host || "";
    if (host) {
      // "custom" only clears a host that one of the other cards filled in
      if (want || PRESET_HOSTS.indexOf(host.value.trim()) >= 0) {
        if (host.value !== want) { host.value = want; flashFilled(host); }
        host.removeAttribute("aria-invalid");
      }
    }
    if (port && port.value !== "") { port.value = ""; flashFilled(port); }
    var auth = form.querySelector('input[name="auth_type"][value="' + (radio.dataset.auth || "password") + '"]');
    if (auth && !auth.checked) { auth.checked = true; auth.dispatchEvent(new Event("change", { bubbles: true })); }
    var tenant = field(form, "tenant");
    if (radio.value === "outlook" && tenant && !tenant.value) { tenant.value = "consumers"; flashFilled(tenant); }
    probeStale(form);
  }

  // folderChip builds one checkbox row of the folder list.
  function folderChip(name, checked, tag, warn) {
    var li = el("li");
    var label = el("label", "folder");
    var box = el("input");
    box.type = "checkbox";
    box.name = "folders";
    box.value = name;
    box.checked = !!checked;
    label.appendChild(box);
    label.appendChild(el("span", "folder-name mono", name));
    if (tag) label.appendChild(el("span", "folder-tag" + (warn ? " is-warn" : ""), tag));
    li.appendChild(label);
    return li;
  }

  function folderTag(name) {
    if (name.toUpperCase() === "INBOX") return "收件匣";
    return JUNK_RE.test(name) ? "垃圾郵件" : "";
  }

  function currentFolders(list) {
    var all = [], checked = {};
    list.querySelectorAll('input[name="folders"]').forEach(function (b) {
      all.push(b.value);
      if (b.checked) checked[b.value] = true;
    });
    return { all: all, checked: checked };
  }

  function showFolders(list, serverFolders) {
    var cur = currentFolders(list);
    var onServer = {};
    list.textContent = "";
    serverFolders.forEach(function (f) {
      onServer[f] = true;
      list.appendChild(folderChip(f, cur.checked[f], folderTag(f), false));
    });
    // chosen folders the server does not list stay (and stay chosen), marked
    cur.all.forEach(function (f) {
      if (cur.checked[f] && !onServer[f]) list.appendChild(folderChip(f, true, "伺服器上沒有", true));
    });
    list.classList.remove("is-invalid");
  }

  function addFolder(form) {
    var input = form.querySelector("[data-folder-input]");
    var list = form.querySelector("[data-folder-list]");
    if (!input || !list) return;
    var name = input.value.trim();
    if (!name) { input.focus(); return; }
    var existing = null;
    list.querySelectorAll('input[name="folders"]').forEach(function (b) { if (b.value === name) existing = b; });
    if (existing) {
      existing.checked = true;
      toast("已勾選 " + name, "info");
    } else {
      var chip = folderChip(name, true, folderTag(name), false);
      chip.firstChild.classList.add("is-new");
      list.appendChild(chip);
    }
    input.value = "";
    input.focus();
  }

  function probeOut(form) { return form.querySelector("[data-probe-out]"); }

  // setProbe shows the connection test state: kind busy|ok|err|idle.
  function setProbe(out, kind, text, strong) {
    if (!out) return;
    out.className = "probe" + (kind === "idle" ? "" : " is-" + kind);
    var ic = out.querySelector(".probe-icon");
    var p = out.querySelector(".probe-text");
    ic.textContent = "";
    if (kind === "busy") ic.appendChild(el("span", "spinner"));
    else ic.appendChild(icon(kind === "ok" ? "check" : kind === "err" ? "alert" : "info"));
    p.textContent = "";
    if (strong) p.appendChild(el("b", null, strong));
    if (text) p.appendChild(document.createTextNode(text));
    return p;
  }

  function probeStale(form) {
    var out = probeOut(form);
    if (out && /is-(ok|err)/.test(out.className) && !form.dataset.probing) {
      setProbe(out, "idle", "設定已變更，可以再測試一次。");
    }
  }

  function runProbe(btn) {
    var form = mailboxForm(btn);
    if (!form || form.dataset.probing) return;
    var v = function (n) { var x = field(form, n); return x && typeof x.value === "string" ? x.value.trim() : ""; };
    var authBox = form.querySelector('input[name="auth_type"]:checked');
    var body = {
      // only an edit form names a stored mailbox (whose saved password may be reused)
      name: form.dataset.name || "",
      host: v("host"), port: v("port"), username: v("username"),
      auth_type: authBox ? authBox.value : "password",
      password: (field(form, "password") || { value: "" }).value,
      client_id: v("client_id"), tenant: v("tenant")
    };
    var out = probeOut(form);
    if (!body.host) { setProbe(out, "err", "請先填入 IMAP 主機"); field(form, "host").focus(); return; }
    if (!body.username) { setProbe(out, "err", "請先填入帳號"); field(form, "username").focus(); return; }
    form.dataset.probing = "1";
    btn.setAttribute("aria-busy", "true");
    btn.disabled = true;
    var target = body.host + ":" + (body.port || "993");
    var p = setProbe(out, "busy", " 並登入…", "正在連線 " + target);
    var started = Date.now();
    var clock = el("span", "probe-time", "0 秒");
    p.appendChild(clock);
    var timer = setInterval(function () { clock.textContent = Math.round((Date.now() - started) / 1000) + " 秒"; }, 1000);
    fetchJSON("/api/mailboxes/probe", body).then(
      function (d) {
        if (d.ok) {
          var folders = d.folders || [];
          showFolders(form.querySelector("[data-folder-list]"), folders);
          setProbe(out, "ok", "：伺服器上有 " + folders.length + " 個資料夾，勾選要監看的。", "登入成功");
        } else {
          setProbe(out, "err", d.error || "測試失敗");
        }
      },
      function (err) { setProbe(out, "err", err.message); }
    ).then(function () {
      clearInterval(timer);
      delete form.dataset.probing;
      btn.removeAttribute("aria-busy");
      btn.disabled = false;
    });
  }

  document.addEventListener("change", function (e) {
    var t = e.target;
    if (!(t instanceof HTMLInputElement) || !mailboxForm(t)) return;
    if (t.name === "preset" && t.checked) applyPreset(t);
    if (t.name === "targets") {
      t.form.querySelectorAll('input[name="targets"][aria-invalid]').forEach(function (b) { b.removeAttribute("aria-invalid"); });
    }
    if (t.name === "folders") t.closest("[data-folder-list]").classList.remove("is-invalid");
  });

  document.addEventListener("input", function (e) {
    var t = e.target;
    if (!(t instanceof HTMLInputElement) || !mailboxForm(t)) return;
    if (t.getAttribute("aria-invalid") === "true" && t.type !== "checkbox") t.removeAttribute("aria-invalid");
    if (["host", "port", "username", "password", "client_id", "tenant"].indexOf(t.name) >= 0) probeStale(t.form);
  });

  document.addEventListener("keydown", function (e) {
    var t = e.target;
    if (e.key === "Enter" && t instanceof HTMLInputElement && t.hasAttribute("data-folder-input")) {
      e.preventDefault(); // add the folder instead of submitting the form
      addFolder(mailboxForm(t));
    }
  });

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!(t instanceof Element)) return;
    var probe = t.closest("[data-probe]");
    if (probe) { runProbe(probe); return; }
    var add = t.closest("[data-folder-add]");
    if (add) addFolder(mailboxForm(add));
  });

  window.relay = {
    localTimes: localTimes,
    fetchJSON: fetchJSON,
    toast: toast,
    confirm: confirmDialog,
    openModal: openModal,
    closeModal: closeModal,
    ready: ready
  };
})();
