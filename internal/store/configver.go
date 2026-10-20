package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// KeepConfigVersions is how many of the newest config versions are retained.
const KeepConfigVersions = 50

type ConfigVersion struct {
	ID        int64
	CreatedAt time.Time
	Source    string // web | import | restore | bootstrap
	IP        string
	Summary   string
	YAML      string
}

// SaveConfigVersion inserts v and deletes versions older than the newest
// KeepConfigVersions. The cut-off is id-based: AUTOINCREMENT never reuses ids and
// versions are only added here, so the newest N versions have consecutive ids.
func (s *Store) SaveConfigVersion(ctx context.Context, v ConfigVersion) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO config_versions (created_at, source, ip, summary, yaml) VALUES (?, ?, ?, ?, ?)`,
		unix(v.CreatedAt), v.Source, nullStr(v.IP), v.Summary, v.YAML)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM config_versions WHERE id <= ?`, id-KeepConfigVersions); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

const configVersionCols = `id, created_at, source, COALESCE(ip, ''), summary, yaml`

func scanConfigVersion(row interface{ Scan(...any) error }) (ConfigVersion, error) {
	var v ConfigVersion
	var at int64
	if err := row.Scan(&v.ID, &at, &v.Source, &v.IP, &v.Summary, &v.YAML); err != nil {
		return ConfigVersion{}, err
	}
	v.CreatedAt = fromUnix(at)
	return v, nil
}

func (s *Store) LatestConfigVersion(ctx context.Context) (ConfigVersion, bool, error) {
	v, err := scanConfigVersion(s.db.QueryRowContext(ctx,
		`SELECT `+configVersionCols+` FROM config_versions ORDER BY id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigVersion{}, false, nil
	}
	return v, err == nil, err
}

func (s *Store) GetConfigVersion(ctx context.Context, id int64) (ConfigVersion, bool, error) {
	v, err := scanConfigVersion(s.db.QueryRowContext(ctx,
		`SELECT `+configVersionCols+` FROM config_versions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigVersion{}, false, nil
	}
	return v, err == nil, err
}

// ListConfigVersions returns all kept versions, newest first, without the YAML body.
func (s *Store) ListConfigVersions(ctx context.Context) ([]ConfigVersion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, source, COALESCE(ip, ''), summary FROM config_versions ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigVersion
	for rows.Next() {
		var v ConfigVersion
		var at int64
		if err := rows.Scan(&v.ID, &at, &v.Source, &v.IP, &v.Summary); err != nil {
			return nil, err
		}
		v.CreatedAt = fromUnix(at)
		out = append(out, v)
	}
	return out, rows.Err()
}
