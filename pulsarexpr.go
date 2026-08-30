// pulsarexpr.go — a tiny scalar expression language for the pulsar visualiser.
//
// This is the "dive infinitely" surface: a scope layer's shape, and the
// per-frame / on-beat variable blocks, are all written as short formulas like
//
//	r  = 0.6 + 0.3*sin(i*6.283 + t) + a*0.4
//	th = i*6.283 + t*0.2
//
// evaluated once per point (or once per frame). It is deliberately minimal —
// floats only, no arrays, no loops, no user functions — which keeps the whole
// thing a couple hundred lines and impossible to hang. Anything malformed fails
// at compile time with an error the editor can show; evaluation itself never
// panics and never blocks.
package main

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"
)

// ---------------------------------------------------------------- AST

type exprNode interface {
	eval(env *exprEnv) float64
}

type numNode struct{ v float64 }

func (n numNode) eval(*exprEnv) float64 { return n.v }

type varNode struct{ name string }

func (n varNode) eval(env *exprEnv) float64 { return env.vars[n.name] }

type unNode struct {
	op rune
	x  exprNode
}

func (n unNode) eval(env *exprEnv) float64 {
	v := n.x.eval(env)
	if n.op == '-' {
		return -v
	}
	return v
}

type binNode struct {
	op   string
	l, r exprNode
}

func (n binNode) eval(env *exprEnv) float64 {
	l := n.l.eval(env)
	r := n.r.eval(env)
	switch n.op {
	case "+":
		return l + r
	case "-":
		return l - r
	case "*":
		return l * r
	case "/":
		if r == 0 {
			return 0
		}
		return l / r
	case "%":
		if r == 0 {
			return 0
		}
		return math.Mod(l, r)
	case "^":
		return math.Pow(l, r)
	case "<":
		return b2f(l < r)
	case ">":
		return b2f(l > r)
	case "<=":
		return b2f(l <= r)
	case ">=":
		return b2f(l >= r)
	case "==":
		return b2f(l == r)
	case "!=":
		return b2f(l != r)
	case "&&":
		return b2f(l != 0 && r != 0)
	case "||":
		return b2f(l != 0 || r != 0)
	}
	return 0
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

type callNode struct {
	fn   string
	args []exprNode
}

func (n callNode) eval(env *exprEnv) float64 {
	a := make([]float64, len(n.args))
	for i, ar := range n.args {
		a[i] = ar.eval(env)
	}
	switch n.fn {
	case "sin":
		return math.Sin(a[0])
	case "cos":
		return math.Cos(a[0])
	case "tan":
		return math.Tan(a[0])
	case "asin":
		return math.Asin(clamp1(a[0]))
	case "acos":
		return math.Acos(clamp1(a[0]))
	case "atan":
		return math.Atan(a[0])
	case "atan2":
		return math.Atan2(a[0], a[1])
	case "sqrt":
		return math.Sqrt(math.Abs(a[0]))
	case "abs":
		return math.Abs(a[0])
	case "floor":
		return math.Floor(a[0])
	case "ceil":
		return math.Ceil(a[0])
	case "fract":
		return a[0] - math.Floor(a[0])
	case "sign":
		return sign(a[0])
	case "exp":
		return math.Exp(a[0])
	case "log":
		return math.Log(math.Abs(a[0]) + 1e-9)
	case "pow":
		return math.Pow(a[0], a[1])
	case "min":
		return math.Min(a[0], a[1])
	case "max":
		return math.Max(a[0], a[1])
	case "mod":
		if a[1] == 0 {
			return 0
		}
		return math.Mod(a[0], a[1])
	case "hypot":
		return math.Hypot(a[0], a[1])
	case "clamp":
		return math.Max(a[1], math.Min(a[2], a[0]))
	case "lerp":
		return a[0] + (a[1]-a[0])*a[2]
	case "if":
		if a[0] != 0 {
			return a[1]
		}
		return a[2]
	case "rand":
		return exprRand.Float64()
	case "noise":
		return valueNoise(a[0])
	}
	return 0
}

// arity is the required argument count for each builtin; -1 means "any".
var exprFuncs = map[string]int{
	"sin": 1, "cos": 1, "tan": 1, "asin": 1, "acos": 1, "atan": 1,
	"sqrt": 1, "abs": 1, "floor": 1, "ceil": 1, "fract": 1, "sign": 1,
	"exp": 1, "log": 1, "noise": 1,
	"atan2": 2, "pow": 2, "min": 2, "max": 2, "mod": 2, "hypot": 2,
	"clamp": 3, "lerp": 3, "if": 3,
	"rand": 0,
}

var exprRand = rand.New(rand.NewSource(time.Now().UnixNano()))

func clamp1(x float64) float64 { return math.Max(-1, math.Min(1, x)) }

func sign(x float64) float64 {
	if x > 0 {
		return 1
	}
	if x < 0 {
		return -1
	}
	return 0
}

// valueNoise is a cheap 1-D smooth noise in [0,1): hash the integer lattice and
// smoothstep between neighbours. Enough for organic drift, not cryptography.
func valueNoise(x float64) float64 {
	i := math.Floor(x)
	f := x - i
	f = f * f * (3 - 2*f)
	a := hashNoise(i)
	b := hashNoise(i + 1)
	return a + (b-a)*f
}

func hashNoise(x float64) float64 {
	s := math.Sin(x*127.1) * 43758.5453
	return s - math.Floor(s)
}

// ---------------------------------------------------------------- env

type exprEnv struct {
	vars map[string]float64
}

func newExprEnv() *exprEnv { return &exprEnv{vars: map[string]float64{}} }

// ---------------------------------------------------------------- program

// a program is a sequence of statements; each is either "name = expr" (assigns
// into the env, persisting across frames) or a bare expr (its value becomes the
// program's result, which is how a single-line formula like a scope's r= works).
type stmt struct {
	name string // "" for a bare expression
	expr exprNode
}

type program struct{ stmts []stmt }

func (p *program) run(env *exprEnv) float64 {
	var last float64
	for _, s := range p.stmts {
		v := s.expr.eval(env)
		if s.name != "" {
			env.vars[s.name] = v
		} else {
			last = v
		}
	}
	return last
}

// compileProgram parses ";"- or newline-separated statements. An empty source
// compiles to a program that does nothing and returns 0.
func compileProgram(src string) (*program, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return &program{}, nil
	}
	parts := strings.FieldsFunc(src, func(r rune) bool { return r == ';' || r == '\n' })
	prog := &program{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		st, err := compileStmt(part)
		if err != nil {
			return nil, err
		}
		prog.stmts = append(prog.stmts, st)
	}
	return prog, nil
}

