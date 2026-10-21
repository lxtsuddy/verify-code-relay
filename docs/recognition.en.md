# Recognition rules and templates

[繁體中文](recognition.md) | English

## How built-in recognition works

- **Body source**: the HTML body comes first. Only when nothing is found there is the plain-text body tried in a second pass. Some mails show the code in bold only in the HTML part, and others explain things fully only in the plain-text part.
- **Codes**: a number is recognised when keywords such as 驗證碼, "this code", "code below", "PIN is", "one-time PIN", 動態密碼 or 安全性代碼 appear near it. Bold, large or letter-spaced text scores higher. A bare "PIN" or "the code" is not a keyword, because those are common in "forgot your PIN? call us" or promo codes. For mails that only say "PIN: 4821", add your own keyword.
  - A number from the subject that also stands alone on a line in the body counts as a code even without a code keyword nearby. Example: subject "Use 123456 to set up recovery email" with `123456` on its own line in the body. Years from 1900 to 2099 are excluded, as are subjects about orders, invoices, shipping, payments, bookings, cases or accounts (order, invoice, payment, booking, case, account …).
  - All-letter codes (5–8 capital letters) count only right after "code: / code is / 驗證碼：" and similar, and only when they stand alone on a line or form a bold run of their own. The READY in a heading "YOUR CODE IS READY" does not count.
