package render

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"text/template/parse"
	"unicode/utf8"
)

// Templates come from the admin (and the data from mail), so a template must not be able to
// run for long or allocate without bound. compile checks the parse tree against an
// allowlist, so every action costs at most a constant multiple of (template size × input
// size):
//
//   - nodes: text, comments, actions, if/with (with else and else-if branches); no range,
//     template calls, define/block, break/continue; with only takes a field or dot (a
//     computed dot could double at every nesting level);
//   - no variables ($x :=, $x =, $);
//   - a pipeline has at most two commands (x | f) and a command at most maxArgs arguments,
//     each a field (including method chains such as .Date.Format), dot or literal; no
//     parenthesised sub-pipelines;
//   - functions: and or not eq ne lt le gt ge len index slice html urlquery js print printf
//     println; printf's format must be a string literal whose widths and precisions have
//     at most 3 digits, without * or [n].
//
// The output of one execution is also capped at maxOutput as a backstop.

// maxOutput caps one execution's output; Telegram's limit is 4096 UTF-16 units, so a
// message this long is cut anyway.
const maxOutput = 64 << 10

// maxArgs bounds the operands of one command (function or method name included).
const maxArgs = 10

var errOutputCap = errors.New("範本輸出超過 64 KiB")

var allowedFuncs = map[string]bool{
	"and": true, "or": true, "not": true, "eq": true, "ne": true, "lt": true, "le": true, "gt": true, "ge": true,
	"len": true, "index": true, "slice": true, "html": true, "urlquery": true, "js": true,
	"print": true, "printf": true, "println": true,
}

const allowedFuncList = "and、or、not、eq、ne、lt、le、gt、ge、len、index、slice、html、urlquery、js、print、printf、println"

// checkTree reports the first construct of t outside the allowlist.
func checkTree(t *template.Template) error {
	if len(t.Templates()) > 1 {
		return errors.New("範本不支援 define／block（不能定義或呼叫其他範本）")
	}
	if t.Tree == nil {
		return nil
	}
	return checkNode(t.Tree.Root)
}

// reject describes n as the unsupported construct msg.
func reject(msg string, n parse.Node) error {
	s := []rune(strings.TrimSpace(n.String()))
	if len(s) > 40 {
		s = append(s[:40], '…')
	}
	return fmt.Errorf("%s：%s", msg, string(s))
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
		return nil
	case *parse.TextNode, *parse.CommentNode:
		return nil
	case *parse.ActionNode:
		return checkPipe(n.Pipe)
	case *parse.IfNode:
		return checkBranch(&n.BranchNode)
	case *parse.WithNode:
		// with sets dot to its value: a computed one could grow at every nesting level
		// ({{with print . .}}…), so only a field or dot itself is allowed
		if p := n.Pipe; p == nil || len(p.Decl) > 0 || len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 1 || !isFieldOrDot(p.Cmds[0].Args[0]) {
			if p != nil && len(p.Decl) > 0 {
				return reject("範本不支援變數（$）", n.Pipe)
			}
			return reject("with 只能接欄位，例如 {{with .Subject}}", n.Pipe)
		}
		return checkBranch(&n.BranchNode)
	case *parse.RangeNode, *parse.BreakNode, *parse.ContinueNode:
		return reject("範本不支援 range（不能重複輸出）", n)
	case *parse.TemplateNode:
		return reject("範本不支援 template／block（不能呼叫其他範本）", n)
	}
	return reject("範本不支援這個語法", n)
}

func checkBranch(b *parse.BranchNode) error {
	if err := checkPipe(b.Pipe); err != nil {
		return err
	}
	if err := checkNode(b.List); err != nil {
		return err
	}
	return checkNode(b.ElseList)
}

func checkPipe(p *parse.PipeNode) error {
	if p == nil {
		return nil
	}
	if len(p.Decl) > 0 {
		return reject("範本不支援變數（$）", p)
	}
	if len(p.Cmds) > 2 {
		return reject("範本的管線最多兩段（例如 .Subject | html）", p)
	}
	for _, c := range p.Cmds {
		if err := checkCommand(c); err != nil {
			return err
		}
	}
	return nil
}

func checkCommand(c *parse.CommandNode) error {
	if len(c.Args) == 0 {
		return reject("範本不支援這個語法", c)
	}
	if len(c.Args) > maxArgs {
		return reject(fmt.Sprintf("一個動作最多 %d 個參數", maxArgs), c)
	}
	for i, a := range c.Args {
		switch a := a.(type) {
		case *parse.IdentifierNode:
			if i > 0 {
				return reject("函式只能放在動作的開頭", c)
			}
			if !allowedFuncs[a.Ident] {
				return reject("範本不支援函式 "+a.Ident+"（可用："+allowedFuncList+"）", c)
			}
			if a.Ident == "printf" {
				if err := checkPrintf(c); err != nil {
					return err
				}
			}
		case *parse.FieldNode, *parse.DotNode, *parse.StringNode, *parse.NumberNode, *parse.BoolNode:
		case *parse.ChainNode:
			switch a.Node.(type) {
			case *parse.FieldNode, *parse.DotNode:
			default:
				return reject("範本不支援括號內的運算", c)
			}
		case *parse.VariableNode:
			return reject("範本不支援變數（$）", c)
		case *parse.PipeNode:
			return reject("範本不支援括號內的運算", c)
		default:
			return reject("範本不支援這個參數", c)
		}
	}
	return nil
}

func isFieldOrDot(n parse.Node) bool {
	switch n.(type) {
	case *parse.FieldNode, *parse.DotNode:
		return true
	}
	return false
}

// checkPrintf requires a literal format with small widths and precisions.
func checkPrintf(c *parse.CommandNode) error {
	if len(c.Args) < 2 {
		return reject("printf 的第一個參數必須是格式字串", c)
	}
	f, ok := c.Args[1].(*parse.StringNode)
	if !ok {
		return reject("printf 的第一個參數必須是格式字串", c)
	}
	if err := checkFormat(f.Text); err != nil {
		return reject(err.Error(), c)
	}
	return nil
}

// checkFormat walks every fmt directive of format: % flags width .precision verb. Widths
// and precisions may have at most 3 digits; * and [n] are refused.
func checkFormat(format string) error {
	bad := errors.New("printf 格式的寬度與精確度最多 3 位數，也不能用 * 或 [n]")
	for i := 0; i < len(format); {
		if format[i] != '%' {
			i++
			continue
		}
		i++
		for i < len(format) && strings.IndexByte("+-# 0", format[i]) >= 0 {
			i++
		}
		digits := func() bool { // reads a number; false when it is too long or * or [ follows
			n := 0
			for i < len(format) && format[i] >= '0' && format[i] <= '9' {
				i++
				n++
			}
			return n <= 3 && (i >= len(format) || (format[i] != '*' && format[i] != '['))
		}
		if !digits() {
			return bad
		}
		if i < len(format) && format[i] == '.' {
			i++
			if !digits() {
				return bad
			}
		}
		if i < len(format) { // the verb, any rune (including '%')
			_, size := utf8.DecodeRuneInString(format[i:])
			i += size
		}
	}
	return nil
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
