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
