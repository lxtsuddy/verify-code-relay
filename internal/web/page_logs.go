package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
)

// The forward log: forward_log newest first, 20 a page by default (20, 50 or 100),
// filtered by mailbox, result, kind, age and a search of subject and sender. Subjects
// are stored already redacted by the pipeline; codes and URLs never reach the log, so
// a search cannot find them either. Bad query values never fail the page: an unknown
// filter value is ignored, an unknown mailbox gives an empty list, and the page number
// is clamped.

const defaultLogPageSize = 20

var logPageSizes = []int{20, 50, 100}

// maxLogPage bounds the page number before it is turned into an offset.
const maxLogPage = 1_000_000

// maxLogSearch bounds the search text (in runes).
const maxLogSearch = 100

// logKindFilters maps the kind filter to the stored kinds it selects.
var logKindFilters = map[string][]string{
	"hit":     {"code", "link", "both"},
	"code":    {"code", "both"},
	"link":    {"link", "both"},
	"ignored": {"ignored"},
	"none":    {"none"},
	"skipped": {"skipped_age", "skipped_size"},
}

var logKindOrder = []string{"hit", "code", "link", "ignored", "none", "skipped"}

var logKindLabels = map[string]string{
	"hit": "有轉發（驗證碼或連結）", "code": "驗證碼", "link": "連結", "ignored": "被規則略過", "none": "未辨識", "skipped": "過舊或過大",
}

// logSinceFilters maps the age filter to how far back it reaches.
var logSinceFilters = map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

var logSinceOrder = []string{"24h", "7d", "30d"}

var logSinceLabels = map[string]string{"24h": "24 小時內", "7d": "7 天內", "30d": "30 天內"}

type logOption struct {
	Value, Label string
	Selected     bool
}

// logFilter is the log page's query: its filters, page size and page.
type logFilter struct {
	Mailbox, Result, Kind, Since, Search string
	Size, Page                           int
}

// url builds a /logs link for f; page sizes and pages at their defaults are left out.
func (f logFilter) url() string {
	q := url.Values{}
	for k, v := range map[string]string{"mailbox": f.Mailbox, "result": f.Result, "kind": f.Kind, "since": f.Since, "q": f.Search} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.Size != defaultLogPageSize {
		q.Set("size", strconv.Itoa(f.Size))
	}
	if f.Page > 1 {
		q.Set("page", strconv.Itoa(f.Page))
	}
	if len(q) == 0 {
		return "/logs"
	}
	return "/logs?" + q.Encode()
}

// pageLink is one item of the pager: a page number, or a gap (…) between them.
type pageLink struct {
	Num     int
	URL     string
	Current bool
	Gap     bool
}

type logsData struct {
	logFilter
	Rows                      []logView
	Mailboxes, Results        []logOption
	Kinds, Sinces, Sizes      []logOption
	Pages                     int
	Total                     int
	First, Last               int // 1-based positions of the rows shown
	PrevURL, NextURL          string
	PageLinks                 []pageLink
	ClearURL, AllURL, HitsURL string
	Filtered                  bool
}

// pageLinks lists the first and last pages and those next to the current one, with gaps;
// a gap that would hide a single page shows that page instead.
func pageLinks(f logFilter, pages int) []pageLink {
	var out []pageLink
	last := 0
	shown := func(n int) bool { return n == 1 || n == pages || (n >= f.Page-1 && n <= f.Page+1) }
	for n := 1; n <= pages; n++ {
		if !shown(n) && !(shown(n-1) && shown(n+1)) {
			continue
		}
		if last > 0 && n > last+1 {
			out = append(out, pageLink{Gap: true})
		}
		g := f
		g.Page = n
		out = append(out, pageLink{Num: n, URL: g.url(), Current: n == f.Page})
		last = n
	}
	return out
}

