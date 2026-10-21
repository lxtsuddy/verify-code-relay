# 辨識強化與 Web 信件測試設計規格

- 日期：2026-10-21
- 狀態：已實作
- 相關規格：`2026-10-19-verify-code-relay-design.md`（辨識規則 §4）、`2026-10-20-web-admin-design.md`（本規格把其中「不做」的 `.eml` 上傳測試納入）

## 1. 目標與背景

一封 Google 的「設定救援信箱」信件被判為「未辨識／未轉發」：

```
主旨：Use 123456 to set up recovery email
內文：… Use this code to finish setting up … as a recovery email:
      123456
      This code will expire in 24 hours.
```

原因是內建驗證碼辨識先要求命中關鍵字，而清單沒有涵蓋「this code」這類寫法；就算加了關鍵字，主旨中的候選也只有 39 分（門檻 40），因為在關鍵字前面的候選距離加倍計算。

本次一併參考外部建議（Gemini Argon 的擷取模組建議）全面檢查辨識邏輯，並新增 Web 上的信件測試，讓使用者能自行驗證與調整規則。

### 已確定的決策

| 項目 | 決策 |
|---|---|
| 內文來源 | HTML 轉出的文字優先，純文字版備援（第一輪沒有結果才用另一版重跑） |
| 純字母驗證碼 | 只在強烈上下文下支援（§3.4） |
| 泛用連結字（Click here／點此） | 支援，依連結前文判斷，並補反例測試 |
| 關鍵字擴充 | 只加保守詞組；不加單獨的 `code`／`代碼`（優惠碼、郵遞區號等誤判風險） |
| 片語碼（correct-horse-battery） | 不做 |
| 信心分數、多候選輸出 | 不做；轉發只需一個結果，辨識過程（Trace）已列出所有候選分數 |
| Web 信件測試 | 放在「辨識規則」頁，用頁面上**尚未儲存**的規則與關鍵字測試；不提供實際發送 |
| 既有測試語料 | 18 封既有 `.eml` 結果必須不變；若「HTML 優先」改變任何一封結果，停下來與使用者討論，不得直接改預期。（撰寫本規格時已暫時切換為 HTML 優先實測：18 封全部照舊通過） |

## 2. 信件解析（`internal/mailparse`）

### 2.1 Message 欄位

- `Text`：主要內文。有 HTML 版時為 HTML 轉出的文字；否則為純文字版。（原本是純文字優先。）
- `AltText`（新增）：另一個版本的內文；只有一種版本時為空。兩版都經過相同的 normalize。
- `Emphasis []Span`（新增）：`Text` 中強調區段的 rune 位置（僅當 `Text` 來自 HTML）。`Span` 為 `{Start, End int}`，End 不含。
- `Links`：合併 HTML `<a>` 連結，以及 `Text`、`AltText` 兩者中的裸網址，依 URL 去重（沿用 `mergeLinks`，優先保留有文字的項目）。

### 2.2 強調區段

HTML 轉文字時，以下元素內的文字記為強調：

- `<b>`、`<strong>`、`<code>`、`<kbd>`、`<samp>`、`<h1>`～`<h4>`；
- 行內 `style` 的 `font-size` ≥ 20px（或 ≥ 1.25em／rem、≥ 15pt）；
- 行內 `style` 含非零 `letter-spacing`。

記錄的是轉文字時的位元組位置，`normalizeLines` 之後必須換算到最終文字的 rune 位置（normalize 會壓縮空白、移除行首尾空白，因此需要位置對應表，或在 normalize 時一併調整區段）。區段無法準確對應時寧可丟棄，不可錯位。

記憶體：normalize 以串流方式輸出（保留的位元組直接寫入結果，不建立逐字元的中間切片）；位置對應表只在 HTML 轉文字時建立（每個位元組一個 `int32`），純文字內文不建立。部署的記憶體上限是 256 MiB，而 watcher 崩潰後會重抓同一封信，所以 5 MiB 的單行內文也必須維持在與改版前相近的用量（實測約 70 MiB）。

