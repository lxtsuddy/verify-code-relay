package render

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"regexp"
	"text/template/parse"
)

// Templates come from the admin (and printf formats possibly from the mail), so they must
// not be able to run for long or allocate without bound: templates are straight-line
// (no range, no template calls, no define/block, so no loops or recursion), printf widths
// and precisions are capped, and output stops at maxOutput as a backstop.
//
// The other built-ins cannot grow output beyond their inputs: print/println/html/js/urlquery
// only format their arguments (template literals or Data fields), len/index/slice/eq/and/or/not
// return at most an argument, and call needs a function value, which Data does not have.

// maxOutput caps one execution's output; Telegram's limit is 4096 UTF-16 units, so a
// message this long is cut anyway.
const maxOutput = 64 << 10

var (
	errUnsupported = errors.New("範本不支援 range／template／define／block（範本只能依序輸出，不能重複或呼叫其他範本）")
	errOutputCap   = errors.New("範本輸出超過 64 KiB")
	// a printf directive: % then flags, argument indexes, width, precision, up to the verb
	printfVerb  = regexp.MustCompile(`%[^a-zA-Z%]*[a-zA-Z%]`)
	printfLarge = regexp.MustCompile(`\*|\d{4,}`)
)

// funcs replaces printf with a bounded one.
var funcs = template.FuncMap{"printf": safePrintf}

func safePrintf(format string, args ...any) (string, error) {
	for _, d := range printfVerb.FindAllString(format, -1) {
		if printfLarge.MatchString(d) {
			return "", fmt.Errorf("printf 的寬度與精確度最多 3 位數，也不能用 *（%q）", d)
		}
	}
	return fmt.Sprintf(format, args...), nil
}

// checkTree rejects the constructs that can repeat work: range, template calls (and with
// them define/block), break and continue.
func checkTree(t *template.Template) error {
	if len(t.Templates()) > 1 {
		return errUnsupported
	}
	if t.Tree == nil {
		return nil
	}
	return checkNode(t.Tree.Root)
}

func checkNode(n parse.Node) error {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Nodes {
			if err := checkNode(c); err != nil {
				return err
			}
		}
	case *parse.IfNode:
		return checkBranch(&n.BranchNode)
	case *parse.WithNode:
		return checkBranch(&n.BranchNode)
	case *parse.RangeNode, *parse.TemplateNode, *parse.BreakNode, *parse.ContinueNode:
		return errUnsupported
	}
	return nil
}

func checkBranch(b *parse.BranchNode) error {
	if err := checkNode(b.List); err != nil {
		return err
	}
	return checkNode(b.ElseList)
}

// capWriter keeps at most maxOutput bytes and then fails, which stops the execution.
type capWriter struct{ b bytes.Buffer }

func (w *capWriter) Write(p []byte) (int, error) {
	if room := maxOutput - w.b.Len(); len(p) > room {
		w.b.Write(p[:room])
		return room, errOutputCap
	}
	return w.b.Write(p)
}

// execute runs t into a capped buffer; hitting the cap is not an error, the text is just
// cut there (Render shortens it to Telegram's limit anyway).
func execute(t *template.Template, d Data) (string, error) {
	var w capWriter
	if err := t.Execute(&w, d); err != nil && !errors.Is(err, errOutputCap) {
		return "", err
	}
	return w.b.String(), nil
}
