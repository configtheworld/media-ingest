package naming

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Template syntax:
//
//	{name}        variable
//	{name:02}     integer variable, zero-padded to 2 digits
//	{{ and }}     literal { and }
//	<...>         optional group: rendered only if every variable inside is non-empty
//	/             path separator (only in literal text)
//
// '<' and '>' are illegal in Windows file names, so they can never be wanted
// as literal text. Square brackets are literal: "[tmdbid-{id}]".
// The file extension and version/subtitle suffixes are appended by the
// server; a trailing ".{ext}" in a template is accepted and ignored.
type Template struct {
	src   string
	nodes []node
}

type node struct {
	lit   string // literal text
	name  string // variable name
	pad   int    // zero padding width for integers
	group []node // optional group
	isVar bool
	isGrp bool
}

// Vars holds template variable values: strings or ints. A missing key or an
// empty string counts as empty; ints (including season 0) always render.
type Vars map[string]any

// Variables available per media kind.
var (
	MovieVars = []string{"title", "original_title", "year", "id", "provider"}
	TVVars    = []string{"show", "original_show", "year", "id", "provider", "season", "episode", "episode_end", "episode_title", "air_date"}
)

// truncatable lists variables that may be shortened, in order of preference,
// when a rendered name would exceed MaxComponentBytes.
var truncatable = []string{"episode_title", "original_title", "original_show", "title", "show"}

// ParseTemplate parses and validates a template against the allowed variables.
func ParseTemplate(src string, allowed []string) (*Template, error) {
	src = strings.TrimSuffix(strings.TrimSpace(src), ".{ext}")
	if src == "" {
		return nil, errors.New("template is empty")
	}
	if len(src) > 1024 {
		return nil, errors.New("template is too long")
	}
	p := &parser{src: src, allowed: map[string]bool{}}
	for _, a := range allowed {
		p.allowed[a] = true
	}
	nodes, err := p.parse(false)
	if err != nil {
		return nil, err
	}
	if p.pos != len(src) {
		return nil, fmt.Errorf("unexpected %q at position %d", src[p.pos], p.pos)
	}
	t := &Template{src: src, nodes: nodes}
	// Validate literal segments: no "." or ".." components, no leading/trailing "/".
	probe := Vars{}
	for _, a := range allowed {
		probe[a] = "x"
	}
	for _, a := range []string{"year", "season", "episode", "episode_end"} {
		probe[a] = 1
	}
	raw := t.renderRaw(probe)
	for _, seg := range strings.Split(raw, "/") {
		if strings.TrimSpace(seg) == "" {
			return nil, errors.New("template produces an empty path segment")
		}
		if seg == "." || seg == ".." {
			return nil, errors.New("template must not contain . or .. segments")
		}
	}
	return t, nil
}

// String returns the template source.
func (t *Template) String() string { return t.src }

type parser struct {
	src     string
	pos     int
	allowed map[string]bool
}

func (p *parser) parse(inGroup bool) ([]node, error) {
	var out []node
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			out = append(out, node{lit: lit.String()})
			lit.Reset()
		}
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '{' && strings.HasPrefix(p.src[p.pos:], "{{"):
			lit.WriteByte('{')
			p.pos += 2
		case c == '}' && strings.HasPrefix(p.src[p.pos:], "}}"):
			lit.WriteByte('}')
			p.pos += 2
		case c == '{':
			end := strings.IndexByte(p.src[p.pos:], '}')
			if end < 0 {
				return nil, fmt.Errorf("unclosed { at position %d", p.pos)
			}
			spec := p.src[p.pos+1 : p.pos+end]
			name, format, _ := strings.Cut(spec, ":")
			if !p.allowed[name] {
				return nil, fmt.Errorf("unknown variable {%s}", name)
			}
			n := node{isVar: true, name: name}
			if format != "" {
				w, err := strconv.Atoi(format)
				if err != nil || w < 1 || w > 6 || format[0] != '0' {
					return nil, fmt.Errorf("invalid format %q for {%s} (use e.g. :02)", format, name)
				}
				n.pad = w
			}
			flush()
			out = append(out, n)
			p.pos += end + 1
		case c == '}':
			return nil, fmt.Errorf("unexpected } at position %d (use }} for a literal brace)", p.pos)
		case c == '<':
			p.pos++
			flush()
			g, err := p.parse(true)
			if err != nil {
				return nil, err
			}
			if p.pos >= len(p.src) || p.src[p.pos] != '>' {
				return nil, errors.New("unclosed < group")
			}
			p.pos++
			out = append(out, node{isGrp: true, group: g})
		case c == '>':
			if !inGroup {
				return nil, fmt.Errorf("unexpected > at position %d", p.pos)
			}
			flush()
			return out, nil
		case c < 0x20 || strings.ContainsRune(`\:*?"|`, rune(c)):
			return nil, fmt.Errorf("character %q is not allowed in templates", c)
		default:
			lit.WriteByte(c)
			p.pos++
		}
	}
	if inGroup {
		return nil, errors.New("unclosed < group")
	}
	flush()
	return out, nil
}

