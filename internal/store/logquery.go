package store

import (
	"context"
	"strings"
	"time"
)

type LogRow struct {
	ID       int64
	At       time.Time
	Mailbox  string
	Folder   string
	FromAddr string
	Subject  string
	Kind     string
	Rule     string
	Result   string
}

type LogQuery struct {
	Mailbox string    // "" = all
	Result  string    // "" = all
	Kinds   []string  // any of these kinds; empty = all
	Since   time.Time // rows at or after; zero = all
	Search  string    // case-insensitive substring of the subject or sender, taken literally; "" = all
	Limit   int       // <= 0 means DefaultLogLimit
	Offset  int
}

// likeEscaper escapes LIKE's wildcards so a search is taken literally (ESCAPE '\').
var likeEscaper = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)

// DefaultLogLimit is the page size used when LogQuery.Limit is not positive.
const DefaultLogLimit = 50

// ListLog returns forward_log rows, newest first, plus the total number of rows
// matching the filters (ignoring Limit/Offset).
func (s *Store) ListLog(ctx context.Context, q LogQuery) (rows []LogRow, total int, err error) {
	var conds []string
	var args []any
	if q.Mailbox != "" {
		conds = append(conds, "mailbox = ?")
		args = append(args, q.Mailbox)
	}
	if q.Result != "" {
		conds = append(conds, "result = ?")
		args = append(args, q.Result)
	}
	if len(q.Kinds) > 0 {
		conds = append(conds, "kind IN (?"+strings.Repeat(", ?", len(q.Kinds)-1)+")")
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	if !q.Since.IsZero() {
		conds = append(conds, "at >= ?")
		args = append(args, unix(q.Since))
	}
	if q.Search != "" {
		// LIKE is case-insensitive for ASCII only; that is enough for addresses and most subjects
		pat := "%" + likeEscaper.Replace(q.Search) + "%"
		conds = append(conds, `(subject LIKE ? ESCAPE '\' OR from_addr LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM forward_log`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit, offset := q.Limit, q.Offset
	if limit <= 0 {
		limit = DefaultLogLimit
	}
	if offset < 0 {
		offset = 0
	}
	rs, err := s.db.QueryContext(ctx,
		`SELECT id, at, mailbox, folder, from_addr, subject, kind, COALESCE(rule, ''), result FROM forward_log`+where+
			` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rs.Close()
	for rs.Next() {
		var r LogRow
		var at int64
		if err := rs.Scan(&r.ID, &at, &r.Mailbox, &r.Folder, &r.FromAddr, &r.Subject, &r.Kind, &r.Rule, &r.Result); err != nil {
			return nil, 0, err
		}
		r.At = fromUnix(at)
		rows = append(rows, r)
	}
	return rows, total, rs.Err()
}

func (s *Store) CountOutbox(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox`).Scan(&n)
	return n, err
}

func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}