- **Links**: link keywords (驗證, 確認, 登入, 重設密碼, verify, confirm, sign in …) may appear in the subject, the body, or the text of any link.
  - Buttons that just say 驗證, 验证 or "Verify", and image buttons (using the image's alt text), are recognised too.
  - Generic link text such as "Click here" or 點此 is used only when its purpose is explained:
    - on the same line ("To verify your email, click here"), or
    - on the line before, pointing down to the link (containing below, link, 以下 or 下方, or ending in a colon).

    Bare URLs in plain-text mail are treated the same way. The English keywords here must be whole words ("confirmation" is not "confirm"). Such links are not used when the text before them or the subject mentions orders, receipts, payments or shipping (order, receipt, payment, 訂單, 付款 …), so the order page under "Order confirmation:" is not forwarded.
  - `activation`, `validation`, `verify`, `token=` and similar in the URL also add to the score.
- **Excluded**:
  - Numbers that are not codes:
    - card number endings (末四碼, 尾號, ending in, last 4 …);
    - postal codes (郵遞區號, 邮编, zip, postal, and US `CA 94043`);
    - street numbers (`1600 Main St`, `100 號`);
    - phone numbers, amounts, times, years and order numbers.
  - Promo codes: codes with promo, coupon, discount, voucher, checkout, % off, 優惠, 折扣 or 折價 on the same or previous line ("Use this code SAVE20 at checkout").
  - Typical newsletter links: unsubscribe, view in browser, contact us, contact (only the link text and URL path are checked), plus images and static files such as `.pdf`, `.css` and `.js`. The text of these links also does not count when checking whether a mail contains a link keyword.
  - Account-recovery links: a link is not treated as a sign-in link when the text before it says "If this wasn't you", "didn't try to sign in", "didn't make this change", 若非本人操作, 密碼可能已外洩 or similar, on the same or previous line. When such a phrase is followed by "ignore this email" or 請忽略 (e.g. 若非本人操作請忽略：[登入]), the mail is a sign-in or reset you asked for, and the link is forwarded as usual.
  - When a mail already has a code, links for resetting or changing the password, securing the account or reviewing activity are not forwarded with it; only the code is. These are links whose text or URL path contains password, 密碼, secure, security, activity, 安全 and the like.
  - At most 500 distinct links are examined per mail, and mails over 5 MiB are skipped.

## When recognition misses or gets it wrong

1. Open **規則與範本 → 辨識規則 (Rules & templates → Recognition rules)** and use 用信件測試 (Test with a mail) at the bottom of the page. Upload the mail and read the 辨識過程 (recognition trace); see the next section. The command line `relay test --eml mail.eml` produces the same trace (see the [deployment guide](deploy.en.md#day-to-day-operations) for K3s/Docker usage).
2. Missing keyword: add a code or link keyword under 辨識關鍵字 (Recognition keywords) on the same page.
3. A sender with an unusual format: add a rule under 自訂規則 (Custom rules) on the same page. A rule matches the sender and subject and gives a regex for the code or the link. Alternatively, set its action to 略過，不轉發 (Skip, don't forward).
   - Code regex: matched against "subject + newline + body". With capture groups, the first group that matched is used; without groups, the whole match.
   - Link regex: **selects** a link rather than extracting part of a URL. If a link's URL matches, that link's full URL is forwarded.
4. Changes apply on save; send the mail again or re-run Test with a mail to confirm.

If built-in recognition gets a common format wrong, issues and pull requests with a test mail are welcome (please remove personal data first); see "Development" in the README.

## Testing with a mail

At the bottom of **規則與範本 → 辨識規則 (Rules & templates → Recognition rules)**:

- Upload an `.eml` (or drop the file onto the box), or switch to 貼上原始碼 (Paste source) and paste the raw message. To get one:
  - Gmail: "⋮" at the top right of the message → "Download message" gives an `.eml`. Or use "Show original" → "Copy to clipboard" and paste. Don't select the whole page, because the summary table at the top breaks the format.
  - Outlook: "⋯" → "View" → "View message source", then copy and paste.
- The custom rules and keywords **currently filled in on the page** are used, even unsaved, so you can tweak and test before saving.
- 預覽訊息使用的信箱 (Mailbox for the preview) is optional. Without it the global templates are used; with it, that mailbox's templates and display name.
- The result shows:
  - the verdict (forward code / forward link / skipped by rule / not forwarded) and the code or link;
  - a preview of the Telegram message;
  - the recognition trace, with every candidate's score and the reason it was excluded;
  - a summary of the mail: sender, subject, the first 500 characters of the body, and the list of links.
- The mail is processed in memory only. It is not stored, not sent to Telegram and not logged. The limit is 5 MiB, the same size above which live mail is skipped; larger files are rejected in the browser before upload. JavaScript is required.

The command line `relay test --eml` uses the same recognition and rendering as the web page. Only it can actually send to Telegram with `--send`.

## Templates

Edit the global templates under **規則與範本 → 訊息範本 (Rules & templates → Message templates)**. A mailbox's 進階設定 (Advanced settings) can override them for that mailbox, with a live preview while editing. The `code`, `link` or `both` template is picked based on the result and looked up in this order: mailbox template → global template → built-in.

Templates use Go `html/template` (Telegram HTML format), and mail content is escaped automatically. Available fields:

| Field | Description |
|---|---|
| `.Code` | The code |
| `.URL` | The full URL of the link |
| `.LinkText` | The link's text in the mail, e.g. "Verify email" |
| `.Host` | The link's host name |
| `.Service` | Service name: the sender's name, or the main part of the sender's domain (e.g. `github`) if there is none |
| `.FromName` `.FromAddr` | Sender name and address |
| `.To` | Recipient: the address that belongs to this mailbox's account (so `+aliases` show), otherwise the first recipient |
| `.Subject` | Subject |
| `.Mailbox` | The mailbox's display name |
| `.Date` | When the mail was received (e.g. `.Date.Format "15:04"`) |
| `.Rule` | Name of the custom rule that matched |

The built-in templates do not print the full URL. Instead, a domain line (`🌐 github.com`) shows where the link goes. Sender, recipient (with the mailbox display name) and subject follow on separate lines, and lines for empty fields are left out. Buttons are generated automatically:

- 📋 複製驗證碼 (Copy code);
- a link button named after the link text in the mail, e.g. 🔗 重設密碼 (Reset password). Text longer than 32 characters is truncated, and a link without text shows 🔗 開啟 <domain> (Open <domain>).

If Telegram rejects the message's HTML and it is resent as plain text, there are no buttons; links are appended at the end as "button text: URL". Custom templates that use `{{.URL}}` still print the full URL.

Templates can only print in sequence. Allowed:

- fields (including methods like `.Date.Format "15:04"`), `if` / `else if` / `else`, `with .Field`, and pipelines of at most two stages (e.g. `{{.Subject | html}}`);
- the functions `and` `or` `not` `eq` `ne` `lt` `le` `gt` `ge` `len` `index` `slice` `html` `urlquery` `js` `print` `printf` `println`. Each action takes at most 10 arguments. The `printf` format must be written literally in the template, with width and precision of at most 3 digits.

Not supported: `range`, `template`, `define` / `block`, variables (`$`) and parenthesised sub-expressions.
