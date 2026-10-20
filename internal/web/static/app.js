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
    form[data-dirty-watch]         有尚未儲存的變更時顯示表單內的 [data-dirty-note]（見「unsaved changes」）。

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

  /* ---------- Microsoft authorisation ----------

    [data-oauth-start="名稱"]  開啟 #dlg-oauth（範本 "oauth-dialog"，頁面要放一份）：
                              POST /api/mailboxes/{名稱}/oauth/start，顯示 user code（點一下複製）、
                              Microsoft 登入頁按鈕與倒數，之後每 3 秒 GET …/oauth/status。
      data-oauth-user="帳號"   顯示在標題下與步驟 2。
      data-oauth-form          信箱編輯頁的按鈕：帳號、client_id、tenant 或認證方式有未儲存的修改時，
                              先要求儲存（授權一律使用已儲存的設定）。
    完成後：頁面上有 [data-oauth-state="名稱"] 就原地改成「已授權」；沒有則在關閉視窗時重新整理頁面。
    伺服器上同一信箱只有一個流程：關閉視窗只停止輪詢，再按一次會接回同一組代碼。
    伺服器給的文字（代碼、網址、錯誤）一律以 textContent 寫入；網址不是 https 時不做成連結。
  */

  var OAUTH_POLL = 3000;
  var OAUTH_GRACE = 15000; // after the code expired, how long network errors are still retried
  var oauth = { name: "", user: "", gen: 0, poll: 0, tick: 0, deadline: 0, life: 0, hasCode: false, done: false };

  function oq(sel) { var d = byId("dlg-oauth"); return d ? d.querySelector(sel) : null; }

  function oauthView(state) {
    var d = byId("dlg-oauth");
    d.dataset.state = state;
    d.querySelectorAll("[data-oauth-only]").forEach(function (n) {
      var want = n.dataset.oauthOnly.split(" ");
      n.hidden = !(want.indexOf(state) >= 0 || (oauth.hasCode && want.indexOf("code") >= 0));
    });
  }

  // oauthStatus fills the status line: kind wait | ok | warn | err.
  function oauthStatus(kind, title, text) {
    var box = oq("[data-oauth-status]");
    box.className = "oauth-status" + (kind === "wait" ? "" : " is-" + kind);
    var ic = oq("[data-oauth-status-icon]");
    ic.textContent = "";
    ic.appendChild(kind === "wait" ? el("span", "pulse-dot") : icon(kind === "ok" ? "check" : "alert"));
    oq("[data-oauth-status-title]").textContent = title || "";
    oq("[data-oauth-status-text]").textContent = text || "";
  }

  function oauthStop() {
    oauth.gen++;
    clearTimeout(oauth.poll);
    clearInterval(oauth.tick);
  }

  function oauthURL(what) { return "/api/mailboxes/" + encodeURIComponent(oauth.name) + "/oauth/" + what; }

  function showCode(d) {
    var code = String(d.user_code || "");
    var box = oq("[data-oauth-code]");
    box.textContent = "";
    for (var i = 0; i < code.length; i++) {
      var ch = code.charAt(i);
      box.appendChild(el("span", ch === "-" || ch === " " ? "postcode-sep" : null, ch === " " ? "-" : ch));
    }
    oq("[data-oauth-copy]").dataset.code = code;
    var uri = String(d.verification_uri || "");
    var link = oq("[data-oauth-link]");
    if (/^https:\/\//i.test(uri)) link.href = uri;
    else link.removeAttribute("href");
    link.hidden = !link.hasAttribute("href");
    oq("[data-oauth-uri]").textContent = uri.replace(/^https:\/\//i, "");
    oauth.hasCode = true;
    oauth.life = Math.max(Number(d.lifetime) || 0, Number(d.expires_in) || 0, 1);
    oauth.deadline = Date.now() + (Number(d.expires_in) || 0) * 1000;
  }

  function countdown() {
    var left = Math.max(0, Math.round((oauth.deadline - Date.now()) / 1000));
    var timer = oq(".oauth-timer");
    var text = oq("[data-oauth-left]");
    if (left > 0) text.textContent = "剩 " + Math.floor(left / 60) + ":" + pad(left % 60);
    else text.textContent = "代碼已過期";
    timer.classList.toggle("is-low", left < 60);
    oq("[data-oauth-meter]").style.setProperty("--left", String(left / oauth.life));
  }

  function oauthFail(msg) {
    oauthStop();
    oauthView("error");
    oauthStatus("err", "授權沒有完成", msg);
    var retry = oq("[data-oauth-retry]");
    if (retry) retry.focus();
  }

  // oauthDone shows the finished sign-in; warning (from the server) says the new token is
  // stored but not in use yet.
  function oauthDone(warning) {
    oauthStop();
    oauth.done = true;
    oauthView("done");
    if (warning) oauthStatus("warn", "授權已儲存，但還沒有生效", warning);
    else oauthStatus("ok", "授權完成", "信箱正在用新的授權重新連線。");
    var ok = oq('[data-oauth-only="done"]');
    if (ok) ok.focus();
  }

  function oauthPoll(gen, delay) {
    clearTimeout(oauth.poll);
    oauth.poll = setTimeout(function () {
      fetchJSON(oauthURL("status")).then(
        function (d) {
          if (gen !== oauth.gen) return;
          if (d.ok === false) oauthFail(d.error || "無法取得授權進度");
          else if (d.state === "pending") {
            oauthStatus("wait", "等待你在 Microsoft 完成登入", "這裡每 3 秒自動檢查一次。");
            oauthPoll(gen, OAUTH_POLL);
          } else if (d.state === "done") oauthDone(d.warning);
          else if (d.state === "error") oauthFail(d.error || "授權失敗");
          else oauthFail("這組代碼已失效：信箱設定可能已變更，或服務已重新啟動。請按「重試」取得新的代碼。");
        },
        function (err) {
          if (gen !== oauth.gen) return;
          if (Date.now() > oauth.deadline + OAUTH_GRACE) {
            oauthFail("代碼已過期，而且無法連線到服務確認結果（" + err.message + "）。確認網路後請按「重試」。");
            return;
          }
          oauthStatus("wait", "暫時無法確認進度", err.message + "，稍後自動再試。");
          oauthPoll(gen, OAUTH_POLL);
        }
      );
    }, delay);
  }

  function oauthStart(name, user) {
    oauthStop();
    var gen = oauth.gen;
    oauth.name = name;
    oauth.user = user || "";
    oauth.hasCode = false;
    oauth.done = false;
    oq("[data-oauth-sub]").textContent = "信箱 " + name + (oauth.user ? " · " + oauth.user : "");
    oq("[data-oauth-user]").textContent = oauth.user;
    oq("[data-oauth-user-line]").hidden = !oauth.user;
    oauthView("loading");
    openModal("dlg-oauth");
    fetchJSON(oauthURL("start"), {}).then(
      function (d) {
        if (gen !== oauth.gen) return;
        if (!d.ok) { oauthFail(d.error || "無法開始授權"); return; }
        showCode(d);
        countdown();
        oauth.tick = setInterval(countdown, 1000);
        oauthView("pending");
        oauthStatus("wait", "等待你在 Microsoft 完成登入", "這裡每 3 秒自動檢查一次。");
        var first = oq(".oauth-copy");
        if (first) first.focus();
        if (d.state === "done") oauthDone(d.warning);
        else oauthPoll(gen, OAUTH_POLL);
      },
      function (err) { if (gen === oauth.gen) oauthFail(err.message); }
    );
  }

  // formDirty reports unsaved changes to the settings an authorisation is for, compared
  // with the stored ones on [data-oauth-state] (a form sent back with errors shows typed,
  // unsaved values as its defaults).
  function formDirty(form) {
    var box = form.querySelector("[data-oauth-state]");
    if (!box) return false;
    var dirty = false;
    ["username", "client_id", "tenant"].forEach(function (n) {
      var f = field(form, n);
      if (f && f.value.trim() !== (box.getAttribute("data-stored-" + n) || "").trim()) dirty = true;
    });
    var ms = form.querySelector('input[name="auth_type"][value="microsoft_oauth"]');
    if (ms && !ms.checked) dirty = true;
    return dirty;
  }

  // markAuthorized turns the edit page's authorisation box into "已授權" without a reload
  // (the form may hold unsaved edits).
  function markAuthorized(box) {
    var ic = box.querySelector(".oauth-icon");
    if (ic) { ic.className = "oauth-icon is-ok"; ic.textContent = ""; ic.appendChild(icon("check")); }
    var title = box.querySelector(".oauth-title");
    if (title) title.textContent = "已授權";
    var hint = box.querySelector(".oauth-text .hint");
    if (hint) hint.textContent = "token 會自動更新。換了 Microsoft 帳號或授權失效時再重新授權。";
    var btn = box.querySelector("[data-oauth-start]");
    if (btn) {
      btn.classList.remove("btn-primary");
      var label = btn.querySelector("span");
      if (label) label.textContent = "重新授權";
    }
  }

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!(t instanceof Element)) return;
    var start = t.closest("[data-oauth-start]");
    if (start) {
      if (!byId("dlg-oauth")) return;
      var form = start.hasAttribute("data-oauth-form") ? mailboxForm(start) : null;
      if (form && formDirty(form)) {
        toast("帳號、應用程式識別碼或 tenant 有修改還沒儲存：授權會使用已儲存的設定，請先按「儲存並套用」。", "error");
        return;
      }
      var user = start.dataset.oauthUser || "";
      var stored = form ? form.querySelector("[data-oauth-state]") : null;
      if (stored) user = stored.getAttribute("data-stored-username") || "";
      oauthStart(start.dataset.oauthStart, user);
      return;
    }
    var copy = t.closest("[data-oauth-copy]");
    if (copy) {
      var code = (oq(".oauth-code") || copy).dataset.code || "";
      copyText(code).then(function (ok) {
        if (!ok) { toast("無法自動複製，請手動輸入代碼", "error"); return; }
        toast("已複製代碼 " + code);
        var c = oq(".oauth-code");
        c.classList.add("is-copied");
        setTimeout(function () { c.classList.remove("is-copied"); }, 1600);
      });
      return;
    }
    if (t.closest("[data-oauth-retry]")) oauthStart(oauth.name, oauth.user);
  });

  ready(function () {
    var d = byId("dlg-oauth");
    if (!d) return;
    d.addEventListener("close", function () {
      oauthStop();
      if (!oauth.done) return;
      oauth.done = false;
      var box = document.querySelector('[data-oauth-state="' + oauth.name.replace(/["\\]/g, "\\$&") + '"]');
      if (box) markAuthorized(box);
      else window.location.reload();
    });
  });

  // a tab coming back from Microsoft's sign-in page checks at once
  document.addEventListener("visibilitychange", function () {
    var d = byId("dlg-oauth");
    if (document.visibilityState === "visible" && d && d.open && d.dataset.state === "pending") oauthPoll(oauth.gen, 0);
  });

  /* ---------- unsaved changes ----------

    form[data-dirty-watch]      有尚未儲存的變更時顯示表單內的 [data-dirty-note]。比較的是表單送出的內容，
                                頁面程式改動表單（重新排序、標籤）後送出 "relay:changed" 事件即可。
  */

  function formSnapshot(form) {
    var parts = [];
    new FormData(form).forEach(function (v, k) { if (k !== "csrf") parts.push(k + "=" + v); });
    return parts.join("\u0000");
  }

  function checkDirty(form) {
    if (!form || !form.hasAttribute("data-dirty-watch")) return;
    var note = form.querySelector("[data-dirty-note]");
    if (note) note.hidden = formSnapshot(form) === form.dataset.snapshot;
  }

  function changed(form) {
    if (form) form.dispatchEvent(new CustomEvent("relay:changed", { bubbles: true }));
  }

  ["input", "change", "relay:changed"].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var form = e.target instanceof Element ? e.target.closest("form[data-dirty-watch]") : null;
      if (form) checkDirty(form);
    });
  });

  // after every page script has set up its form (ready handlers run in order)
  // A form sent back with errors is unsaved as it stands, so its note shows from the start.
  function snapshotForms() {
    document.querySelectorAll("form[data-dirty-watch]").forEach(function (f) {
      f.dataset.snapshot = f.querySelector('[aria-invalid="true"], .rule-error') ? "\u0001" : formSnapshot(f);
      checkDirty(f);
    });
  }

  function reducedMotion() {
    return window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  }

  /* ---------- rules page ----------

    form[data-rules-form]
      ol[data-rule-list] > li[data-rule]      一條規則一列；欄位以平行陣列送出，所以 DOM 順序就是比對順序。
        [data-rule-move="up|down"]            上移／下移；列內按 Alt＋↑／↓ 也可以。
        [data-rule-remove]                    刪除這一列（儲存後才生效）。
        [data-rule-sum]                       寄件者／主旨摘要，精簡檢視時顯示在列首。
        input[data-rule-name]                 名稱，同步顯示在列首 [data-rule-title]。
      [data-rules-compact]                    精簡檢視（aria-pressed）：只顯示列首，方便調整順序；有錯誤與新加的列保持展開。
      [data-rule-add]                         由 <template id="rule-new"> 加一列；其中的 "rule-new" id 前綴換成唯一值。
      [data-rule-live]                        螢幕閱讀器的移動／刪除提示。
    [data-tags] .textarea[data-tags-source]   一行一個的關鍵字欄位，強化成可刪除的標籤（Enter 加入，貼上多行時逐行加入；
                                            關鍵字可以含逗號）；送出的仍是 textarea。
  */

  var ruleSeq = 0;

  function ruleList(node) { return node.closest("form[data-rules-form]").querySelector("[data-rule-list]"); }

  function ruleName(li) {
    var input = li.querySelector("[data-rule-name]");
    return input && input.value.trim() ? "「" + input.value.trim() + "」" : "未命名規則";
  }

  function announce(form, text) {
    var live = form.querySelector("[data-rule-live]");
    if (!live) return;
    live.textContent = "";
    setTimeout(function () { live.textContent = text; }, 30);
  }

  function refreshRules(list) {
    var rows = list.querySelectorAll(":scope > [data-rule]");
    rows.forEach(function (li, i) {
      li.querySelector('[data-rule-move="up"]').disabled = i === 0;
      li.querySelector('[data-rule-move="down"]').disabled = i === rows.length - 1;
    });
  }

  // flip animates nodes from their old positions (rects taken before the DOM change).
  function flip(nodes, before) {
    if (reducedMotion() || typeof Element.prototype.animate !== "function") return;
    nodes.forEach(function (n, i) {
      var dy = before[i].top - n.getBoundingClientRect().top;
      if (dy) n.animate([{ transform: "translateY(" + dy + "px)" }, { transform: "none" }], { duration: 220, easing: "cubic-bezier(0.2, 0.7, 0.2, 1)" });
    });
  }

  function moveRule(li, dir, keepFocus) {
    var other = dir === "up" ? li.previousElementSibling : li.nextElementSibling;
    if (!other || !other.hasAttribute("data-rule")) return;
    var list = li.parentNode;
    var before = [li.getBoundingClientRect(), other.getBoundingClientRect()];
    if (dir === "up") list.insertBefore(li, other);
    else list.insertBefore(other, li);
    flip([li, other], before);
    refreshRules(list);
    li.classList.remove("is-moved");
    void li.offsetWidth;
    li.classList.add("is-moved");
    var focus = keepFocus;
    if (!focus || focus.disabled) {
      var btn = li.querySelector('[data-rule-move="' + dir + '"]');
      focus = btn.disabled ? li.querySelector('[data-rule-move="' + (dir === "up" ? "down" : "up") + '"]') : btn;
    }
    if (focus && !focus.disabled) focus.focus();
    var pos = Array.prototype.indexOf.call(list.children, li) + 1;
    var form = li.closest("form");
    announce(form, "已將" + ruleName(li) + "移到第 " + pos + " 條");
    changed(form);
  }

  function removeRule(li) {
    var list = li.parentNode;
    var form = li.closest("form");
    var next = li.nextElementSibling || li.previousElementSibling;
    var name = ruleName(li);
    li.parentNode.removeChild(li);
    refreshRules(list);
    var focus = next ? next.querySelector("[data-rule-remove]") : form.querySelector("[data-rule-add]");
    if (focus) focus.focus();
    announce(form, "已刪除" + name + "，儲存後生效");
    changed(form);
  }

  function addRule(btn) {
    var form = btn.closest("form");
    var tpl = byId("rule-new");
    var list = ruleList(btn);
    if (!tpl || !list) return;
    var frag = tpl.content.cloneNode(true);
    var prefix = "rule-n" + (++ruleSeq);
    frag.querySelectorAll("[id], [for], [aria-describedby], [aria-labelledby]").forEach(function (n) {
      ["id", "for", "aria-describedby", "aria-labelledby"].forEach(function (a) {
        var v = n.getAttribute(a);
        if (v) n.setAttribute(a, v.replace(/rule-new/g, prefix));
      });
    });
    var li = frag.querySelector("[data-rule]");
    li.classList.add("is-new");
    list.appendChild(frag);
    refreshRules(list);
    var name = li.querySelector("[data-rule-name]");
    li.scrollIntoView({ block: "center", behavior: reducedMotion() ? "auto" : "smooth" });
    if (name) name.focus({ preventScroll: true });
    changed(form);
  }

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!(t instanceof Element)) return;
    var mv = t.closest("[data-rule-move]");
    if (mv) { moveRule(mv.closest("[data-rule]"), mv.dataset.ruleMove, mv); return; }
    var rm = t.closest("[data-rule-remove]");
    if (rm) { removeRule(rm.closest("[data-rule]")); return; }
    var add = t.closest("[data-rule-add]");
    if (add) addRule(add);
  });

  document.addEventListener("keydown", function (e) {
    if (!e.altKey || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
    var li = e.target instanceof Element ? e.target.closest("form[data-rules-form] [data-rule]") : null;
    if (!li || e.target.tagName === "SELECT") return; // Alt+↓ opens a select's list
    e.preventDefault();
    moveRule(li, e.key === "ArrowUp" ? "up" : "down", document.activeElement);
  });

  document.addEventListener("input", function (e) {
    var t = e.target;
    if (!(t instanceof HTMLInputElement)) return;
    var li = t.closest("form[data-rules-form] [data-rule]");
    if (!li) return;
    if (t.hasAttribute("data-rule-name")) {
      var title = li.querySelector("[data-rule-title]");
      if (title) title.textContent = t.value.trim() || "未命名規則";
    } else if (t.name === "rule_from" || t.name === "rule_subject") {
      var from = li.querySelector('[name="rule_from"]').value.trim();
      var subj = li.querySelector('[name="rule_subject"]').value.trim();
      li.querySelector("[data-rule-sum]").textContent =
        (from ? "寄件者 " + from : "") + (from && subj ? " · " : "") + (subj ? "主旨 " + subj : "");
    }
  });

  document.addEventListener("click", function (e) {
    var b = e.target instanceof Element ? e.target.closest("[data-rules-compact]") : null;
    if (!b) return;
    var on = b.getAttribute("aria-pressed") !== "true";
    b.setAttribute("aria-pressed", on ? "true" : "false");
    ruleList(b).classList.toggle("is-compact", on);
  });

  /* keyword tags */

  function tagValues(src) {
    var seen = {};
    return src.value.split(/\r?\n/).map(function (s) { return s.trim(); }).filter(function (s) {
      if (!s || seen[s]) return false;
      seen[s] = true;
      return true;
    });
  }

  function enhanceTags(field) {
    var src = field.querySelector("[data-tags-source]");
    if (!src) return;
    var box = el("div", "tags-box");
    var list = el("ul", "tags-list");
    list.setAttribute("aria-label", "已加入的關鍵字");
    var input = el("input", "tags-input");
    input.type = "text";
    input.id = src.id + "-input";
    input.autocomplete = "off";
    input.spellcheck = false;
    input.placeholder = src.dataset.tagsPlaceholder || "";
    input.setAttribute("enterkeyhint", "done");
    var desc = src.getAttribute("aria-describedby");
    if (desc) input.setAttribute("aria-describedby", desc);
    if (src.getAttribute("aria-invalid") === "true") input.setAttribute("aria-invalid", "true");
    box.appendChild(list);
    box.appendChild(input);
    src.parentNode.insertBefore(box, src);
    var label = field.querySelector('label[for="' + src.id + '"]');
    if (label) label.htmlFor = input.id;
    field.classList.add("is-enhanced");

    function sync() {
      var vals = [];
      list.querySelectorAll("[data-tag]").forEach(function (c) { vals.push(c.dataset.tag); });
      src.value = vals.join("\n");
      list.hidden = vals.length === 0;
      changed(field.closest("form"));
    }

    function chip(text) {
      var li = el("li", "tag-chip");
      li.dataset.tag = text;
      li.appendChild(el("span", "tag-chip-text", text));
      var x = el("button", "tag-chip-x");
      x.type = "button";
      x.setAttribute("aria-label", "移除關鍵字 " + text);
      x.appendChild(icon("x"));
      x.addEventListener("click", function () { remove(li, true); });
      x.addEventListener("keydown", function (e) {
        if (e.key === "Backspace" || e.key === "Delete") { e.preventDefault(); remove(li, true); }
      });
      li.appendChild(x);
      return li;
    }

    function remove(li, focusNext) {
      var next = li.nextElementSibling || li.previousElementSibling;
      li.parentNode.removeChild(li);
      sync();
      if (focusNext) (next ? next.querySelector("button") : input).focus();
    }

    function add(raw) {
      var added = false;
      raw.split(/\r?\n/).forEach(function (s) {
        s = s.trim();
        if (!s) return;
        var dup = null;
        list.querySelectorAll("[data-tag]").forEach(function (c) { if (c.dataset.tag === s) dup = c; });
        if (dup) {
          dup.classList.remove("is-dup");
          void dup.offsetWidth;
          dup.classList.add("is-dup");
          return;
        }
        var c = chip(s);
        c.classList.add("is-new");
        list.appendChild(c);
        added = true;
      });
      if (added) sync();
    }

    tagValues(src).forEach(function (v) { list.appendChild(chip(v)); });
    list.hidden = !list.firstChild;
    box.addEventListener("click", function (e) { if (e.target === box || e.target === list) input.focus(); });
    input.addEventListener("keydown", function (e) {
      if (e.isComposing) return;
      if (e.key === "Enter") {
        e.preventDefault();
        add(input.value);
        input.value = "";
      } else if (e.key === "Backspace" && input.value === "") {
        var last = list.lastElementChild;
        if (last) { e.preventDefault(); last.querySelector("button").focus(); }
      }
    });
    input.addEventListener("paste", function (e) {
      var text = e.clipboardData ? e.clipboardData.getData("text") : "";
      if (/\n/.test(text)) {
        e.preventDefault();
        add(text);
      }
    });
    input.addEventListener("blur", function () {
      if (input.value.trim()) { add(input.value); input.value = ""; }
    });
    // pending text in the input counts as a keyword when the form is sent
    var form = field.closest("form");
    if (form) form.addEventListener("submit", function () {
      if (input.value.trim()) { add(input.value); input.value = ""; }
    }, true);
  }

  ready(function () {
    document.querySelectorAll("[data-rule-list]").forEach(refreshRules);
    document.querySelectorAll("[data-rules-compact]").forEach(function (b) { b.hidden = false; });
    document.querySelectorAll("[data-tags]").forEach(enhanceTags);
  });

  /* ---------- templates page ----------

    form[data-tpl-form]
      [data-tpl-tab="kind"] ／ [data-tpl-panel="kind"]   三種範本的分頁（←→ Home End 切換）；沒有 JS 時三個面板依序顯示。
                                                     隱藏的面板只加 hidden，textarea 仍會送出。
      textarea[data-tpl-source="kind"]                 範本；停頓 400ms 後以 POST /api/templates/preview 更新預覽。
      [data-tpl-insert="{{.Code}}" data-for="id"]      在游標位置插入變數。
      [data-tpl-wrap="b" data-for="id"]                以 <b>…</b> 包住選取的文字。
      [data-tpl-builtin="範本" data-for="id"]          填入內建範本；[data-tpl-clear] 清空（改用內建）。
      figure[data-tpl-preview="kind" data-for="id"]    Telegram 訊息預覽。

    預覽的 HTML 已由伺服器過濾成 Telegram 的標籤子集；這裡仍以 DOMParser 在不執行、不載入的文件中
    解析，再只複製允許的元素（a 只留 http／https／tg 的 href）與文字到畫面上，不使用 innerHTML。
  */

  var TG_TAGS = { B: 1, STRONG: 1, I: 1, EM: 1, U: 1, INS: 1, S: 1, STRIKE: 1, DEL: 1, CODE: 1, PRE: 1, A: 1, BLOCKQUOTE: 1, BR: 1 };
  var TG_HREF = /^\s*(https?|tg):/i;

  function copyAllowed(from, to) {
    Array.prototype.forEach.call(from.childNodes, function (n) {
      if (n.nodeType === 3) {
        to.appendChild(document.createTextNode(n.nodeValue));
      } else if (n.nodeType === 1) {
        if (!TG_TAGS[n.tagName]) { copyAllowed(n, to); return; }
        var e = document.createElement(n.tagName.toLowerCase());
        if (n.tagName === "A") {
          var href = n.getAttribute("href");
          if (href && TG_HREF.test(href)) {
            e.setAttribute("href", href.trim());
            e.setAttribute("target", "_blank");
            e.setAttribute("rel", "noopener noreferrer");
          }
        }
        copyAllowed(n, e);
        to.appendChild(e);
      }
    });
  }

  function showSafeHTML(node, html) {
    var doc = new DOMParser().parseFromString("<!doctype html><body>" + html, "text/html");
    var frag = document.createDocumentFragment();
    copyAllowed(doc.body, frag);
    node.textContent = "";
    node.appendChild(frag);
  }

  function nowHM() { var d = new Date(); return pad(d.getHours()) + ":" + pad(d.getMinutes()); }

  var previews = {}; // kind -> { seq, timer, typing }

  function pq(fig, sel) { return fig.querySelector(sel); }

  function tabState(kind, text, cls) {
    var tab = document.querySelector('[data-tpl-tab="' + kind + '"] [data-tpl-tab-state]');
    if (!tab) return;
    tab.textContent = text;
    tab.classList.toggle("is-err", cls === "err");
    tab.classList.toggle("is-custom", cls === "custom");
  }

  function runPreview(fig) {
    var kind = fig.dataset.tplPreview;
    var src = byId(fig.dataset.for);
    var st = previews[kind] || (previews[kind] = { seq: 0 });
    var seq = ++st.seq;
    var sub = pq(fig, "[data-pv-sub]");
    clearTimeout(st.typing);
    st.typing = setTimeout(function () { sub.textContent = "正在輸入…"; sub.classList.add("is-typing"); }, 120);
    var body = { kind: kind, template: src.value };
    if (fig.dataset.mailbox) body.mailbox = fig.dataset.mailbox;
    fetchJSON("/api/templates/preview", body).then(function (d) {
      if (seq !== st.seq) return;
      done();
      if (!d.ok) { fail(d.error || "無法產生預覽"); return; }
      pq(fig, "[data-pv-error]").hidden = true;
      var bubble = pq(fig, "[data-pv-msg]");
      bubble.classList.remove("is-stale");
      bubble.hidden = false;
      fig.dataset.shown = "1";
      showSafeHTML(pq(fig, "[data-pv-text]"), d.html || "");
      pq(fig, "[data-pv-time]").textContent = nowHM();
      var kb = pq(fig, "[data-pv-kb]");
      kb.textContent = "";
      (d.buttons || []).forEach(function (b) {
        var k = el("span", "tg-key" + (b.url ? " tg-key-url" : " tg-key-copy"), b.text);
        k.title = b.url ? "開啟 " + b.url : "複製「" + b.copy + "」";
        kb.appendChild(k);
      });
      kb.hidden = !kb.firstChild;
      var len = pq(fig, "[data-pv-len]");
      len.textContent = "長度 " + d.length + " / " + d.max;
      len.classList.toggle("is-near", d.length > d.max * 0.85);
      pq(fig, "[data-pv-raw]").textContent = d.text || "";
      var warn = pq(fig, "[data-pv-warn]");
      var tags = d.unsupported || [];
      warn.hidden = tags.length === 0;
      pq(fig, "[data-pv-warn-tags]").textContent = tags.map(function (t) { return "<" + t + ">"; }).join("、");
      if (src.value.trim()) tabState(kind, "已自訂", "custom");
      else tabState(kind, "內建", "");
    }, function (err) {
      if (seq !== st.seq) return;
      done();
      fail(err.message);
    });

    function done() {
      clearTimeout(st.typing);
      sub.textContent = "bot";
      sub.classList.remove("is-typing");
    }
    function fail(msg) {
      pq(fig, "[data-pv-error-text]").textContent = msg;
      pq(fig, "[data-pv-error]").hidden = false;
      var bubble = pq(fig, "[data-pv-msg]");
      bubble.classList.add("is-stale");
      if (!fig.dataset.shown) bubble.hidden = true; // nothing good to show yet
      tabState(kind, "有錯誤", "err");
    }
  }

  function schedulePreview(src) {
    var fig = document.querySelector('[data-tpl-preview][data-for="' + src.id + '"]');
    if (!fig) return;
    var st = previews[fig.dataset.tplPreview] || (previews[fig.dataset.tplPreview] = { seq: 0 });
    clearTimeout(st.timer);
    st.timer = setTimeout(function () { runPreview(fig); }, 400);
  }

  function setTemplate(ta, value) {
    ta.value = value;
    ta.focus();
    ta.setSelectionRange(value.length, value.length);
    ta.dispatchEvent(new Event("input", { bubbles: true }));
  }

  function selectTab(form, kind, focus) {
    form.querySelectorAll("[data-tpl-tab]").forEach(function (t) {
      var on = t.dataset.tplTab === kind;
      t.setAttribute("aria-selected", on ? "true" : "false");
      t.tabIndex = on ? 0 : -1;
      if (on && focus) t.focus();
    });
    form.querySelectorAll("[data-tpl-panel]").forEach(function (p) { p.hidden = p.dataset.tplPanel !== kind; });
  }

  document.addEventListener("input", function (e) {
    var t = e.target;
    if (t instanceof HTMLTextAreaElement && t.hasAttribute("data-tpl-source")) {
      if (t.getAttribute("aria-invalid") === "true") t.removeAttribute("aria-invalid");
      schedulePreview(t);
    }
  });

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!(t instanceof Element)) return;
    var tab = t.closest("[data-tpl-tab]");
    if (tab) { selectTab(tab.closest("form"), tab.dataset.tplTab, false); return; }
    var ins = t.closest("[data-tpl-insert]");
    if (ins) {
      var ta = byId(ins.dataset.for);
      ta.focus();
      ta.setRangeText(ins.dataset.tplInsert, ta.selectionStart, ta.selectionEnd, "end");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
      return;
    }
    var wrap = t.closest("[data-tpl-wrap]");
    if (wrap) {
      var w = byId(wrap.dataset.for);
      var tag = wrap.dataset.tplWrap;
      var s = w.selectionStart, en = w.selectionEnd;
      w.focus();
      w.setRangeText("<" + tag + ">" + w.value.slice(s, en) + "</" + tag + ">", s, en, "select");
      if (s === en) w.setSelectionRange(s + tag.length + 2, s + tag.length + 2);
      w.dispatchEvent(new Event("input", { bubbles: true }));
      return;
    }
    var bi = t.closest("[data-tpl-builtin]");
    if (bi) {
      var b = byId(bi.dataset.for);
      var text = bi.dataset.tplBuiltin;
      if (b.value.trim() && b.value.trim() !== text.trim()) {
        confirmDialog("以內建範本取代目前的內容？", { ok: "取代" }).then(function (yes) { if (yes) setTemplate(b, text); });
      } else setTemplate(b, text);
      return;
    }
    var clr = t.closest("[data-tpl-clear]");
    if (clr) setTemplate(byId(clr.dataset.for), "");
  });

  document.addEventListener("keydown", function (e) {
    var tab = e.target instanceof Element ? e.target.closest("[data-tpl-tab]") : null;
    if (!tab) return;
    var tabs = Array.prototype.slice.call(tab.parentNode.querySelectorAll("[data-tpl-tab]"));
    var i = tabs.indexOf(tab);
    var to = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key];
    if (to === undefined) return;
    e.preventDefault();
    to = (to + tabs.length) % tabs.length;
    selectTab(tab.closest("form"), tabs[to].dataset.tplTab, true);
  });

  ready(function () {
    document.querySelectorAll("form[data-tpl-form]").forEach(function (form) {
      form.classList.add("is-tabbed");
      var bad = form.querySelector('[data-tpl-panel] [aria-invalid="true"]');
      var first = bad ? bad.closest("[data-tpl-panel]") : form.querySelector("[data-tpl-panel]");
      var hash = (location.hash || "").slice(1);
      if (!bad && /^(code|link|both)$/.test(hash)) first = form.querySelector('[data-tpl-panel="' + hash + '"]') || first;
      if (first) selectTab(form, first.dataset.tplPanel, false);
    });
    document.querySelectorAll("[data-tpl-preview]").forEach(runPreview);
  });

  /* ---------- overview ----------

    [data-status-board]   總覽：每 10 秒 GET /api/status，原地更新系統狀態（郵戳、摘要數字）、每張信箱卡片
                          （[data-mb] 的狀態、[data-folder] 徽章、說明與動作）與最近轉發（[data-recent]）。
                          標籤、樣式與判斷都由伺服器決定，這裡只寫入；文字一律 textContent。
                          分頁隱藏時暫停，回到前景立即更新；這個輪詢不算使用中（session 照樣會閒置逾時）。
                          信箱增減時不重新整理，只顯示 [data-board-changed]。卡片的動作按鈕取自
                          <template id="tpl-mb-oauth">／<template id="tpl-mb-edit">。
  */

  var STATUS_EVERY = 10000;
  var board = { gen: 0, timer: 0, recent: null };

  function boardEl() { return document.querySelector("[data-status-board]"); }

  function setText(root, sel, text) {
    var n = root.querySelector(sel);
    if (n && n.textContent !== String(text)) n.textContent = String(text);
    return n;
  }

  function timeEl(iso) {
    var t = el("time", null, iso.replace("T", " ").replace(/:\d\dZ$/, " UTC"));
    t.setAttribute("datetime", iso);
    t.setAttribute("data-localtime", "");
    return t;
  }

  function liveNote(ok, text) {
    var n = document.querySelector("[data-live-note]");
    if (!n) return;
    n.classList.toggle("is-off", !ok);
    setText(n, "[data-live-text]", text);
  }

  function badge(cls, live, label) {
    return el("span", "badge " + (cls || "badge-idle") + (live ? " badge-live" : ""), label);
  }

  function setBadge(node, cls, live, label) {
    var want = "badge " + (cls || "badge-idle") + (live ? " badge-live" : "");
    if (node.className !== want) node.className = want;
    if (node.textContent !== label) node.textContent = label;
  }

  function updateHealth(root, d) {
    var h = d.health || {};
    if (root.dataset.state !== h.state) {
      root.dataset.state = h.state || "idle";
      var mark = root.querySelector(".health-mark");
      if (mark && !reducedMotion()) {
        mark.classList.remove("is-restamp");
        void mark.getBoundingClientRect();
        mark.classList.add("is-restamp");
      }
    }
    setText(root, "[data-health-word]", h.word || "");
    setText(root, "[data-health-title]", h.title || "");
    setText(root, "[data-health-text]", h.text || "");
    setText(root, '[data-fact="mailboxes"]', h.mailboxes || 0);
    setText(root, '[data-fact="ok"]', h.ok || 0);
    setText(root, '[data-fact="bad"]', h.bad || 0);
    setText(root, '[data-fact="pending"]', d.pending || 0);
    var ok = root.querySelector('[data-fact-cell="ok"]');
    if (ok) ok.classList.toggle("is-hot", h.ok > 0);
    var bad = root.querySelector('[data-fact-cell="bad"]');
    if (bad) bad.classList.toggle("is-hot", h.bad > 0);
    var pend = root.querySelector('[data-fact-cell="pending"]');
    if (pend) pend.classList.toggle("is-hot", d.pending > 0);
    var applied = root.querySelector('[data-fact="applied"]');
    if (applied) {
      var cur = applied.querySelector("time");
      if (!d.applied_at) setText(root, '[data-fact="applied"]', "尚未套用");
      else if (!cur || cur.getAttribute("datetime") !== d.applied_at) {
        applied.textContent = "";
        applied.appendChild(timeEl(d.applied_at));
        localTimes(applied);
      }
    }
    var now = new Date();
    setText(root, "[data-health-stamp]", pad(now.getHours()) + ":" + pad(now.getMinutes()) + " 更新");
  }

  function cardAction(card, c) {
    var slot = card.querySelector("[data-mb-action]");
    if (!slot) return;
    var has = slot.querySelector("[data-oauth-start]") ? "oauth" : slot.querySelector("a") ? "edit" : "";
    if (has === (c.action || "")) return;
    slot.textContent = "";
    var tpl = byId(c.action === "oauth" ? "tpl-mb-oauth" : c.action === "edit" ? "tpl-mb-edit" : "");
    if (!tpl) return;
    var node = tpl.content.firstElementChild.cloneNode(true);
    var name = card.dataset.mb;
    if (c.action === "oauth") {
      node.dataset.oauthStart = name;
      node.dataset.oauthUser = card.dataset.oauthUser || "";
    } else node.href = "/mailboxes/" + encodeURIComponent(name);
    slot.appendChild(node);
  }

  // updateCards writes each card's verdict; it reports whether the set of mailboxes changed.
  function updateCards(root, d) {
    var byName = {};
    (d.mailboxes || []).forEach(function (c) { byName[c.mailbox] = c; });
    var cards = root.querySelectorAll("[data-mb]");
    var changed = cards.length !== (d.mailboxes || []).length;
    cards.forEach(function (card) {
      var c = byName[card.dataset.mb];
      if (!c) { changed = true; return; }
      if (card.dataset.state !== c.state) card.dataset.state = c.state;
      var folders = {};
      (c.folders || []).forEach(function (f) { folders[f.folder] = f; });
      card.querySelectorAll("[data-folder]").forEach(function (row) {
        var f = folders[row.dataset.folder];
        var b = row.querySelector(".badge");
        if (!f) { changed = true; return; }
        if (b) setBadge(b, f.class, f.live, f.label);
        else row.appendChild(badge(f.class, f.live, f.label));
      });
      if ((c.folders || []).length !== card.querySelectorAll("[data-folder]").length) changed = true;
      var foot = card.querySelector("[data-mb-foot]");
      setText(card, "[data-mb-note]", c.note || "");
      cardAction(card, c);
      if (foot) foot.hidden = !c.note && !c.action;
    });
    return changed;
  }

  function recentRow(r) {
    var tr = document.createElement("tr");
    var time = el("td", "c-time");
    if (r.at) time.appendChild(timeEl(r.at));
    tr.appendChild(time);
    var mb = el("td", "c-mb");
    var label = r.mailbox_label || r.mailbox;
    var name = mb.appendChild(el("span", "log-mb", label));
    if (label !== r.mailbox) name.appendChild(el("span", "log-key mono", r.mailbox));
    var folder = mb.appendChild(el("span", "log-folder mono", r.folder));
    folder.title = r.folder;
    tr.appendChild(mb);
    var msg = el("td", "c-msg");
    var subj = el("span", "log-subject", r.subject ? r.subject : null);
    if (!r.subject) subj.appendChild(el("span", "muted", "（沒有主旨）"));
    msg.appendChild(subj);
    msg.appendChild(el("span", "log-from mono", r.from));
    tr.appendChild(msg);
    var kind = el("td", "c-kind");
    kind.appendChild(el("span", "tag" + (r.hit ? " tag-hit" : ""), r.kind_label));
    tr.appendChild(kind);
    var res = el("td", "c-result");
    res.appendChild(badge(r.result_class, r.live, r.result_label));
    tr.appendChild(res);
    return tr;
  }

  function updateRecent(root, d) {
    var body = root.querySelector("[data-recent]");
    if (!body) return;
    var rows = d.recent || [];
    var sig = JSON.stringify(rows);
    if (sig === board.recent) return;
    board.recent = sig;
    var frag = document.createDocumentFragment();
    rows.forEach(function (r) { frag.appendChild(recentRow(r)); });
    body.textContent = "";
    body.appendChild(frag);
    localTimes(body);
    var wrap = root.querySelector("[data-recent-wrap]");
    var empty = root.querySelector("[data-recent-empty]");
    if (wrap) wrap.hidden = rows.length === 0;
    if (empty) empty.hidden = rows.length > 0;
  }

  function boardFetch() {
    clearTimeout(board.timer);
    var root = boardEl();
    if (!root || document.visibilityState === "hidden") return;
    var gen = ++board.gen;
    fetchJSON("/api/status").then(
      function (d) {
        if (gen !== board.gen) return;
        if (!d || d.ok === false || !d.health) {
          liveNote(false, "暫時無法更新狀態" + (d && d.error ? "：" + d.error : "") + "，稍後自動重試");
        } else {
          updateHealth(root, d);
          var changed = updateCards(root, d);
          var note = root.querySelector("[data-board-changed]");
          if (note) note.hidden = !changed;
          updateRecent(root, d);
          liveNote(true, "每 10 秒自動更新");
        }
        board.timer = setTimeout(boardFetch, STATUS_EVERY);
      },
      function (err) {
        if (gen !== board.gen) return;
        liveNote(false, err.message + "，稍後自動重試");
        board.timer = setTimeout(boardFetch, STATUS_EVERY);
      }
    );
  }

  ready(function () {
    var root = boardEl();
    if (!root) return;
    var now = new Date();
    setText(root, "[data-health-stamp]", pad(now.getHours()) + ":" + pad(now.getMinutes()) + " 更新");
    board.timer = setTimeout(boardFetch, STATUS_EVERY);
    document.addEventListener("visibilitychange", function () {
      if (document.visibilityState === "hidden") {
        clearTimeout(board.timer);
        board.gen++; // an answer still on its way is dropped
        liveNote(true, "分頁在背景，已暫停更新");
      } else boardFetch();
    });
  });

  ready(snapshotForms);

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
