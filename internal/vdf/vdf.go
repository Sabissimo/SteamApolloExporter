// Package vdf parses Valve's text KeyValues format (libraryfolders.vdf, *.acf).
package vdf

import (
	"fmt"
	"strings"
)

// Node is a VDF object: keys map to either string or Node values.
type Node map[string]any

// String returns the string value at key, or "".
func (n Node) String(key string) string {
	s, _ := n[key].(string)
	return s
}

// Child returns the nested object at key, or nil.
func (n Node) Child(key string) Node {
	c, _ := n[key].(Node)
	return c
}

// Parse decodes a text VDF document into its root object.
func Parse(data string) (Node, error) {
	p := &parser{src: strings.TrimPrefix(data, "\xef\xbb\xbf")} // UTF-8 BOM
	root, err := p.object(false)
	if err != nil {
		return nil, err
	}
	return root, nil
}

type parser struct {
	src string
	pos int
}

func (p *parser) skip() {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.pos++
		case strings.HasPrefix(p.src[p.pos:], "//"):
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

// token returns the next string token, or "{" / "}" as structural tokens.
func (p *parser) token() (tok string, quoted bool, ok bool) {
	p.skip()
	if p.pos >= len(p.src) {
		return "", false, false
	}
	c := p.src[p.pos]
	if c == '{' || c == '}' {
		p.pos++
		return string(c), false, true
	}
	if c == '"' {
		p.pos++
		var b strings.Builder
		for p.pos < len(p.src) {
			c = p.src[p.pos]
			if c == '\\' && p.pos+1 < len(p.src) {
				switch p.src[p.pos+1] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(p.src[p.pos+1])
				}
				p.pos += 2
				continue
			}
			p.pos++
			if c == '"' {
				return b.String(), true, true
			}
			b.WriteByte(c)
		}
		return b.String(), true, true
	}
	start := p.pos
	for p.pos < len(p.src) && !strings.ContainsRune(" \t\r\n{}\"", rune(p.src[p.pos])) {
		p.pos++
	}
	return p.src[start:p.pos], false, true
}

func (p *parser) object(nested bool) (Node, error) {
	n := Node{}
	for {
		key, quoted, ok := p.token()
		if !ok {
			if nested {
				return nil, fmt.Errorf("vdf: unexpected end of input")
			}
			return n, nil
		}
		if !quoted && key == "}" {
			if !nested {
				return nil, fmt.Errorf("vdf: unexpected '}' at %d", p.pos)
			}
			return n, nil
		}
		val, vquoted, ok := p.token()
		if !ok {
			return nil, fmt.Errorf("vdf: missing value for %q", key)
		}
		if !vquoted && val == "{" {
			child, err := p.object(true)
			if err != nil {
				return nil, err
			}
			n[key] = child
		} else {
			n[key] = val
		}
	}
}