func (t *Template) renderRaw(v Vars) string {
	var b strings.Builder
	renderNodes(&b, t.nodes, v)
	return b.String()
}

func renderNodes(b *strings.Builder, nodes []node, v Vars) {
	for _, n := range nodes {
		switch {
		case n.isGrp:
			if groupFilled(n.group, v) {
				renderNodes(b, n.group, v)
			}
		case n.isVar:
			b.WriteString(formatVar(v[n.name], n.pad))
		default:
			b.WriteString(n.lit)
		}
	}
}

func groupFilled(nodes []node, v Vars) bool {
	for _, n := range nodes {
		// Nested optional groups never make the outer group empty.
		if n.isVar && formatVar(v[n.name], 0) == "" {
			return false
		}
	}
	return true
}

func formatVar(val any, pad int) string {
	switch x := val.(type) {
	case nil:
		return ""
	case int:
		if pad > 0 {
			return fmt.Sprintf("%0*d", pad, x)
		}
		return strconv.Itoa(x)
	case string:
		return sanitizeValue(x)
	default:
		return sanitizeValue(fmt.Sprint(x))
	}
}

// sanitizeValue removes characters that could change the path structure.
// Per-segment rules (trimming, reserved names) are applied after rendering.
func sanitizeValue(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(replacer.Replace(s))
}

// Render renders the template to sanitized path segments. reserveLast is the
// number of bytes to keep free in the last segment for suffixes such as
// " - Version 2.en.forced.srt". Long variables are shortened to fit.
func (t *Template) Render(v Vars, reserveLast int) ([]string, error) {
	vars := Vars{}
	for k, val := range v {
		vars[k] = val
	}
	for range 64 {
		segs, tooLong := t.renderSegments(vars, reserveLast)
		if !tooLong {
			for _, s := range segs {
				if s == "" {
					return nil, errors.New("rendered path has an empty segment")
				}
			}
			return segs, nil
		}
		if !shrinkLongest(vars) {
			break
		}
	}
	return nil, errors.New("cannot render a name short enough for the file system")
}

func (t *Template) renderSegments(v Vars, reserveLast int) ([]string, bool) {
	raw := strings.Split(t.renderRaw(v), "/")
	segs := make([]string, len(raw))
	tooLong := false
	for i, s := range raw {
		limit := MaxComponentBytes
		if i == len(raw)-1 {
			limit -= reserveLast
		}
		clean := cleanComponent(s)
		if len(clean) > limit {
			tooLong = true
		}
		segs[i] = clean
	}
	return segs, tooLong
}

func shrinkLongest(v Vars) bool {
	best, bestLen := "", 0
	for _, k := range truncatable {
		if s, ok := v[k].(string); ok && len(s) > bestLen && len(s) > 8 {
			best, bestLen = k, len(s)
		}
	}
	if best == "" {
		return false
	}
	v[best] = TruncateBytes(v[best].(string), bestLen*4/5)
	return true
}