func compileStmt(src string) (stmt, error) {
	// an assignment is "ident = ...", but "==" / ">=" etc. are comparisons
	if i := strings.IndexByte(src, '='); i > 0 && !strings.ContainsAny(src[i-1:i], "<>=!") && (i+1 >= len(src) || src[i+1] != '=') {
		name := strings.TrimSpace(src[:i])
		if isIdent(name) {
			node, err := compileExpr(src[i+1:])
			if err != nil {
				return stmt{}, err
			}
			return stmt{name: name, expr: node}, nil
		}
	}
	node, err := compileExpr(src)
	if err != nil {
		return stmt{}, err
	}
	return stmt{expr: node}, nil
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// ---------------------------------------------------------------- lexer

type tokKind int

const (
	tkNum tokKind = iota
	tkIdent
	tkOp
	tkLParen
	tkRParen
	tkComma
	tkEOF
)

type token struct {
	kind tokKind
	text string
	num  float64
}

func lex(src string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c >= '0' && c <= '9' || (c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9'):
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			// optional exponent
			if j < len(src) && (src[j] == 'e' || src[j] == 'E') {
				j++
				if j < len(src) && (src[j] == '+' || src[j] == '-') {
					j++
				}
				for j < len(src) && src[j] >= '0' && src[j] <= '9' {
					j++
				}
			}
			var f float64
			if _, err := fmt.Sscanf(src[i:j], "%g", &f); err != nil {
				return nil, fmt.Errorf("bad number %q", src[i:j])
			}
			toks = append(toks, token{kind: tkNum, num: f})
			i = j
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			j := i
			for j < len(src) && (src[j] == '_' || src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z' || src[j] >= '0' && src[j] <= '9') {
				j++
			}
			toks = append(toks, token{kind: tkIdent, text: src[i:j]})
			i = j
		case c == '(':
			toks = append(toks, token{kind: tkLParen})
			i++
		case c == ')':
			toks = append(toks, token{kind: tkRParen})
			i++
		case c == ',':
			toks = append(toks, token{kind: tkComma})
			i++
		case strings.ContainsRune("+-*/%^", rune(c)):
			toks = append(toks, token{kind: tkOp, text: string(c)})
			i++
		case strings.ContainsRune("<>=!&|", rune(c)):
			// two-char comparison / logical operators
			if i+1 < len(src) && (src[i+1] == '=' || (c == '&' && src[i+1] == '&') || (c == '|' && src[i+1] == '|')) {
				toks = append(toks, token{kind: tkOp, text: src[i : i+2]})
				i += 2
			} else if c == '<' || c == '>' {
				toks = append(toks, token{kind: tkOp, text: string(c)})
				i++
			} else {
				return nil, fmt.Errorf("stray %q", string(c))
			}
		default:
			return nil, fmt.Errorf("unexpected %q", string(c))
		}
	}
	toks = append(toks, token{kind: tkEOF})
	return toks, nil
}

