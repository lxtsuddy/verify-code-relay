package recognize

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/lxtsuddy/verify-code-relay/internal/config"
	"github.com/lxtsuddy/verify-code-relay/internal/mailparse"
)

type expectation struct {
	Code   string `yaml:"code"`
	URL    string `yaml:"url"`
	Ignore bool   `yaml:"ignore"`
	None   bool   `yaml:"none"`
}

// parseExpectation decodes an .expected.yaml strictly: unknown fields are errors,
// and it must state exactly one outcome — ignore, none, or a code and/or url.
func parseExpectation(raw []byte) (expectation, error) {
	var exp expectation
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&exp); err != nil {
		return exp, err
	}
	kinds := 0
	for _, set := range []bool{exp.Ignore, exp.None, exp.Code != "" || exp.URL != ""} {
		if set {
			kinds++
		}
	}
	if kinds != 1 {
		return exp, fmt.Errorf("expectation must set exactly one of ignore, none, or code/url; got %+v", exp)
	}
	return exp, nil
}

func TestParseExpectation(t *testing.T) {
	good := map[string]expectation{
		"code: \"123456\"\n":              {Code: "123456"},
		"url: \"https://x.test/v\"\n":     {URL: "https://x.test/v"},
		"code: \"1\"\nurl: \"https://x\"": {Code: "1", URL: "https://x"},
		"ignore: true":                    {Ignore: true},
		"none: true":                      {None: true},
	}
	for in, want := range good {
		if got, err := parseExpectation([]byte(in)); err != nil || got != want {
			t.Errorf("parseExpectation(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "none: false", "cod: \"1\"", "none: true\ncode: \"1\"", "ignore: true\nnone: true", "ignore: true\nurl: \"https://x\""} {
		if _, err := parseExpectation([]byte(in)); err == nil {
			t.Errorf("parseExpectation(%q): want error", in)
		}
	}
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
	expFiles, _ := filepath.Glob("../../testdata/eml/*.expected.yaml")
	for _, f := range expFiles {
		if _, err := os.Stat(strings.TrimSuffix(f, ".expected.yaml") + ".eml"); err != nil {
			t.Errorf("orphan expectation %s: %v", filepath.Base(f), err)
		}
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
			exp, err := parseExpectation(expRaw)
			if err != nil {
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
