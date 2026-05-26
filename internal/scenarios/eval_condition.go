package scenarios

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// TokenType 定義 token 的類型
type TokenType int

const (
	TokenEOF TokenType = iota
	TokenAND
	TokenOR
	TokenNOT
	TokenEQ
	TokenNE
	TokenLT
	TokenLE
	TokenGT
	TokenGE
	TokenLParen
	TokenRParen
	TokenIdentifier
	TokenString
	TokenNumber
	TokenError
)

// Token 代表一個詞彙單位
type Token struct {
	Type  TokenType
	Value string
}

// Lexer 將字符串分解為 token
type Lexer struct {
	input string
	pos   int
	ch    byte
}

// NewLexer 建立新的 Lexer
func NewLexer(input string) *Lexer {
	l := &Lexer{input: input, pos: 0}
	if l.pos < len(l.input) {
		l.ch = l.input[l.pos]
	}
	return l
}

// NextToken 返回下一個 token
func (l *Lexer) NextToken() Token {
	l.skipWhitespace()
	if l.pos >= len(l.input) {
		return Token{Type: TokenEOF}
	}

	ch := l.ch

	if isLetter(ch) {
		return l.readKeywordOrIdentifier()
	}
	if ch == '"' {
		return l.readString()
	}
	if unicode.IsDigit(rune(ch)) {
		return l.readNumber()
	}

	switch ch {
	case '(':
		l.advance()
		return Token{Type: TokenLParen, Value: "("}
	case ')':
		l.advance()
		return Token{Type: TokenRParen, Value: ")"}
	case '=':
		l.advance()
		if l.ch == '=' {
			l.advance()
			return Token{Type: TokenEQ, Value: "=="}
		}
		return Token{Type: TokenError, Value: "unexpected ="}
	case '!':
		l.advance()
		if l.ch == '=' {
			l.advance()
			return Token{Type: TokenNE, Value: "!="}
		}
		return Token{Type: TokenNOT, Value: "!"}
	case '<':
		l.advance()
		if l.ch == '=' {
			l.advance()
			return Token{Type: TokenLE, Value: "<="}
		}
		return Token{Type: TokenLT, Value: "<"}
	case '>':
		l.advance()
		if l.ch == '=' {
			l.advance()
			return Token{Type: TokenGE, Value: ">="}
		}
		return Token{Type: TokenGT, Value: ">"}
	default:
		l.advance()
		return Token{Type: TokenError, Value: fmt.Sprintf("unexpected character: %c", ch)}
	}
}

func (l *Lexer) skipWhitespace() {
	for l.pos < len(l.input) && unicode.IsSpace(rune(l.ch)) {
		l.advance()
	}
}

func (l *Lexer) advance() {
	l.pos++
	if l.pos < len(l.input) {
		l.ch = l.input[l.pos]
	}
}

func (l *Lexer) readKeywordOrIdentifier() Token {
	start := l.pos
	for l.pos < len(l.input) && (isLetter(l.ch) || unicode.IsDigit(rune(l.ch)) || l.ch == '_' || l.ch == '.') {
		l.advance()
	}
	value := l.input[start:l.pos]

	switch strings.ToUpper(value) {
	case "AND":
		return Token{Type: TokenAND, Value: value}
	case "OR":
		return Token{Type: TokenOR, Value: value}
	case "NOT":
		return Token{Type: TokenNOT, Value: value}
	default:
		return Token{Type: TokenIdentifier, Value: value}
	}
}

func (l *Lexer) readString() Token {
	l.advance() // skip opening quote
	start := l.pos
	for l.pos < len(l.input) && l.ch != '"' {
		l.advance()
	}
	if l.pos >= len(l.input) {
		return Token{Type: TokenError, Value: "unterminated string"}
	}
	value := l.input[start:l.pos]
	l.advance() // skip closing quote
	return Token{Type: TokenString, Value: value}
}

func (l *Lexer) readNumber() Token {
	start := l.pos
	for l.pos < len(l.input) && unicode.IsDigit(rune(l.ch)) {
		l.advance()
	}
	if l.pos < len(l.input) && l.ch == '.' {
		l.advance()
		for l.pos < len(l.input) && unicode.IsDigit(rune(l.ch)) {
			l.advance()
		}
	}
	value := l.input[start:l.pos]
	return Token{Type: TokenNumber, Value: value}
}

