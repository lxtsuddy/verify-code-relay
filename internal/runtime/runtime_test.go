package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/dispatcher"
	"verify-code-relay/internal/store"
	"verify-code-relay/internal/telegram"
	"verify-code-relay/internal/tglen"
	"verify-code-relay/internal/watcher"

	"github.com/emersion/go-imap/v2/imapclient"
)

type fakeSender struct {
	mu   sync.Mutex
	reqs []telegram.SendRequest
}

func (f *fakeSender) Send(_ context.Context, r telegram.SendRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	return nil
}

func (f *fakeSender) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.Text)
	}
	return out
}

// blockedDial never connects until ctx is cancelled, so watchers stay "connecting".
func blockedDial(string, int) watcher.DialFunc {
	return func(ctx context.Context, _ *imapclient.Options) (*imapclient.Client, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

func newRT(t *testing.T) (*Runtime, *store.Store, *fakeSender) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fs := &fakeSender{}
	rt := New(Options{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Level: new(slog.LevelVar), Sender: fs, Dial: blockedDial})
	t.Cleanup(rt.Stop)
	return rt, st, fs
}

func sample() *config.Config {
	c := config.Empty()
	c.Bots["main"] = config.Bot{Token: "1:abc"}
	c.Targets["me"] = config.Target{Bot: "main", ChatID: 42}
	c.Admin = "me"
	c.Mailboxes["gmail"] = &config.Mailbox{Host: "imap.example.com", Username: "u", Auth: config.Auth{Type: "password", Password: "p"},
		Folders: []string{"INBOX", "Spam"}, Targets: []string{"me"}}
	return c
}

func TestBuildReportsAllProblems(t *testing.T) {
	c := sample()
	c.Targets["bad"] = config.Target{Bot: "ghost", ChatID: 1}
	c.Templates.Code = "{{.Nope}}"
	_, err := Build(c)
	if err == nil || !strings.Contains(err.Error(), "targets.bad.bot: ") || !strings.Contains(err.Error(), "templates.code: ") {
		t.Fatalf("err = %v", err)
	}
	if c.LogLevel != "" {
		t.Error("Build must not modify raw")
	}
}

func TestApplyStartsAndReplacesWatchers(t *testing.T) {
	rt, _, _ := newRT(t)
	b, err := Build(sample())
	if err != nil {
		t.Fatal(err)
	}
	rt.Apply(b)
	snap := rt.Snapshot()
	if len(snap.Watchers) != 2 || snap.Watchers[0].Folder != "INBOX" || snap.Watchers[0].DisplayName != "gmail" || snap.Applying {
		t.Fatalf("snapshot = %+v", snap)
	}
	c := sample()
	c.Mailboxes["gmail"].Folders = []string{"INBOX"}
	c.LogLevel = "debug"
	b2, _ := Build(c)
	rt.Apply(b2)
	if n := len(rt.Snapshot().Watchers); n != 1 {
		t.Fatalf("watchers after reapply = %d", n)
	}
	if rt.opts.Level.Level() != slog.LevelDebug {
		t.Error("log level not applied")
	}
	if len(rt.Sources()) != 1 {
		t.Error("sources not replaced")
	}
}

func TestEmptyConfigRuns(t *testing.T) {
	rt, _, _ := newRT(t)
	b, err := Build(config.Empty())
	if err != nil {
		t.Fatal(err)
	}
	rt.Apply(b)
	if len(rt.Snapshot().Watchers) != 0 {
		t.Fatal("want no watchers")
	}
	rt.Notify("hello") // no admin target: logged only, must not panic
}

func TestNotifyBypassesDedupAndAlertDedups(t *testing.T) {
	rt, _, fs := newRT(t)
	b, _ := Build(sample())
	rt.Apply(b)
	rt.Notify("login ok")
	rt.Notify("login ok")
	rt.Alert("k", "boom")
	rt.Alert("k", "boom")
	deadline := time.Now().Add(5 * time.Second)
	for len(fs.texts()) < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := strings.Join(fs.texts(), "|")
	if strings.Count(got, "login ok") != 2 || strings.Count(got, "boom") != 1 {
		t.Fatalf("sent = %q", got)
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	_, st, _ := newRT(t)
	raw, id, err := LoadConfig(context.Background(), st)
	if err != nil || id != 0 || len(raw.Mailboxes) != 0 {
		t.Fatalf("empty load: %v %d %+v", err, id, raw)
	}
	bad := sample()
	bad.Admin = "nobody"
	if _, _, err := SaveConfig(context.Background(), st, bad, Meta{Source: "web", Summary: "x"}, time.Now()); err == nil {
		t.Fatal("invalid config saved")
	}
	if _, id, _ := LoadConfig(context.Background(), st); id != 0 {
		t.Fatal("invalid config must not be stored")
	}
	b, id, err := SaveConfig(context.Background(), st, sample(), Meta{Source: "web", IP: "1.1.1.1", Summary: "新增信箱 gmail"}, time.Now())
	if err != nil || id == 0 || b.Config.Mailboxes["gmail"].Port != 993 {
		t.Fatalf("save: %v %d", err, id)
	}
	raw, id2, err := LoadConfig(context.Background(), st)
	if err != nil || id2 != id || raw.Mailboxes["gmail"].Port != 0 || raw.Bots["main"].Token != "1:abc" {
		t.Fatalf("load: %v %d %+v", err, id2, raw.Mailboxes["gmail"])
	}
}

// TestStopIsIdempotentAndFinal: Stop may run twice and before any Apply; Alert/Notify
// without a running service only log; Apply, ApplyWith and Reload after Stop start
// nothing (a save racing the shutdown must not restart the watchers), but ApplyWith still
// runs its hook.
func TestStopIsIdempotentAndFinal(t *testing.T) {
	rt, st, _ := newRT(t)
	b, _ := Build(sample())
	rt.Apply(b)
	if rt.Current() != b || rt.Snapshot().AppliedAt.IsZero() {
		t.Fatal("Current/AppliedAt not set")
	}
	rt.Stop()
	rt.Stop()
	rt.Alert("k", "no service")
	rt.Notify("no service")
	if rt.Current() != nil || len(rt.Snapshot().Watchers) != 0 || len(rt.Sources()) != 0 {
		t.Fatal("Stop must clear the service")
	}
	rt.Apply(b)
	hooked := false
	rt.ApplyWith(b, func() { hooked = true })
	if _, _, err := SaveConfig(context.Background(), st, sample(), Meta{Source: "test", Summary: "x"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Reload(); err != nil {
		t.Fatalf("Reload after Stop = %v", err)
	}
	if rt.Current() != nil || len(rt.Snapshot().Watchers) != 0 || rt.Snapshot().Applying {
		t.Fatal("Apply after Stop started a service")
	}
	if !hooked {
		t.Fatal("ApplyWith after Stop skipped its hook")
	}
}

// TestStopBeforeApply: a Stop before the first Apply is final too.
func TestStopBeforeApply(t *testing.T) {
	rt, _, _ := newRT(t)
	rt.Stop()
	b, _ := Build(sample())
	rt.Apply(b)
	if rt.Current() != nil {
		t.Fatal("Apply after Stop started a service")
	}
}

// slowPruner blocks until shutdown, then takes a while to return.
type slowPruner struct{ returned atomic.Bool }

func (p *slowPruner) PruneLog(ctx context.Context, _ time.Time) (int64, error) {
	<-ctx.Done()
	time.Sleep(100 * time.Millisecond)
	p.returned.Store(true)
	return 0, ctx.Err()
}

// TestStartWaitsForPruneLoop: wait must not return while pruneLoop may still use the
// store (the store is closed right after Stop).
func TestStartWaitsForPruneLoop(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.DiscardHandler)
	p := &slowPruner{}
	s := &service{pruner: p, log: log, disp: dispatcher.New(dispatcher.Options{Store: st, Log: log})}
	ctx, cancel := context.WithCancel(context.Background())
	wait := s.start(ctx)
	cancel()
	wait()
	if !p.returned.Load() {
		t.Fatal("wait returned before pruneLoop stopped")
	}
}

type fakePruner struct {
	n      int64
	err    error
	before chan time.Time
}

func (p *fakePruner) PruneLog(_ context.Context, before time.Time) (int64, error) {
	p.before <- before
	return p.n, p.err
}

type recLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *recLog) Info(msg string, _ ...any) { l.add("INFO " + msg) }
func (l *recLog) Warn(msg string, _ ...any) { l.add("WARN " + msg) }
func (l *recLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, s)
}

func TestPruneLoop(t *testing.T) {
	for _, tc := range []struct {
		name      string
		n         int64
		err       error
		cancelled bool // ctx already cancelled: errors are expected and not logged
		want      string
	}{
		{name: "pruned", n: 3, want: "INFO 已清理舊轉發紀錄"},
		{name: "nothing", want: ""},
		{name: "error", err: errors.New("disk I/O error"), want: "WARN 清理轉發紀錄失敗"},
		{name: "error after cancel", err: context.Canceled, cancelled: true, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakePruner{n: tc.n, err: tc.err, before: make(chan time.Time, 1)}
			log := &recLog{}
			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancelled {
				cancel()
			}
			done := make(chan struct{})
			go func() { pruneLoop(ctx, p, log); close(done) }()
			before := <-p.before
			cancel()
			<-done
			if d := time.Since(before) - 30*24*time.Hour; d < 0 || d > time.Minute {
				t.Errorf("before = %v, want about 30 days ago", before)
			}
			got := strings.Join(log.lines, "\n")
			if got != tc.want {
				t.Errorf("log = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReloadAppliesNewestVersion: a Reload that waits behind another Apply must load the
// config only once it holds the apply lock, so a version saved meanwhile is not lost.
func TestReloadAppliesNewestVersion(t *testing.T) {
	rt, st, _ := newRT(t)
	ctx := context.Background()
	if _, _, err := SaveConfig(ctx, st, sample(), Meta{Source: "web", Summary: "v1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rt.applyMu.Lock() // an Apply in progress
	done := make(chan error, 1)
	go func() { done <- rt.Reload() }()
	time.Sleep(100 * time.Millisecond)
	c := sample()
	c.Mailboxes["gmail"].Folders = []string{"INBOX", "Spam", "Archive"}
	if _, _, err := SaveConfig(ctx, st, c, Meta{Source: "web", Summary: "v2"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rt.applyMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := len(rt.Snapshot().Watchers); n != 3 {
		t.Fatalf("watchers = %d, want 3 (the newest version)", n)
	}
}

func TestNotifyTextIsCapped(t *testing.T) {
	rt, _, fs := newRT(t)
	b, _ := Build(sample())
	rt.Apply(b)
	rt.Notify(strings.Repeat("x", 5000))
	deadline := time.Now().Add(5 * time.Second)
	for len(fs.texts()) < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := fs.texts()
	if len(got) != 1 || tglen.Len(got[0]) > tglen.Max || !strings.HasPrefix(got[0], "🔔 ") {
		t.Fatalf("sent %d messages; first len = %d", len(got), len(strings.Join(got, "")))
	}
}

// ApplyWith runs its hook once, after every watcher of the old service has stopped and
// before the new service starts.
func TestApplyWithRunsHookBetweenServices(t *testing.T) {
	rt, _, _ := newRT(t)
	b, _ := Build(sample())
	rt.Apply(b)
	b2, _ := Build(sample())
	calls := 0
	rt.ApplyWith(b2, func() {
		calls++
		for _, w := range rt.Snapshot().Watchers {
			if w.Status != watcher.StatusStopped {
				t.Errorf("watcher %s/%s still %s in the hook", w.Mailbox, w.Folder, w.Status)
			}
		}
		if rt.Current() != b {
			t.Error("new service started before the hook")
		}
	})
	if calls != 1 || rt.Current() != b2 {
		t.Fatalf("calls = %d, current is new = %v", calls, rt.Current() == b2)
	}
}

// LoadBuilt tells a stored config that is not usable (InvalidConfigError) from a failure
// to read the database.
func TestLoadBuiltErrorKinds(t *testing.T) {
	ctx := context.Background()
	_, st, _ := newRT(t)
	var invalid *InvalidConfigError
	for _, y := range []string{"targets: [\n", "targets: { me: { bot: ghost, chat_id: 5 } }\n"} {
		if _, err := st.SaveConfigVersion(ctx, store.ConfigVersion{CreatedAt: time.Now(), Source: "test", Summary: "x", YAML: y}); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBuilt(ctx, st); !errors.As(err, &invalid) {
			t.Errorf("%q: err = %v, want InvalidConfigError", y, err)
		}
	}
	st.Close()
	if _, err := LoadBuilt(ctx, st); err == nil || errors.As(err, &invalid) {
		t.Errorf("closed database: err = %v, want a read error", err)
	}
}

// Each folder's watcher forwards to that folder's targets only.
func TestEachFolderForwardsToItsOwnTargets(t *testing.T) {
	c := sample()
	c.Targets["fam"] = config.Target{Bot: "main", ChatID: 7}
	m := c.Mailboxes["gmail"]
	m.Folders, m.Targets = nil, nil
	m.Routes = []config.Route{{Folder: "INBOX", Target: "me"}, {Folder: "Spam", Target: "fam"}, {Folder: "INBOX", Target: "fam"}}
	b, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range b.Config.Mailboxes["gmail"].WatchedFolders() {
		for _, tg := range mailboxFor(b.Config, "gmail", f).Targets {
			got[f] += fmt.Sprintf("%s:%d ", tg.Name, tg.ChatID)
		}
	}
	if want := map[string]string{"INBOX": "me:42 fam:7 ", "Spam": "fam:7 "}; !reflect.DeepEqual(got, want) {
		t.Fatalf("targets by folder = %v", got)
	}
}
