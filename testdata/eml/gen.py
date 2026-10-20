#!/usr/bin/env python3
"""Generates the recognizer test corpus. Re-run after editing: python3 testdata/eml/gen.py"""
import base64, os

HERE = os.path.dirname(os.path.abspath(__file__))

def b64(data: bytes) -> str:
    s = base64.b64encode(data).decode()
    return "\r\n".join(s[i:i + 76] for i in range(0, len(s), 76))

def word(text: str, charset: str) -> str:
    return "=?%s?B?%s?=" % (charset, base64.b64encode(text.encode(charset)).decode())

def write(name, headers, body_parts, expected):
    lines = [f"{k}: {v}" for k, v in headers]
    lines.append("MIME-Version: 1.0")
    if len(body_parts) == 1:
        ctype, charset, body = body_parts[0]
        lines += [f"Content-Type: {ctype}; charset={charset}", "Content-Transfer-Encoding: base64", "", b64(body.encode(charset))]
    else:
        lines += ['Content-Type: multipart/alternative; boundary="BOUNDARY"', ""]
        for ctype, charset, body in body_parts:
            lines += ["--BOUNDARY", f"Content-Type: {ctype}; charset={charset}", "Content-Transfer-Encoding: base64", "", b64(body.encode(charset))]
        lines.append("--BOUNDARY--")
    with open(os.path.join(HERE, name + ".eml"), "wb") as f:
        f.write(("\r\n".join(lines) + "\r\n").encode("ascii"))
    with open(os.path.join(HERE, name + ".expected.yaml"), "w", encoding="utf-8") as f:
        f.write(expected.strip() + "\n")

DATE = ("Date", "Mon, 19 Oct 2026 08:00:00 +0800")

write("github-device-en",
      [("From", '"GitHub" <noreply@github.com>'), ("To", "me@gmail.com"), ("Subject", "[GitHub] Please verify your device"), DATE],
      [("text/plain", "utf-8", "Hey me!\n\nA sign in attempt requires further verification because we did not recognize your device.\n\nVerification code: 482913\n\nIf you did not attempt to sign in to your account, your password may be compromised.\nVisit https://github.com/settings/security to create a new, strong password.\n\nThanks,\nThe GitHub Team\n"),
       ("text/html", "utf-8", "<p>Hey me!</p><p>Verification code: <strong>482913</strong></p><p><a href=\"https://github.com/settings/security\">Change password</a></p>")],
      'code: "482913"')

write("google-code-en",
      [("From", "Google <no-reply@accounts.google.com>"), ("To", "me@gmail.com"), ("Subject", "G-583920 is your Google verification code"), DATE],
      [("text/plain", "utf-8", "Google\nYour verification code is G-583920.\nDon't share it with anyone.\n© 2026 Google LLC, 1600 Amphitheatre Parkway, Mountain View, CA 94043\n")],
      'code: "583920"')

write("microsoft-security-code-en",
      [("From", "Microsoft account team <account-security-noreply@accountprotection.microsoft.com>"), ("To", "me@outlook.com"), ("Subject", "Microsoft account security code"), DATE],
      [("text/plain", "utf-8", "Microsoft account\nSecurity code\n\nPlease use the following security code for the Microsoft account me@outlook.com.\n\nSecurity code: 7291\n\nIf you don't recognize the Microsoft account me@outlook.com, you can click https://account.live.com/dp?ft=-Dj7a8sQ2 to remove your email address from that account.\n\nThanks,\nThe Microsoft account team\n")],
      'code: "7291"')

write("bank-zh-tw-big5",
      [("From", word("範例銀行", "big5") + " <notify@bank.example.com.tw>"), ("To", "me@example.com"), ("Subject", word("【範例銀行】網路銀行登入驗證碼", "big5")), DATE],
      [("text/html", "big5", "<html><body><p>親愛的客戶您好：</p><p>您的驗證碼為 <b>604217</b>，有效時間 5 分鐘，請勿提供給他人。</p><p>本次交易金額 NT$ 1,280。</p><p>如有疑問請洽客服專線 0800-000-123。</p><p>範例銀行 敬上 2026年10月19日</p></body></html>")],
      'code: "604217"')