// ---------------------------------------------------------------- parser (Pratt)

type parser struct {
	toks []token
	pos  int
}

func compileExpr(src string) (exprNode, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	node, err := p.parseBin(0)
	if err != nil {
		return nil, err
	}
	if p.cur().kind != tkEOF {
		return nil, fmt.Errorf("trailing tokens in %q", src)
	}
	return node, nil
}

func (p *parser) cur() token  { return p.toks[p.pos] }
func (p *parser) next() token { t := p.toks[p.pos]; p.pos++; return t }

var binPrec = map[string]int{
	"||": 1, "&&": 2,
	"==": 3, "!=": 3, "<": 3, ">": 3, "<=": 3, ">=": 3,
	"+": 4, "-": 4,
	"*": 5, "/": 5, "%": 5,
	"^": 6,
}

func (p *parser) parseBin(minPrec int) (exprNode, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.cur()
		if t.kind != tkOp {
			return left, nil
		}
		prec, ok := binPrec[t.text]
		if !ok || prec < minPrec {
			return left, nil
		}
		p.next()
		nextMin := prec + 1
		if t.text == "^" { // right-associative
			nextMin = prec
		}
		right, err := p.parseBin(nextMin)
		if err != nil {
			return nil, err
		}
		left = binNode{op: t.text, l: left, r: right}
	}
}

func (p *parser) parseUnary() (exprNode, error) {
	if t := p.cur(); t.kind == tkOp && (t.text == "-" || t.text == "+") {
		p.next()
		// exponentiation still binds to the operand, so -x^2 is -(x^2), the
		// same as maths and Python — hence parseBin at ^'s precedence, not a
		// bare parseUnary.
		x, err := p.parseBin(binPrec["^"])
		if err != nil {
			return nil, err
		}
		if t.text == "-" {
			return unNode{op: '-', x: x}, nil
		}
		return x, nil
	}
	return p.parseAtom()
}

func (p *parser) parseAtom() (exprNode, error) {
	t := p.next()
	switch t.kind {
	case tkNum:
		return numNode{v: t.num}, nil
	case tkLParen:
		node, err := p.parseBin(0)
		if err != nil {
			return nil, err
		}
		if p.next().kind != tkRParen {
			return nil, fmt.Errorf("missing )")
		}
		return node, nil
	case tkIdent:
		if p.cur().kind == tkLParen {
			p.next()
			var args []exprNode
			if p.cur().kind != tkRParen {
				for {
					a, err := p.parseBin(0)
					if err != nil {
						return nil, err
					}
					args = append(args, a)
					if p.cur().kind == tkComma {
						p.next()
						continue
					}
					break
				}
			}
			if p.next().kind != tkRParen {
				return nil, fmt.Errorf("missing ) after %s(", t.text)
			}
			want, known := exprFuncs[t.text]
			if !known {
				return nil, fmt.Errorf("unknown function %q", t.text)
			}
			if want >= 0 && len(args) != want {
				return nil, fmt.Errorf("%s() wants %d arg(s), got %d", t.text, want, len(args))
			}
			return callNode{fn: t.text, args: args}, nil
		}
		return varNode{name: t.text}, nil
	}
	return nil, fmt.Errorf("unexpected token")
}
