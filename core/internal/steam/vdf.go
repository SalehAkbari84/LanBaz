package steam

import (
	"fmt"
	"strings"
)

// A minimal reader and writer for Valve's text KeyValues ("VDF") format, the
// one Steam keeps its library list, app manifests and login users in:
//
//	"users"
//	{
//		"76561198000000000"
//		{
//			"AccountName"		"name"
//		}
//	}
//
// Strings are kept exactly as written (escapes included), so a file read and
// written back differs only in whitespace, and every key Steam wrote - known
// to us or not - survives in its original order.

// Node is one key: either a value (Obj false) or a block of children.
type Node struct {
	Key      string
	Value    string
	Obj      bool
	Children []*Node
}

// Child returns the first child with this key (case-insensitive, as Steam
// reads them).
func (n *Node) Child(key string) *Node {
	for _, c := range n.Children {
		if strings.EqualFold(c.Key, key) {
			return c
		}
	}
	return nil
}

// Get returns a child value, or "".
func (n *Node) Get(key string) string {
	if c := n.Child(key); c != nil && !c.Obj {
		return c.Value
	}
	return ""
}

// Set changes a child value, adding it at the end when missing.
func (n *Node) Set(key, value string) {
	if c := n.Child(key); c != nil && !c.Obj {
		c.Value = value
		return
	}
	n.Children = append(n.Children, &Node{Key: key, Value: value})
}

// Parse reads a VDF document into a root block holding its top-level keys.
func Parse(text string) (*Node, error) {
	p := &parser{s: text}
	root := &Node{Obj: true}
	if err := p.block(root, true); err != nil {
		return nil, err
	}
	return root, nil
}

// bom is the UTF-8 byte order mark some editors put in front of a file.
const bom = "\xef\xbb\xbf"

type parser struct {
	s   string
	pos int
}

func (p *parser) skip() {
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.pos++
		case c == '/' && p.pos+1 < len(p.s) && p.s[p.pos+1] == '/':
			for p.pos < len(p.s) && p.s[p.pos] != '\n' {
				p.pos++
			}
		case strings.HasPrefix(p.s[p.pos:], bom):
			p.pos += len(bom)
		default:
			return
		}
	}
}

// token reads a quoted or bare string, or one of the braces.
func (p *parser) token() (tok string, brace byte, err error) {
	p.skip()
	if p.pos >= len(p.s) {
		return "", 0, nil
	}
	switch c := p.s[p.pos]; c {
	case '{', '}':
		p.pos++
		return "", c, nil
	case '"':
		start := p.pos + 1
		i := start
		for i < len(p.s) && p.s[i] != '"' {
			if p.s[i] == '\\' && i+1 < len(p.s) {
				i++
			}
			i++
		}
		if i >= len(p.s) {
			return "", 0, fmt.Errorf("vdf: unterminated string at %d", p.pos)
		}
		p.pos = i + 1
		return p.s[start:i], 0, nil
	default:
		start := p.pos
		for p.pos < len(p.s) && !strings.ContainsRune(" \t\r\n{}\"", rune(p.s[p.pos])) {
			p.pos++
		}
		return p.s[start:p.pos], 0, nil
	}
}

func (p *parser) block(into *Node, top bool) error {
	for {
		key, brace, err := p.token()
		if err != nil {
			return err
		}
		switch {
		case brace == '}':
			if top {
				return fmt.Errorf("vdf: unexpected } at %d", p.pos)
			}
			return nil
		case brace == '{':
			return fmt.Errorf("vdf: block without a key at %d", p.pos)
		case key == "" && p.pos >= len(p.s):
			if !top {
				return fmt.Errorf("vdf: missing }")
			}
			return nil
		}
		val, vb, err := p.token()
		if err != nil {
			return err
		}
		switch vb {
		case '{':
			child := &Node{Key: key, Obj: true}
			if err := p.block(child, false); err != nil {
				return err
			}
			into.Children = append(into.Children, child)
		case '}':
			return fmt.Errorf("vdf: key %q without a value", key)
		default:
			into.Children = append(into.Children, &Node{Key: key, Value: val})
		}
	}
}

// Encode writes the document back in Steam's own layout.
func Encode(root *Node) string {
	var b strings.Builder
	for _, c := range root.Children {
		write(&b, c, 0)
	}
	return b.String()
}

func write(b *strings.Builder, n *Node, depth int) {
	indent := strings.Repeat("\t", depth)
	if n.Obj {
		fmt.Fprintf(b, "%s\"%s\"\n%s{\n", indent, n.Key, indent)
		for _, c := range n.Children {
			write(b, c, depth+1)
		}
		fmt.Fprintf(b, "%s}\n", indent)
		return
	}
	fmt.Fprintf(b, "%s\"%s\"\t\t\"%s\"\n", indent, n.Key, n.Value)
}

// Unescape turns a raw VDF string into display text.
func Unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n", `\t`, "\t")
	return r.Replace(s)
}
