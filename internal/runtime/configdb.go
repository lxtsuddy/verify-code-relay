package runtime

import (
	"context"
	"fmt"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/store"
)

// Meta describes who saved a config version and why.
type Meta struct {
	Source  string
	IP      string
	Summary string
}

// InvalidConfigError is a stored config version that was read but is not a usable
// config: it does not decode or does not build. Any other error from LoadConfig or
// LoadBuilt is a failure to read the database.
type InvalidConfigError struct {
	Version int64
	Err     error
}

func (e *InvalidConfigError) Error() string { return e.Err.Error() }

func (e *InvalidConfigError) Unwrap() error { return e.Err }

// LoadConfig returns the latest stored raw config (Empty() when none) and its version id
// (0 when none). A version that does not decode is an *InvalidConfigError.
func LoadConfig(ctx context.Context, st *store.Store) (*config.Config, int64, error) {
	v, ok, err := st.LatestConfigVersion(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("讀取設定：%w", err)
	}
	if !ok {
		return config.Empty(), 0, nil
	}
	c, err := config.Decode([]byte(v.YAML))
	if err != nil {
		return nil, 0, &InvalidConfigError{Version: v.ID, Err: fmt.Errorf("設定版本 %d 無法解析：%w", v.ID, err)}
	}
	return c, v.ID, nil
}

// LoadBuilt loads and builds the latest stored config. A version that does not decode or
// does not build is an *InvalidConfigError.
func LoadBuilt(ctx context.Context, st *store.Store) (*Built, error) {
	raw, id, err := LoadConfig(ctx, st)
	if err != nil {
		return nil, err
	}
	b, err := Build(raw)
	if err != nil {
		return nil, &InvalidConfigError{Version: id, Err: err}
	}
	return b, nil
}

// SaveConfig builds raw (returning the Build error unchanged when invalid), then stores it as a new version.
func SaveConfig(ctx context.Context, st *store.Store, raw *config.Config, m Meta, now time.Time) (*Built, int64, error) {
	b, err := Build(raw)
	if err != nil {
		return nil, 0, err
	}
	data, err := config.Encode(raw)
	if err != nil {
		return nil, 0, err
	}
	id, err := st.SaveConfigVersion(ctx, store.ConfigVersion{CreatedAt: now, Source: m.Source, IP: m.IP, Summary: m.Summary, YAML: string(data)})
	if err != nil {
		return nil, 0, fmt.Errorf("儲存設定：%w", err)
	}
	return b, id, nil
}