### 2.3 連結附加資訊

`Link` 新增：

- `Button bool`：有按鈕樣式。`<a>` 本身：`class`／`role` 含 `btn`／`button`，或有背景色，或 `style` 含 `padding`。`<a>` 的唯一子元素，或以 `<a>` 為唯一子元素的父元素（郵件常把按鈕色放在外層 `<td>`）：`class`／`role` 含 `btn`／`button`，或有背景色。「有背景色」指 `style` 的 `background`／`background-color` 或 `bgcolor` 屬性，且不是 none、transparent、inherit、initial、白色（`#fff`、`#ffffff`、`white`）。
- `Before string`：連結前文，格式為「上一行 + `\n` + 同一行」：同一行指連結開始位置之前、同一行的文字；上一行指再往前最近一個非空白行（段落之間有空行，所以不是字面上的上一行）。兩段各自空白壓縮、各取最後 120 個 rune。只讀連結前 1 KiB（從完整字元開始，不切斷多位元組字元）。用於泛用連結字判斷（§4.4）。連結之後同一行沒有任何文字時（例如段落結尾的空白連結），同一行仍是連結所在的那一行，而不是下一行。
- 連結文字為空且內含 `<img>` 時，`Text` 改用第一個非空的 `alt`。（`<img>` 本身仍不輸出到內文。）

裸網址（純文字中的 URL）沒有 `Button`；`Before` 取該網址在其所在內文中、前面同一行與上一行的文字（規則同上），以支援純文字信「Click the link below to verify:\nhttps://…」。同一 URL 同時有 HTML 連結與裸網址時，`Button`、`Before` 以 HTML 連結為準。

同一 URL 出現多次時只保留第一次（`Before` 也只算第一次；後面的只能補空白的 `Text`），而且在算 `Before` 之前就去重。每封信最多保留 500 個不同的連結（`maxLinks`，HTML 連結與兩份內文的裸網址各自與合併後都適用）：驗證信只有少數連結，上限讓一封塞滿網址的信也只花固定的工夫（改版前 5 MiB 的單行網址要 33 秒）。

## 3. 驗證碼辨識（`internal/recognize/code.go`）

### 3.1 兩輪辨識

輪次由 `Chain.Recognize` 負責，各 stage 不知道輪次：第一輪把所有 stage（自訂規則、內建）跑在原信件上；若沒有任何 stage 回傳結果或略過，且 `AltText` 非空，第二輪把所有 stage 再跑一次，用的是信件副本（`Text = AltText`、`AltText = ""`、`Emphasis = nil`，其餘欄位相同）。Trace 標示目前是哪一輪（「HTML 內文」／「純文字內文」）。同一輪內驗證碼與連結一起判斷，行為與現在相同。

### 3.2 關鍵字

在 `defaultCodeKeywords` 加入：

- 英文：`this code`、`use this code`、`enter this code`、`code below`、`following code`、`temporary code`、`pin code`、`pin is`、`one-time pin`、`temporary pin`、`security pin`
- 中文：`安全性代碼`、`驗證代碼`、`验证代码`、`動態密碼`、`动态密码`
- 日文：`セキュリティコード`

不加入單獨的 `code`、`代碼`、`代码`。也不加入 `the code`（「Use the code SAVE20 at checkout」是優惠碼）與單獨的 `pin`（「If you forgot your PIN, call 0800 123 456」、「Never share your PIN … Branch 4821」會把電話或分行號碼當成驗證碼）；只寫「PIN: 4821」的信因此辨識不到，需要時可自行加關鍵字。

### 3.3 主旨＋獨立行（B 規則）

若某個 4～8 位純數字同時：

