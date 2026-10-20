package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
)

// The forward log: forward_log newest first, 50 a page, filtered by mailbox and result.
// Subjects are stored already redacted by the pipeline; codes and URLs never reach the
// log. Bad query values never fail the page: an unknown result is ignored, an unknown
// mailbox gives an empty list, and the page number is clamped.

const logPageSize = 50

// maxLogPage bounds the page number before it is turned into an offset.
const maxLogPage = 1_000_000

type logOption struct {
	Value, Label string
	Selected     bool
}

type logsData struct {
	Rows             []logView
	Mailboxes        []logOption
	Results          []logOption
	Mailbox, Result  string // the filters in force
	Page, Pages      int
	Total            int
	First, Last      int // 1-based positions of the rows shown
	PrevURL, NextURL string
	ClearURL         string
	Filtered         bool
}

// logsURL builds a /logs link keeping the filters.
func logsURL(mailbox, result string, page int) string {
	q := url.Values{}
	if mailbox != "" {
		q.Set("mailbox", mailbox)
	}
	if result != "" {
		q.Set("result", result)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return "/logs"
	}
	return "/logs?" + q.Encode()
}

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	d := logsData{Mailbox: truncate(strings.TrimSpace(q.Get("mailbox")), 200), Result: q.Get("result")}
	if _, ok := resultLabels[d.Result]; !ok {
		d.Result = ""
	}
	d.Page, _ = strconv.Atoi(q.Get("page"))
	d.Page = min(max(d.Page, 1), maxLogPage)

	lq := store.LogQuery{Mailbox: d.Mailbox, Result: d.Result, Limit: logPageSize, Offset: (d.Page - 1) * logPageSize}
	rows, total, err := s.o.Store.ListLog(ctx, lq)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Total = total
	d.Pages = max(1, (total+logPageSize-1)/logPageSize)
	if d.Page > d.Pages { // past the end: show the last page
		d.Page = d.Pages
		lq.Offset = (d.Page - 1) * logPageSize
		if rows, d.Total, err = s.o.Store.ListLog(ctx, lq); err != nil {
			s.serverError(w, r, err)
			return
		}
		d.Pages = max(1, (d.Total+logPageSize-1)/logPageSize)
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
	if d.Page > 1 {
		d.PrevURL = logsURL(d.Mailbox, d.Result, d.Page-1)
	}
	if d.Page < d.Pages {
		d.NextURL = logsURL(d.Mailbox, d.Result, d.Page+1)
	}
	d.Filtered = d.Mailbox != "" || d.Result != ""
	d.ClearURL = "/logs"

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
	s.render(w, r, http.StatusOK, "logs", Page{Title: "轉發紀錄", Nav: "logs", Data: d})
}