func isLetter(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

// ExprNode 代表 AST 中的一個節點
type ExprNode interface {
	Evaluate(ctx *VarContext) (bool, error)
}

// ComparisonNode 代表比較表達式
type ComparisonNode struct {
	Left     string    // 變數名稱或 identifier
	Operator TokenType // EQ, NE, LT, LE, GT, GE
	Right    string    // 值（需要渲染）
}

func (n *ComparisonNode) Evaluate(ctx *VarContext) (bool, error) {
	// 先渲染 left 和 right
	// 支援簡化的變數語法：如果不是已渲染的形式，嘗試轉為 {{vars.xxx}}
	leftVal := renderToken(n.Left, ctx)
	leftRendered, err := RenderString(leftVal, ctx)
	if err != nil {
		// 如果渲染失敗，就使用原始值（可能是字面常數）
		leftRendered = n.Left
	}

	rightVal := renderToken(n.Right, ctx)
	rightRendered, err := RenderString(rightVal, ctx)
	if err != nil {
		// 如果渲染失敗，就使用原始值（可能是字面常數）
		rightRendered = n.Right
	}

	switch n.Operator {
	case TokenEQ:
		return leftRendered == rightRendered, nil
	case TokenNE:
		return leftRendered != rightRendered, nil
	case TokenLT:
		return compareNumeric(leftRendered, rightRendered) < 0, nil
	case TokenLE:
		return compareNumeric(leftRendered, rightRendered) <= 0, nil
	case TokenGT:
		return compareNumeric(leftRendered, rightRendered) > 0, nil
	case TokenGE:
		return compareNumeric(leftRendered, rightRendered) >= 0, nil
	default:
		return false, fmt.Errorf("unknown comparison operator: %v", n.Operator)
	}
}

// BinaryOpNode 代表二元操作（AND, OR）
type BinaryOpNode struct {
	Op    TokenType // AND or OR
	Left  ExprNode
	Right ExprNode
}

func (n *BinaryOpNode) Evaluate(ctx *VarContext) (bool, error) {
	left, err := n.Left.Evaluate(ctx)
	if err != nil {
		return false, err
	}

	right, err := n.Right.Evaluate(ctx)
	if err != nil {
		return false, err
	}

	switch n.Op {
	case TokenAND:
		return left && right, nil
	case TokenOR:
		return left || right, nil
	default:
		return false, fmt.Errorf("unknown binary operator: %v", n.Op)
	}
}

// UnaryOpNode 代表單元操作（NOT）
type UnaryOpNode struct {
	Op   TokenType // NOT
	Expr ExprNode
}

func (n *UnaryOpNode) Evaluate(ctx *VarContext) (bool, error) {
	val, err := n.Expr.Evaluate(ctx)
	if err != nil {
		return false, err
	}
	return !val, nil
}

// Parser 將 token 流解析為 AST
type Parser struct {
	lexer *Lexer
	cur   Token
}

// NewParser 建立新的 Parser
func NewParser(input string) *Parser {
	lexer := NewLexer(input)
	p := &Parser{lexer: lexer}
	p.cur = p.lexer.NextToken()
	return p
}

// Parse 解析表達式
func (p *Parser) Parse() (ExprNode, error) {
	expr, err := p.parseOR()
	if err != nil {
		return nil, err
	}
	// 確保所有 token 都被消費
	if p.cur.Type != TokenEOF {
		return nil, fmt.Errorf("unexpected token after expression: %v (%s)", p.cur.Type, p.cur.Value)
	}
	return expr, nil
}

// parseOR 處理 OR 運算子（最低優先級）
func (p *Parser) parseOR() (ExprNode, error) {
	left, err := p.parseAND()
	if err != nil {
		return nil, err
	}

	for p.cur.Type == TokenOR {
		p.advance()
		right, err := p.parseAND()
		if err != nil {
			return nil, err
		}
		left = &BinaryOpNode{Op: TokenOR, Left: left, Right: right}
	}

	return left, nil
}

// parseAND 處理 AND 運算子
func (p *Parser) parseAND() (ExprNode, error) {
	left, err := p.parseNOT()
	if err != nil {
		return nil, err
	}

	for p.cur.Type == TokenAND {
		p.advance()
		right, err := p.parseNOT()
		if err != nil {
			return nil, err
		}
		left = &BinaryOpNode{Op: TokenAND, Left: left, Right: right}
	}

	return left, nil
}

// parseNOT 處理 NOT 運算子
func (p *Parser) parseNOT() (ExprNode, error) {
	if p.cur.Type == TokenNOT {
		p.advance()
		expr, err := p.parseNOT()
		if err != nil {
			return nil, err
		}
		return &UnaryOpNode{Op: TokenNOT, Expr: expr}, nil
	}

	return p.parseComparison()
}

// parseComparison 處理比較表達式或括號表達式
func (p *Parser) parseComparison() (ExprNode, error) {
	if p.cur.Type == TokenLParen {
		p.advance()
		expr, err := p.parseOR()
		if err != nil {
			return nil, err
		}
		if p.cur.Type != TokenRParen {
			return nil, fmt.Errorf("expected ), got %v", p.cur.Type)
		}
		p.advance()
		return expr, nil
	}

	if p.cur.Type == TokenEOF {
		return nil, fmt.Errorf("unexpected EOF")
	}

	if p.cur.Type == TokenError {
		return nil, fmt.Errorf("lexer error: %s", p.cur.Value)
	}

	if p.cur.Type != TokenIdentifier && p.cur.Type != TokenString && p.cur.Type != TokenNumber {
		return nil, fmt.Errorf("expected identifier or value, got %v (%s)", p.cur.Type, p.cur.Value)
	}

	left := p.cur.Value
	p.advance()

	if p.cur.Type != TokenEQ && p.cur.Type != TokenNE && p.cur.Type != TokenLT && p.cur.Type != TokenLE && p.cur.Type != TokenGT && p.cur.Type != TokenGE {
		return nil, fmt.Errorf("expected comparison operator, got %v (%s)", p.cur.Type, p.cur.Value)
	}

	op := p.cur.Type
	p.advance()

	if p.cur.Type == TokenEOF {
		return nil, fmt.Errorf("unexpected EOF after operator")
	}

	if p.cur.Type != TokenIdentifier && p.cur.Type != TokenString && p.cur.Type != TokenNumber {
		return nil, fmt.Errorf("expected value, got %v (%s)", p.cur.Type, p.cur.Value)
	}

	right := p.cur.Value
	p.advance()

	return &ComparisonNode{Left: left, Operator: op, Right: right}, nil
}

func (p *Parser) advance() {
	p.cur = p.lexer.NextToken()
}

func compareNumeric(a, b string) int {
	aVal := parseNumber(a)
	bVal := parseNumber(b)
	if aVal < bVal {
		return -1
	} else if aVal > bVal {
		return 1
	}
	return 0
}

func parseNumber(s string) float64 {
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return val
}

// renderToken 將識別符轉換為模板格式（如有必要）
func renderToken(token string, ctx *VarContext) string {
	// 已經是模板格式，直接返回
	if strings.HasPrefix(token, "{{") && strings.HasSuffix(token, "}}") {
		return token
	}

	// 嘗試解析為數字，如果成功就是字面值
	if _, err := strconv.ParseFloat(token, 64); err == nil {
		return token
	}

	// 檢查是否看起來像變數引用或字面字符串
	// 如果包含特殊字符且不是引號，可能是變數引用
	if strings.Contains(token, ".") {
		// 可能是 vars.key、capture.key、data.key 等形式
		if strings.HasPrefix(token, "vars.") || strings.HasPrefix(token, "capture.") || strings.HasPrefix(token, "data.") {
			return "{{" + token + "}}"
		}
		// 或者是 key.subkey 形式，假設是 vars.key.subkey
		return "{{vars." + token + "}}"
	}

	// 簡單的識別符，假設是變數引用
	if isSimpleIdentifier(token) {
		// 檢查是否存在於 ctx.Vars 或其他地方
		if ctx != nil && ctx.Vars != nil {
			if _, ok := ctx.Vars[token]; ok {
				return "{{vars." + token + "}}"
			}
		}
		if ctx != nil && ctx.Captures != nil {
			if _, ok := ctx.Captures[token]; ok {
				return "{{capture." + token + "}}"
			}
		}
		if ctx != nil && ctx.Data != nil {
			if _, ok := ctx.Data[token]; ok {
				return "{{data." + token + "}}"
			}
		}
		// 如果找不到，假設是 vars.token
		return "{{vars." + token + "}}"
	}

	// 其他情況，返回原始值（可能是字面字符串）
	return token
}

// isSimpleIdentifier 檢查字符串是否是簡單的識別符
func isSimpleIdentifier(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, ch := range s {
		if i == 0 {
			if !isLetter(byte(ch)) && ch != '_' {
				return false
			}
		} else {
			if !isLetter(byte(ch)) && !unicode.IsDigit(ch) && ch != '_' {
				return false
			}
		}
	}
	return true
}