1. 出現在主旨中，且主旨中的這串數字與內文中的那一行都不被排除（完整的 `excludeReason`：網址／email、金額、時間、年份、電話、訂單編號、§3.6 新增的卡號、郵遞區號、地址與優惠碼）；
2. 在內文中以相同的值**獨佔一行**；
3. 不是 1900～2099 的四位數（年份太常同時出現在主旨與內文標題）；
4. 主旨不含任何訂單、付款、物流或編號類的詞（英文詞遵守字母邊界）：
   - 交易（`commerceWords`，§4.4 也用）：`訂單`、`订单`、`order(s)`、`invoice(s)`、`receipt(s)`、`tracking`、`shipment(s)`、`shipping`、`payment(s)`、`purchase(s)`、`包裹`、`物流`、`出貨`、`出货`、`發票`、`发票`、`付款`、`購買`、`购买`、`繳費`；
   - 訂位、案件與帳號：`booking(s)`、`reservation(s)`、`ticket(s)`、`case`、`reference`、`ref`、`account`、`member`、`flight`、`預訂`、`訂房`、`訂位`、`預約`、`案件`、`編號`、`單號`、`帳號`、`帳戶`、`预订`、`预约`、`账号`、`账户`。

   既有的訂單編號排除只看候選前 10 個 rune，擋不住「Your order has shipped: 20261021」、「您的訂房 48213765 已確認」、「Case 4821937: …」這種寫法，所以 B 規則另外整句檢查主旨；

則即使沒有任何關鍵字命中，也視為有關鍵字：主旨中那串數字的位置被當作一個虛擬關鍵字加入，讓既有的距離評分正常運作。Trace 記為「驗證碼：主旨的 123456 也獨佔內文一行，視為關鍵字」。

若主旨有多個符合條件的數字（罕見），全部加入。

### 3.4 純字母驗證碼

新增候選格式：5～8 個大寫 ASCII 字母（`\b[A-Z]{5,8}\b`），僅在以下條件**全部**成立時才成為候選：

1. 緊接在某個驗證碼關鍵字之後，中間只允許空白、`:`、`：`、`is`、`為`、`为`、`是`；
2. 獨佔一行，或本身就是一整個強調區段（強調區段去頭尾空白後恰好等於候選）。只是落在較長的強調區段內不算：`<h1>YOUR CODE IS READY</h1>` 的 `READY` 不是驗證碼。

不符合時不列入（Trace 不必逐一列出，以免雜訊）。

### 3.5 強調加分

候選完全落在某個強調區段內時，分數 +15（數字候選只要落在區段內即可；純字母候選依 §3.4 必須是整個區段）。

### 3.6 新增排除

候選前 16 個 rune 內（小寫、空白壓縮成單一空格後比對）出現以下詞時排除（16 個 rune 才涵蓋「last 4 digits 1234」）：

- 卡號：`末四碼`、`末4碼`、`末四码`、`末4码`、`尾號`、`尾号`、`ending in`、`ends in`、`last 4`、`last4`、`last four`——Trace 原因「卡號末碼」；
- 郵遞區號：`郵遞區號`、`郵政編碼`、`邮编`、`邮政编码`、`zip`、`postal`、`postcode`——Trace 原因「郵遞區號」。

英文詞比對時遵守既有的英文字母邊界規則（`zip` 不比中 `unzip`）。比對的是空白壓縮後的文字，而不是把空白全部移除：移除空白會讓相鄰的單字黏在一起，使字母邊界檢查失效，所以英文詞組保留空格寫成 `ending in`、`last 4`。

另新增地址排除（Trace 原因「地址」）：

- 門牌：候選後方同一行 30 個 rune 內，有獨立的英文字詞（以英數字切詞；被 30 個 rune 的邊界截斷的字不算，例如「Stay」截成「st」）`st`、`street`、`ave`、`avenue`、`rd`、`road`、`blvd`、`boulevard`、`parkway`、`pkwy`（不含 suite、floor：會撞到「G Suite」這類產品名）；或候選後緊接（可隔一個空白）`號`、`号`、`路`、`街`、`樓`、`楼`。例：`1600 Amphitheatre Parkway`、`1600 Main St`、`100 號`。
- 美國郵遞區號：恰好 5 位數字（或 `12345-6789`），前面緊接美國州名縮寫（兩個大寫字母，例如 `CA 94043`）。