write("shop-zh-cn-gb2312",
      [("From", word("某商城", "gb2312") + " <service@shop.example.cn>"), ("To", "me@example.com"), ("Subject", word("登录验证码", "gb2312")), DATE],
      [("text/plain", "gb2312", "【某商城】验证码：917344，用于登录，5分钟内有效。如非本人操作，请忽略本邮件。\n您最近的订单号 20261019123 已发货。\n")],
      'code: "917344"')

write("ja-login-iso2022jp",
      [("From", "Example <no-reply@example.co.jp>"), ("To", "me@example.com"), ("Subject", word("ログイン認証コードのお知らせ", "iso-2022-jp")), DATE],
      [("text/plain", "iso-2022-jp", "いつもご利用ありがとうございます。\n認証コード：338120\nこのコードの有効期限は10分です。\n")],
      'code: "338120"')

write("slack-magic-link-en",
      [("From", "Slack <no-reply@slack.com>"), ("To", "me@gmail.com"), ("Subject", "Sign in to Example on Slack"), DATE],
      [("text/html", "utf-8", "<p>Click the button below to sign in to Slack. This link will expire in 24 hours.</p><a href=\"https://example.slack.com/z-app-1234-abcd/magic?t=xoxm7YbQ2LcP0aZ9eKfTqWv3\">Sign in to Slack</a><p><a href=\"https://slack.com/help\">Help</a> <a href=\"https://slack.com/unsubscribe?e=1\">Unsubscribe</a></p>")],
      'url: "https://example.slack.com/z-app-1234-abcd/magic?t=xoxm7YbQ2LcP0aZ9eKfTqWv3"')

write("verify-email-link-en",
      [("From", "Example <hello@mail.example.com>"), ("To", "me@gmail.com"), ("Subject", "Confirm your email address"), DATE],
      [("text/html", "utf-8", "<img src=\"https://t.example.com/open.gif\"><h1>Welcome!</h1><p>Please confirm your email address to finish signing up.</p><a href=\"https://click.mail.example.com/ls/click?upn=AbCdEf1234567890GhIjKl\">Confirm email</a><p>Follow us: <a href=\"https://twitter.com/example\">Twitter</a> <a href=\"https://www.facebook.com/example\">Facebook</a></p><p><a href=\"https://example.com/privacy\">Privacy</a> · <a href=\"https://click.mail.example.com/unsubscribe?u=9\">Unsubscribe</a></p>")],
      'url: "https://click.mail.example.com/ls/click?upn=AbCdEf1234567890GhIjKl"')

write("reset-password-en",
      [("From", "Acme <security@acme.example>"), ("To", "me@gmail.com"), ("Subject", "Reset your Acme password"), DATE],
      [("text/plain", "utf-8", "Someone requested a password reset for your Acme account.\n\nReset your password:\nhttps://acme.example/account/reset-password?token=q8Zp3LmN0aB7cD2eF5gH\n\nIf you didn't request this, you can ignore this email.\nNeed help? https://acme.example/support\n")],
      'url: "https://acme.example/account/reset-password?token=q8Zp3LmN0aB7cD2eF5gH"')

write("reset-password-zh",
      [("From", word("範例服務", "utf-8") + " <noreply@service.example.com.tw>"), ("To", "me@example.com"), ("Subject", word("重設密碼通知", "utf-8")), DATE],
      [("text/html", "utf-8", "<p>您好，我們收到了重設密碼的請求。</p><p><a href=\"https://service.example.com.tw/password/reset?code=7f3a9c2e1b5d4f6a8c0e\">重設密碼</a></p><p>若非本人操作請忽略此信。<a href=\"https://service.example.com.tw/help\">說明中心</a></p>")],
      'url: "https://service.example.com.tw/password/reset?code=7f3a9c2e1b5d4f6a8c0e"')

write("code-and-link-en",
      [("From", "Example <noreply@example.com>"), ("To", "me+shop@gmail.com"), ("Subject", "Verify your email for Example"), DATE],
      [("text/plain", "utf-8", "Your verification code is 250391.\n\nOr verify your email by opening this link:\nhttps://example.com/verify?token=Hk29dLqP0zXv8MnB4cRt\n\nThis code expires in 10 minutes.\n")],
      'code: "250391"\nurl: "https://example.com/verify?token=Hk29dLqP0zXv8MnB4cRt"')

