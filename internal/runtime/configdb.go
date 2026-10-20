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

// LoadConfig returns the latest stored raw config (Empty() when none) and its version id (0 when none).
func LoadConfig(ctx context.Context, st *store.Store) (*config.Config, int64, error) {
	v, ok, err := st.LatestConfigVersion(ctx)
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		return config.Empty(), 0, nil
	}
	c, err := config.Decode([]byte(v.YAML))
	if err != nil {
		return nil, 0, fmt.Errorf("設定版本 %d 無法解析：%w", v.ID, err)
	}
	return c, v.ID, nil
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