另新增優惠碼排除（Trace 原因「優惠碼」）：候選所在行，或其上方最近一個非空白行（內文的候選不往上看到主旨），含 `promo(s)`、`coupon(s)`、`discount(s)`、`voucher(s)`、`checkout`、`% off`、`優惠`、`折扣`、`折價`、`优惠`、`折扣码`、`折价`（英文詞前後都遵守字母邊界）。例：「Use this code SAVE20 at checkout」、「Get 20% off your next order!」下一段的「Use this code: 482913」。

## 4. 連結辨識（`internal/recognize/link.go`）

### 4.1 入口檢查

原本只在主旨＋內文中找連結關鍵字。改為主旨、目前這輪的內文、**或任一連結的 `Text`** 含關鍵字即可進入評分。會被 §4.5 排除的連結（例如「Confirm email preferences」）的文字不算。

### 4.2 關鍵字

`defaultLinkKeywords` 加入：`complete registration`、`complete your registration`、`authenticate`、`完成註冊`、`完成注册`、`認證`、`认证`、`核驗`、`核验`。

### 4.3 網址特徵詞（修正）

`urlHintWords` 改用字根，修正 `activation`／`validation` 不含 `activate`／`validate` 子字串而比不中的問題：

`verif`、`confirm`、`activat`、`validat`、`magic`、`reset`、`login`、`signin`、`sign-in`、`token=`、`code=`、`ticket=`，以及 `auth` 只在作為路徑段或參數名時算數（`/auth/`、`/auth?`、路徑以 `/auth` 結尾、`auth=`、`?auth`、`&auth`），避免 `/author/…` 比中。

分數不變（任一命中 +30）。

### 4.4 評分調整

- `Button` 為真**且**連結文字含連結關鍵字（含 §4.4 泛用字經前文判定成立者）：+10。單有按鈕樣式不加分——否則同網域的「Read more」按鈕加上網址特徵詞就可能剛好達門檻。（撰寫本規格時量過：既有反例語料的連結最高 10 分。）
- **泛用連結字**：連結文字為空（裸網址、無 alt 的圖片），或（去頭尾空白與標點、小寫後）完全等於下列之一時：
  `click here`、`here`、`click`、`this link`、`link`、`點此`、`點這裡`、`點擊這裡`、`点击这里`、`點擊此處`、`点击此处`、`按此`、`這裡`、`这里`、`此處`、`此处`、`ここ`、`こちら`；
  改看 `Before`，符合以下任一條件即給「文字含關鍵字」的 +50：
  1. 同一行含連結關鍵字（「To verify your email, click here」）；
  2. 上一行含連結關鍵字，**而且**上一行有指向下文的寫法——含 `below`、`following`、`link`、`button`、`click`、`下方`、`以下`、`下列`、`下面`、`連結`、`链接`、`按鈕`、`按钮`、`點擊`、`点击`，或以 `:`／`：` 結尾（「Please verify your email by clicking the link below.」）。
  只看「上一行含關鍵字」會把電子報「上一段提到 confirm，下一段 Click here to read more」誤判，所以要求指向寫法。
  這條路線的連結關鍵字前後都遵守英文字母邊界（`confirm` 不比中 `confirmation`；指向寫法仍是子字串，`clicking`、`links` 也算）。`Before` 或主旨含 §3.3 的交易詞（`commerceWords`：order、receipt、invoice、shipping、payment、訂單、付款 …；「in order to」不算）時不走這條路線：「Order confirmation:」、「View your order confirmation at the link below:」、「請確認以下訂單資訊：」下面的網址是訂單頁，不是驗證連結。
- 其餘不變。

### 4.5 排除

`linkExcludeWords`（比對連結文字與完整網址）加入：`opt-out`、`optout`、`view in browser`、`view this email`、`在瀏覽器中檢視`、`在浏览器中查看`、`聯絡我們`、`联系我们`、`幫助`、`帮助`。`contact` 只比對連結文字與網址路徑（不含查詢字串），因為驗證網址的參數常帶收件地址，例如 `?email=contact%40company.com`。