write("newsletter-en",
      [("From", "Example Weekly <weekly@example.com>"), ("To", "me@gmail.com"), ("Subject", "This week: 5 stories you missed"), DATE],
      [("text/html", "utf-8", "<h2>Top stories</h2><p>1. Our 2026 roadmap — <a href=\"https://example.com/blog/2026-roadmap\">Read more</a></p><p>2. Join 12000 developers at our conference on Oct 19, 2026.</p><p><a href=\"https://example.com/unsubscribe\">Unsubscribe</a></p>")],
      "none: true")

write("order-shipped-zh",
      [("From", word("範例購物", "utf-8") + " <order@shop.example.com.tw>"), ("To", "me@example.com"), ("Subject", word("您的訂單已出貨", "utf-8")), DATE],
      [("text/html", "utf-8", "<p>親愛的會員您好，您的訂單 #A1234567 已於 2026年10月19日 出貨。</p><p>訂單金額 NT$ 2,580。請確認收貨後給予評價。</p><a href=\"https://shop.example.com.tw/order/A1234567\">查看訂單</a>")],
      "none: true")

write("invoice-en",
      [("From", "Billing <billing@saas.example>"), ("To", "me@gmail.com"), ("Subject", "Your invoice #20261019 is ready"), DATE],
      [("text/plain", "utf-8", "Invoice #20261019\nAmount due: $1,234.00 on 2026-11-01\nQuestions? Call +1 (800) 555-0199.\nView invoice: https://saas.example/invoices/20261019\n")],
      "none: true")

write("security-alert-en",
      [("From", "Google <no-reply@accounts.google.com>"), ("To", "me@gmail.com"), ("Subject", "Security alert"), DATE],
      [("text/html", "utf-8", "<p>We noticed a new sign in to your Google Account on a Windows device. If this was you, you don't need to do anything.</p><a href=\"https://accounts.google.com/AccountChooser?Email=me@gmail.com&continue=https://myaccount.google.com/alert/nt/1729300000000\">Check activity</a><p>You received this email to let you know about important changes. © 2026 Google LLC</p>")],
      "none: true")

write("ignored-promo",
      [("From", "Deals <promo@news.example.com>"), ("To", "me@gmail.com"), ("Subject", "Your code is inside: SAVE2026"), DATE],
      [("text/plain", "utf-8", "Use promo code SAVE2026 to get 20% off.\n")],
      "ignore: true")

# Harder distractors: a number right before the keyword, a code inside a table
# among dates/serials/amounts, and a Big5-encoded link mail.
write("code-after-reference-en",
      [("From", "Example <security@example.com>"), ("To", "me@gmail.com"), ("Subject", "Sign-in attempt"), DATE],
      [("text/plain", "utf-8", "Hi,\nWe received a sign-in request.\nRef 482019 — your code is 731596 and expires in 10 minutes.\nIf this wasn't you, call 0800-123-456.\n")],
      'code: "731596"')

write("code-in-table-zh-tw",
      [("From", word("範例支付", "utf-8") + " <notify@pay.example.com.tw>"), ("To", "me@example.com"), ("Subject", word("交易通知", "utf-8")), DATE],
      [("text/html", "utf-8", "<p>您好，以下為本次交易資訊：</p><table><tr><td>交易時間</td><td>2026/10/19 08:00</td></tr><tr><td>交易序號</td><td>88120934</td></tr><tr><td>驗證碼</td><td><b>590274</b></td></tr><tr><td>金額</td><td>NT$ 1500</td></tr></table><p>請勿將驗證碼提供給他人。</p>")],
      'code: "590274"')

write("activate-link-zh-tw-big5",
      [("From", word("範例論壇", "big5") + " <noreply@forum.example.com.tw>"), ("To", "me@example.com"), ("Subject", word("請啟用您的帳號", "big5")), DATE],
      [("text/html", "big5", "<p>感謝註冊！請點擊下方按鈕啟用帳號：</p><p><a href=\"https://forum.example.com.tw/account/activate?key=Qm9yZWFsaXNEaWdpdGFs\">啟用帳號</a></p><p>如有問題請參閱<a href=\"https://forum.example.com.tw/help\">說明</a>。<a href=\"https://forum.example.com.tw/unsubscribe\">取消訂閱</a></p><p>2026年10月19日</p>")],
      'url: "https://forum.example.com.tw/account/activate?key=Qm9yZWFsaXNEaWdpdGFs"')

print("ok")
