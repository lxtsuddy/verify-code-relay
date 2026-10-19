package recognize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/mailparse"
)

type expectation struct {
	Code   string `yaml:"code"`
	URL    string `yaml:"url"`
	Ignore bool   `yaml:"ignore"`
	None   bool   `yaml:"none"`
}

func TestCorpus(t *testing.T) {
	chain, err := New([]config.Rule{{Name: "promo", From: "*@news.example.com", Ignore: true}}, config.RecognizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("../../testdata/eml/*.eml")
	if len(files) < 16 {
		t.Fatalf("corpus too small: %d files", len(files))
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".eml")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			expRaw, err := os.ReadFile(strings.TrimSuffix(f, ".eml") + ".expected.yaml")
			if err != nil {
				t.Fatal(err)
			}
			var exp expectation
			if err := yaml.Unmarshal(expRaw, &exp); err != nil {
				t.Fatal(err)
			}
			msg, err := mailparse.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			res, ignore, tr := chain.Recognize(msg)
			fail := func(format string, args ...any) {
				t.Errorf(format+"\nsubject=%q\ntext=%q\ntrace:\n  %s", append(args, msg.Subject, msg.Text, strings.Join(tr.Steps, "\n  "))...)
			}
			switch {
			case exp.Ignore:
				if !ignore {
					fail("want ignore, got %+v", res)
				}
			case exp.None:
				if res != nil || ignore {
					fail("want none, got %+v ignore=%v", res, ignore)
				}
			default:
				if res == nil || ignore || res.Code != exp.Code || res.URL != exp.URL {
					fail("want code=%q url=%q, got %+v ignore=%v", exp.Code, exp.URL, res, ignore)
				}
			}
		})
	}
}