// cutRunes returns s cut to its first n runes (without an ellipsis: it is a search).
func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// readLogFilter reads the page's query, dropping values it does not know.
func readLogFilter(q url.Values) logFilter {
	f := logFilter{
		Mailbox: truncate(strings.TrimSpace(q.Get("mailbox")), 200), Result: q.Get("result"), Kind: q.Get("kind"),
		Since: q.Get("since"), Search: cutRunes(strings.TrimSpace(q.Get("q")), maxLogSearch),
	}
	if _, ok := resultLabels[f.Result]; !ok {
		f.Result = ""
	}
	if _, ok := logKindFilters[f.Kind]; !ok {
		f.Kind = ""
	}
	if _, ok := logSinceFilters[f.Since]; !ok {
		f.Since = ""
	}
	f.Size, _ = strconv.Atoi(q.Get("size"))
	if !slices.Contains(logPageSizes, f.Size) {
		f.Size = defaultLogPageSize
	}
	f.Page, _ = strconv.Atoi(q.Get("page"))
	f.Page = min(max(f.Page, 1), maxLogPage)
	return f
}

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := logsData{logFilter: readLogFilter(r.URL.Query())}
	lq := store.LogQuery{Mailbox: d.Mailbox, Result: d.Result, Kinds: logKindFilters[d.Kind], Search: d.Search,
		Limit: d.Size, Offset: (d.Page - 1) * d.Size}
	if age, ok := logSinceFilters[d.Since]; ok {
		lq.Since = s.now().Add(-age)
	}
	rows, total, err := s.o.Store.ListLog(ctx, lq)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Total = total
	d.Pages = max(1, (total+d.Size-1)/d.Size)
	if d.Page > d.Pages { // past the end: show the last page
		d.Page = d.Pages
		lq.Offset = (d.Page - 1) * d.Size
		if rows, d.Total, err = s.o.Store.ListLog(ctx, lq); err != nil {
			s.serverError(w, r, err)
			return
		}
		d.Pages = max(1, (d.Total+d.Size-1)/d.Size)
	}
	for _, row := range rows {
		d.Rows = append(d.Rows, viewLog(row))
	}
	raw, _, err := runtime.LoadConfig(ctx, s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	labelLogs(d.Rows, raw)
	if len(d.Rows) > 0 {
		d.First = lq.Offset + 1
		d.Last = lq.Offset + len(d.Rows)
	}
	at := func(page int) string {
		f := d.logFilter
		f.Page = page
		return f.url()
	}
	if d.Page > 1 {
		d.PrevURL = at(d.Page - 1)
	}
	if d.Page < d.Pages {
		d.NextURL = at(d.Page + 1)
	}
	d.PageLinks = pageLinks(d.logFilter, d.Pages)
	d.Filtered = d.Mailbox != "" || d.Result != "" || d.Kind != "" || d.Since != "" || d.Search != ""
	d.ClearURL = logFilter{Size: d.Size}.url()
	all, hits := d.logFilter, d.logFilter
	all.Kind, all.Page, hits.Kind, hits.Page = "", 1, "hit", 1
	d.AllURL, d.HitsURL = all.url(), hits.url()

	names := sortedNames(raw.Mailboxes)
	if d.Mailbox != "" && !slices.Contains(names, d.Mailbox) {
		names = append(names, d.Mailbox) // a deleted mailbox still has its history
	}
	for _, n := range names {
		label := n
		if m := raw.Mailboxes[n]; m != nil && m.DisplayName != "" && m.DisplayName != n {
			label = m.DisplayName + "（" + n + "）"
		}
		d.Mailboxes = append(d.Mailboxes, logOption{Value: n, Label: label, Selected: n == d.Mailbox})
	}
	for _, res := range resultOrder {
		d.Results = append(d.Results, logOption{Value: res, Label: resultLabels[res], Selected: res == d.Result})
	}
	for _, k := range logKindOrder {
		d.Kinds = append(d.Kinds, logOption{Value: k, Label: logKindLabels[k], Selected: k == d.Kind})
	}
	for _, v := range logSinceOrder {
		d.Sinces = append(d.Sinces, logOption{Value: v, Label: logSinceLabels[v], Selected: v == d.Since})
	}
	for _, n := range logPageSizes {
		v := strconv.Itoa(n)
		d.Sizes = append(d.Sizes, logOption{Value: v, Label: v + " 筆", Selected: n == d.Size})
	}
	s.render(w, r, http.StatusOK, "logs", Page{Title: "轉發紀錄", Nav: "logs", Data: d})
}