圖片副檔名排除擴充為靜態資源：`.png .jpg .jpeg .gif .webp .svg .bmp .ico .css .js .pdf`（Trace 原因「靜態資源」）。

## 5. Web 信件測試

### 5.1 介面

「辨識規則」頁（`/rules`）下方新增「用信件測試」區塊：

- 檔案拖放區／選擇檔案（`.eml`），或切換到「貼上原始碼」文字框。說明文字：Gmail「顯示原始郵件」→「下載原始郵件」或「複製到剪貼簿」（不要全選整頁，頁首的摘要表格會破壞格式）；Outlook「檢視訊息原始碼」。
- 「預覽訊息使用的信箱」下拉選單：預設「（不指定，用全域範本）」，顯示名稱為「（測試）」，與 CLI 相同；選擇信箱時用該信箱的範本與顯示名稱。
- 「測試」按鈕。區塊上方註明「使用本頁目前填寫的規則與關鍵字（尚未儲存也會套用）」。
- 結果區：結論列（轉發驗證碼／轉發連結／被規則略過／不轉發）、驗證碼／連結／服務／規則、Telegram 訊息預覽（沿用範本預覽的元件與按鈕呈現）、可展開的辨識過程（Trace 逐行）、連結清單（URL 與文字）、信件摘要（寄件者、主旨、警告、內文前 500 字）。
- 介面以 frontend-design skill 設計；完成後以 headless chromium 截圖確認。

### 5.2 API

`POST /api/rules/test`，`multipart/form-data`：

- `eml`：檔案，或 `eml_text`：貼上的原始碼（擇一，檔案優先）；
- `mailbox`：選填；
- 規則與關鍵字：與 `POST /rules` 相同的欄位（`rule_*` 平行陣列、`extra_code_keywords`、`extra_link_keywords`），以相同的讀取與驗證程式處理（`readRules`、`readKeywords`、規則驗證）。

回應 JSON：

```json
{"ok":true,
 "message":{"from":"…","subject":"…","warnings":["…"],"text":"前 500 字","links":[{"url":"…","text":"…"}]},
 "trace":["…"],
 "result":{"kind":"code|link|both|ignored|none","code":"…","url":"…","service":"…","rule":"…"},
 "preview":{"html":"…","buttons":[…],"length":n,"max":4096}}
```

或 `{"ok":false,"error":"…","errors":{"rules[0].code_regex":"…"}}`（規則欄位錯誤時附欄位錯誤，前端標在對應欄位）。`preview` 僅在有結果且非略過時存在。

### 5.3 限制與安全

- 需登入（`authed`），沿用 Origin／Sec-Fetch-Site 檢查。
- 這個路由的請求本文上限放寬為 6 MiB（全域 1 MiB）；信件原始碼超過 5 MiB（`pipeline.MaxMessageSize`）時回「信件超過 5 MiB，實際運作時也會略過不辨識」。頁面在送出前先檢查檔案大小（貼上的原始碼以 UTF-8、CRLF 換行計算），超過時直接顯示同一句話，不上傳。
- 信件只在記憶體中處理：不寫資料庫、不寫檔、不寫日誌。Go 的 `ParseMultipartForm(maxMemory)` 只在檔案部分合計超過 `maxMemory` 時才寫暫存檔；這個路由的請求本文上限（6 MiB）遠小於使用的 `maxMemory`（32 MiB），所以所有部分都留在記憶體。實作必須維持「本文上限 < maxMemory」，並以註解說明這個不變量（另以編譯期檢查保證；沒有 `X-CSRF-Token` 標頭時，CSRF 檢查改讀表單欄位 `PostFormValue`，用的是 Go 預設的 32 MiB，同樣大於本文上限）；Trace 含驗證碼與連結，**不得**出現在任何 log。錯誤日誌只記錯誤類型，不記內容。
- 不提供實際發送（CLI `relay test --send` 保留）。
- 自訂規則的正規表示式由使用者輸入：沿用 Go `regexp`（RE2，線性時間），無回溯爆炸風險。

