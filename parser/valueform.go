package parser

import (
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// offsetOf converts a token.Pos back to a byte offset within p.src.
// It is the exact inverse of posAt: posAt(off) = p.file.Pos(p.base + off),
// so offsetOf(pos) = p.file.Offset(pos) - p.base.
func (p *parser) offsetOf(pos token.Pos) int {
	return p.file.Offset(pos) - p.base
}

// parseValueCF parses one value-form `if`/`switch` contribution. at is the
// absolute offset in p.src of the leading keyword; kw is "if" or "switch".
func (p *parser) parseValueCF(at int, kw string) (*ast.ValueCF, error) {
	start := p.posAt(at)
	cf := &ast.ValueCF{}
	var end token.Pos
	switch kw {
	case "if":
		vi, e, err := p.parseValueIf(at)
		if err != nil {
			return nil, err
		}
		cf.If, end = vi, e
	case "switch":
		vs, e, err := p.parseValueSwitch(at)
		if err != nil {
			return nil, err
		}
		cf.Switch, end = vs, e
	}
	ast.SetSpan(cf, start, end)
	return cf, nil
}

// parseValueIf parses `if Cond { Arm } [else if … | else { Arm }]` starting at
// the `if` keyword (offset at). Returns the node and the position one past
// its last `}`.
func (p *parser) parseValueIf(at int) (*ast.ValueIf, token.Pos, error) {
	start := p.posAt(at)
	condStart := at + len("if")
	braceOff, ok := scanToBlockBrace(p.src, condStart, "if")
	if !ok {
		return nil, 0, p.errorf(p.posAt(condStart), "expected `{` after `if` condition")
	}
	rawCond := p.src[condStart:braceOff]
	lead := len(rawCond) - len(strings.TrimLeft(rawCond, " \t\r\n"))
	n := &ast.ValueIf{Cond: strings.TrimSpace(rawCond), CondPos: p.posAt(condStart + lead)}
	arm, afterThenPos, err := p.parseValueArm(braceOff)
	if err != nil {
		return nil, 0, err
	}
	n.Then = arm
	afterThen := p.offsetOf(afterThenPos)
	end := afterThenPos
	// optional else / else if
	rest := strings.TrimLeft(p.src[afterThen:], " \t\r\n")
	if strings.HasPrefix(rest, "else") {
		elseAt := afterThen + (len(p.src[afterThen:]) - len(rest)) + len("else")
		r2 := strings.TrimLeft(p.src[elseAt:], " \t\r\n")
		switch {
		case strings.HasPrefix(r2, "if") && (len(r2) == 2 || !isIdentByte(r2[2])):
			ifAt := elseAt + (len(p.src[elseAt:]) - len(r2))
			ei, e2, err := p.parseValueIf(ifAt)
			if err != nil {
				return nil, 0, err
			}
			n.ElseIf, end = ei, e2
		case strings.HasPrefix(r2, "{"):
			braceAt := elseAt + (len(p.src[elseAt:]) - len(r2))
			ea, e2, err := p.parseValueArm(braceAt)
			if err != nil {
				return nil, 0, err
			}
			n.Else, end = ea, e2
		default:
			return nil, 0, p.errorf(p.posAt(elseAt), "expected `{` or `if` after `else`")
		}
	}
	ast.SetSpan(n, start, end)
	return n, end, nil
}

// parseValueArm parses `{ <go-value-expr> }` whose `{` is at offset braceOff.
// Returns the arm and the position one past the matching `}`.
func (p *parser) parseValueArm(braceOff int) (*ast.ValueArm, token.Pos, error) {
	closeOff, ok := goExprEnd(p.src, braceOff)
	if !ok {
		return nil, 0, p.errorf(p.posAt(braceOff), "unterminated `{` in value-form arm")
	}
	inner := p.src[braceOff+1 : closeOff]
	if strings.TrimSpace(inner) == "" {
		return nil, 0, p.errorf(p.posAt(braceOff), "value-form arm must produce a value")
	}
	lead := len(inner) - len(strings.TrimLeft(inner, " \t\r\n"))
	seed, stages, err := parsePipe(strings.TrimSpace(inner), p.posAt(braceOff+1+lead))
	if err != nil {
		return nil, 0, p.pipeErrorf(p.posAt(braceOff), err)
	}
	arm := &ast.ValueArm{Expr: seed, Stages: stages}
	ast.SetSpan(arm, p.posAt(braceOff), p.posAt(closeOff+1))
	return arm, p.posAt(closeOff + 1), nil
}

// parseValueSwitch parses `switch [Tag] { case List: Arm … default: Arm }`
// starting at the `switch` keyword (offset at).
func (p *parser) parseValueSwitch(at int) (*ast.ValueSwitch, token.Pos, error) {
	start := p.posAt(at)
	tagStart := at + len("switch")
	braceOff, ok := scanToBlockBrace(p.src, tagStart, "switch")
	if !ok {
		return nil, 0, p.errorf(p.posAt(tagStart), "expected `{` after `switch`")
	}
	n := &ast.ValueSwitch{Tag: strings.TrimSpace(p.src[tagStart:braceOff])}
	i := braceOff + 1 // past switch-body `{`
	for {
		r := strings.TrimLeft(p.src[i:], " \t\r\n")
		if strings.HasPrefix(r, "}") {
			closeAt := i + (len(p.src[i:]) - len(r))
			end := p.posAt(closeAt + 1)
			ast.SetSpan(n, start, end)
			return n, end, nil
		}
		caseAt := i + (len(p.src[i:]) - len(r))
		cc, after, err := p.parseValueSwitchCase(caseAt)
		if err != nil {
			return nil, 0, err
		}
		n.Cases = append(n.Cases, cc)
		i = after
	}
}

// parseValueSwitchCase parses one `case List: Arm` or `default: Arm`, starting
// at the keyword (offset at). Returns the node and the offset at the next case,
// default, or switch-closing brace.
func (p *parser) parseValueSwitchCase(at int) (*ast.ValueSwitchCase, int, error) {
	start := p.posAt(at)
	cc := &ast.ValueSwitchCase{}
	r := p.src[at:]
	var valueAt int
	switch {
	case strings.HasPrefix(r, "case") && (len(r) == 4 || !isIdentByte(r[4])):
		listStart := at + len("case")
		colonOff, ok := scanToCaseColon(p.src, listStart)
		if !ok {
			return nil, 0, p.errorf(p.posAt(listStart), "expected `:` in `case`")
		}
		cc.List = strings.TrimSpace(p.src[listStart:colonOff])
		rest := strings.TrimLeft(p.src[colonOff+1:], " \t\r\n")
		valueAt = colonOff + 1 + (len(p.src[colonOff+1:]) - len(rest))
	case strings.HasPrefix(r, "default") && (len(r) == 7 || !isIdentByte(r[7])):
		cc.Default = true
		colon := strings.IndexByte(p.src[at:], ':')
		if colon < 0 {
			return nil, 0, p.errorf(p.posAt(at), "expected `:` after `default`")
		}
		rest := strings.TrimLeft(p.src[at+colon+1:], " \t\r\n")
		valueAt = at + colon + 1 + (len(p.src[at+colon+1:]) - len(rest))
	default:
		return nil, 0, p.errorf(p.posAt(at), "expected `case` or `default` in value-form `switch`")
	}
	if valueAt < len(p.src) && p.src[valueAt] == '{' {
		arm, afterPos, err := p.parseValueArm(valueAt)
		if err != nil {
			return nil, 0, err
		}
		cc.Value = arm
		ast.SetSpan(cc, start, arm.End())
		return cc, p.offsetOf(afterPos), nil
	}
	end, ok := valueSwitchArmEnd(p.src, valueAt)
	if !ok {
		return nil, 0, p.errorf(p.posAt(valueAt), "unterminated value-form switch case")
	}
	raw := p.src[valueAt:end]
	expr := strings.TrimSpace(raw)
	if expr == "" {
		return nil, 0, p.errorf(p.posAt(valueAt), "value-form switch case must produce a value")
	}
	lead := len(raw) - len(strings.TrimLeft(raw, " \t\r\n"))
	seed, stages, err := parsePipe(expr, p.posAt(valueAt+lead))
	if err != nil {
		return nil, 0, err
	}
	arm := &ast.ValueArm{Expr: seed, Stages: stages}
	trimmedEnd := end - (len(raw) - len(strings.TrimRight(raw, " \t\r\n")))
	ast.SetSpan(arm, p.posAt(valueAt+lead), p.posAt(trimmedEnd))
	cc.Value = arm
	ast.SetSpan(cc, start, p.posAt(trimmedEnd))
	return cc, end, nil
}