### 5.4 共用

CLI `relay test` 與 Web 共用「解析 → 辨識 → 渲染」的邏輯，抽成一個函式（例如 `internal/pipeline` 或新檔），兩邊只負責輸入與輸出格式，避免兩份實作漂移。

## 6. 測試

### 6.1 語料（`testdata/eml/gen.py` 產生）

應辨識：

| 名稱 | 預期 |
|---|---|
| `google-recovery-en` | 本次的 Google 救援信箱信 → `123456` |
| `verify-button-zh-tw`／`verify-button-zh-cn`／`verify-button-en` | 只有「驗證」「验证」「Verify」按鈕，連結為追蹤網址 → 該連結 |
| `image-button-en` | 按鈕是 `<img alt="Verify email">` → 該連結 |
| `click-here-en` | 「Please verify your email by clicking the link below」＋「Click here」 → 該連結 |
| `html-only-useful-en` | 純文字版只有「請使用 HTML 郵件軟體」，HTML 版有粗體驗證碼 → 該碼 |
| `alpha-code-en` | 「Your code is:」下一行粗體 `QWERTY` → `QWERTY` |
| `activation-url-en` | 連結文字為「Continue」、網址含 `/activation?` → 該連結（驗證 §4.3 修正） |

反例（`none: true`）：

| 名稱 | 內容 |
|---|---|
| `promo-code-en` | 「Use promo code SAVE20」「Your discount code: 8888」 |
| `order-subject-zh` | 主旨「您的訂單已出貨：20261021」；內文只有那串數字（`20261021` 獨佔一行），沒有提到訂單 |
| `card-last4-zh` | 信用卡消費通知：「您末四碼 1234 的信用卡…」，同段有「請勿將驗證碼告訴他人」 |
| `newsletter-click-here-en` | 電子報「Click here to read more」 |
| `author-link-en` | 同網域「Read more」按鈕連到 `https://blog.example.com/author/jane`，內文有「verify your own profile」（讓連結入口檢查通過） |
| `footer-zip-en` | 安全提醒信「We will never ask for your verification code」，頁尾 `Example Inc., 1600 Main Street, Mountain View, CA 94043`，沒有驗證連結 |

> 註：`order-subject-zh` 確認主旨提到訂單、內文又獨佔一行出現同一串數字時，不會被當成驗證碼。它有兩道防線：主旨中的數字前 10 個 rune 內就有「訂單」（§3.6 前既有的訂單編號排除），主旨也含訂單詞（§3.3 第 4 點）；只拿掉其中一道仍會通過，所以 §3.3 第 4 點另由單元測試（訂單詞不在數字前 10 個 rune 內的主旨，以及訂房、案件等編號）個別驗證。`author-link-en` 驗證 §4.3 的 `auth` 錨定（`/author/…` 不算網址特徵詞）與 §4.4 的按鈕條件（單有按鈕樣式不加分）。

### 6.2 單元測試

- `mailparse`：HTML 優先與 `AltText`；強調區段的 rune 位置（含 normalize 後的位移、CJK）；`Button`、`Before`、圖片 alt。
- `recognize`：B 規則、純字母條件、強調加分、新排除、兩輪辨識、入口檢查、網址字根、泛用連結字。
- `web`：`/api/rules/test` 的成功、未登入、跨來源、超過大小、規則欄位錯誤、未儲存規則生效、無信箱與指定信箱的預覽。

### 6.3 既有測試

既有 18 封語料與所有既有測試必須維持通過且預期不變。

## 7. 文件

- README：辨識規則章節更新（HTML 優先、B 規則、純字母碼、按鈕連結）；新增「用信件測試」說明。
- `docs/TODO.md`：移除已完成的 `.eml` 上傳測試延後項目（若有），記錄本次不做的項目（片語碼、信心分數）。
