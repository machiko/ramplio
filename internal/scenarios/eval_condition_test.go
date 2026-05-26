package scenarios

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLexer_Basic 測試 Lexer 基本功能
func TestLexer_Basic(t *testing.T) {
	tests := []struct {
		input    string
		expected []Token
	}{
		{
			input: "status==200",
			expected: []Token{
				{Type: TokenIdentifier, Value: "status"},
				{Type: TokenEQ, Value: "=="},
				{Type: TokenNumber, Value: "200"},
				{Type: TokenEOF},
			},
		},
		{
			input: "status != 200",
			expected: []Token{
				{Type: TokenIdentifier, Value: "status"},
				{Type: TokenNE, Value: "!="},
				{Type: TokenNumber, Value: "200"},
				{Type: TokenEOF},
			},
		},
		{
			input: "status AND error_rate < 1",
			expected: []Token{
				{Type: TokenIdentifier, Value: "status"},
				{Type: TokenAND, Value: "AND"},
				{Type: TokenIdentifier, Value: "error_rate"},
				{Type: TokenLT, Value: "<"},
				{Type: TokenNumber, Value: "1"},
				{Type: TokenEOF},
			},
		},
		{
			input: `name == "test"`,
			expected: []Token{
				{Type: TokenIdentifier, Value: "name"},
				{Type: TokenEQ, Value: "=="},
				{Type: TokenString, Value: "test"},
				{Type: TokenEOF},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			lexer := NewLexer(test.input)
			var tokens []Token
			for {
				tok := lexer.NextToken()
				tokens = append(tokens, tok)
				if tok.Type == TokenEOF {
					break
				}
			}
			assert.Equal(t, test.expected, tokens)
		})
	}
}

// TestParser_SimpleComparison 測試簡單比較
func TestParser_SimpleComparison(t *testing.T) {
	tests := []struct {
		input    string
		ctx      *VarContext
		expected bool
		hasErr   bool
	}{
		{
			input:    "200 == 200",
			ctx:      &VarContext{},
			expected: true,
		},
		{
			input:    "200 != 300",
			ctx:      &VarContext{},
			expected: true,
		},
		{
			input:    "100 < 200",
			ctx:      &VarContext{},
			expected: true,
		},
		{
			input:    "200 <= 200",
			ctx:      &VarContext{},
			expected: true,
		},
		{
			input:    "300 > 200",
			ctx:      &VarContext{},
			expected: true,
		},
		{
			input:    "200 >= 200",
			ctx:      &VarContext{},
			expected: true,
		},
		{
			input:    "100 < 50",
			ctx:      &VarContext{},
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err)
			result, err := expr.Evaluate(test.ctx)
			if test.hasErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, test.expected, result)
			}
		})
	}
}

// TestParser_AND 測試 AND 運算子
func TestParser_AND(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{
			input:    "200 == 200 AND 100 < 200",
			expected: true,
		},
		{
			input:    "200 == 200 AND 100 > 200",
			expected: false,
		},
		{
			input:    "200 != 200 AND 100 < 200",
			expected: false,
		},
		{
			input:    "200 == 200 AND 100 < 200 AND 50 < 100",
			expected: true,
		},
		{
			input:    "200 == 200 AND 100 < 200 AND 50 > 100",
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err)
			result, err := expr.Evaluate(&VarContext{})
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result)
		})
	}
}

// TestParser_OR 測試 OR 運算子
func TestParser_OR(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{
			input:    "200 == 200 OR 100 > 200",
			expected: true,
		},
		{
			input:    "200 != 200 OR 100 > 200",
			expected: false,
		},
		{
			input:    "200 == 300 OR 100 < 200",
			expected: true,
		},
		{
			input:    "200 == 200 OR 100 > 200 OR 50 > 100",
			expected: true,
		},
		{
			input:    "200 != 200 OR 100 > 200 OR 50 > 100",
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err)
			result, err := expr.Evaluate(&VarContext{})
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result)
		})
	}
}

// TestParser_NOT 測試 NOT 運算子
func TestParser_NOT(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{
			input:    "NOT 200 == 300",
			expected: true,
		},
		{
			input:    "NOT 200 == 200",
			expected: false,
		},
		{
			input:    "NOT NOT 200 == 200",
			expected: true,
		},
		{
			input:    "NOT 100 < 50",
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err)
			result, err := expr.Evaluate(&VarContext{})
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result)
		})
	}
}

// TestParser_Parentheses 測試括號與優先級
func TestParser_Parentheses(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{
			input:    "(200 == 200)",
			expected: true,
		},
		{
			input:    "(200 == 200 AND 100 < 200)",
			expected: true,
		},
		{
			input:    "200 == 200 OR 100 > 200 AND 50 < 100",
			expected: true,
		},
		{
			input:    "(200 == 200 OR 100 > 200) AND 50 < 100",
			expected: true,
		},
		{
			input:    "(200 != 200 OR 100 > 200) AND 50 < 100",
			expected: false,
		},
		{
			input:    "NOT (200 == 200 AND 100 < 200)",
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err)
			result, err := expr.Evaluate(&VarContext{})
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result)
		})
	}
}

// TestParser_ComplexExpression 測試複雜表達式
func TestParser_ComplexExpression(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{
			input:    "(200 == 200 AND 100 < 200) OR (300 == 300 AND 50 > 100)",
			expected: true,
		},
		{
			input:    "NOT (200 == 300) AND (100 < 200 OR 50 > 100)",
			expected: true,
		},
		{
			input:    "(200 == 200 OR 100 > 200) AND (50 < 100 OR 25 > 50)",
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err)
			result, err := expr.Evaluate(&VarContext{})
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result)
		})
	}
}

// TestParser_StringComparison 測試字符串比較
func TestParser_StringComparison(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{
			input:    `"hello" == "hello"`,
			expected: true,
		},
		{
			input:    `"hello" != "world"`,
			expected: true,
		},
		{
			input:    `"200" == "200"`,
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err)
			result, err := expr.Evaluate(&VarContext{})
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result)
		})
	}
}

// TestParser_WithVariables 測試變數渲染
func TestParser_WithVariables(t *testing.T) {
	ctx := &VarContext{
		Vars: map[string]string{
			"status":     "200",
			"error_rate": "0.5",
			"name":       "test",
		},
	}

	tests := []struct {
		input    string
		expected bool
	}{
		{
			input:    "status == 200",
			expected: true,
		},
		{
			input:    "status != 300",
			expected: true,
		},
		{
			input:    "error_rate < 1",
			expected: true,
		},
		{
			input:    `name == "test"`,
			expected: true,
		},
		{
			input:    "status == 200 AND error_rate < 1",
			expected: true,
		},
		{
			input:    `status == 200 AND name == "test"`,
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err)
			result, err := expr.Evaluate(ctx)
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result)
		})
	}
}

// TestParser_ErrorHandling 測試錯誤處理
func TestParser_ErrorHandling(t *testing.T) {
	tests := []struct {
		input   string
		hasErr  bool
		errMsg  string
	}{
		{
			input:  "status ==",
			hasErr: true,
		},
		{
			input:  "== 200",
			hasErr: true,
		},
		{
			input:  "(200 == 200",
			hasErr: true,
		},
		{
			input:  "200 == 200)",
			hasErr: true,
		},
		{
			input:  "status == 200 INVALID",
			hasErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			if test.hasErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, expr)
			}
		})
	}
}

// TestParser_OperatorPrecedence 測試運算子優先級
func TestParser_OperatorPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
		desc     string
	}{
		{
			name:     "AND_before_OR",
			input:    "200 == 200 OR 100 > 200 AND 50 < 100",
			expected: true,
			desc:     "AND has higher precedence than OR",
		},
		{
			name:     "NOT_before_AND",
			input:    "NOT 200 == 300 AND 100 < 200",
			expected: true,
			desc:     "NOT has higher precedence than AND",
		},
		{
			name:     "NOT_before_OR",
			input:    "NOT 200 == 300 OR 100 > 200",
			expected: true,
			desc:     "NOT has higher precedence than OR",
		},
		{
			name:     "multiple_AND_left_associative",
			input:    "200 == 200 AND 100 < 200 AND 50 < 100",
			expected: true,
			desc:     "AND is left-associative",
		},
		{
			name:     "multiple_OR_left_associative",
			input:    "200 != 200 OR 100 > 200 OR 50 < 100",
			expected: true,
			desc:     "OR is left-associative",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err, test.desc)
			result, err := expr.Evaluate(&VarContext{})
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result, test.desc)
		})
	}
}

// TestParser_FloatNumbers 測試浮點數比較
func TestParser_FloatNumbers(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{
			input:    "100.5 < 200.5",
			expected: true,
		},
		{
			input:    "100.5 == 100.5",
			expected: true,
		},
		{
			input:    "99.9 < 100",
			expected: true,
		},
		{
			input:    "0.5 < 1",
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err)
			result, err := expr.Evaluate(&VarContext{})
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result)
		})
	}
}

// TestLexer_CaseInsensitive 測試關鍵字大小寫敏感度
func TestLexer_CaseInsensitive(t *testing.T) {
	tests := []struct {
		input    string
		expected TokenType
	}{
		{"AND", TokenAND},
		{"and", TokenAND},
		{"And", TokenAND},
		{"OR", TokenOR},
		{"or", TokenOR},
		{"Or", TokenOR},
		{"NOT", TokenNOT},
		{"not", TokenNOT},
		{"Not", TokenNOT},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			lexer := NewLexer(test.input)
			tok := lexer.NextToken()
			assert.Equal(t, test.expected, tok.Type)
		})
	}
}

// TestParser_DottedIdentifiers 測試含點的識別符
func TestParser_DottedIdentifiers(t *testing.T) {
	ctx := &VarContext{
		Vars: map[string]string{
			"response.time": "100",
			"request.url":   "https://example.com",
		},
	}

	tests := []struct {
		input    string
		expected bool
	}{
		{
			input:    "response.time < 200",
			expected: true,
		},
		{
			input:    `request.url == "https://example.com"`,
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err)
			result, err := expr.Evaluate(ctx)
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result)
		})
	}
}

// TestParser_IntegrationComplex 測試複雜的整合場景
func TestParser_IntegrationComplex(t *testing.T) {
	ctx := &VarContext{
		Vars: map[string]string{
			"status_code":   "200",
			"response_time": "150",
			"error_rate":    "0.5",
		},
		Captures: map[string]string{
			"user_id": "42",
		},
	}

	tests := []struct {
		input    string
		expected bool
		name     string
	}{
		{
			name:     "complex_and_or",
			input:    "(status_code == 200 OR status_code == 201) AND response_time < 200",
			expected: true,
		},
		{
			name:     "nested_conditions",
			input:    "((status_code == 200 AND response_time < 200) OR status_code == 404) AND error_rate < 1",
			expected: true,
		},
		{
			name:     "capture_variable",
			input:    "user_id == 42",
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser := NewParser(test.input)
			expr, err := parser.Parse()
			assert.NoError(t, err, "parsing failed: %s", test.input)
			result, err := expr.Evaluate(ctx)
			assert.NoError(t, err, "evaluation failed: %s", test.input)
			assert.Equal(t, test.expected, result, "input: %s", test.input)
		})
	}
}

// TestEvalCondition_Integration 測試 EvalCondition 函數與新實現的整合
func TestEvalCondition_Integration(t *testing.T) {
	ctx := &VarContext{
		Vars: map[string]string{
			"status":     "200",
			"error_rate": "0.5",
		},
	}

	tests := []struct {
		expr     string
		expected bool
		name     string
	}{
		{
			name:     "simple_equality",
			expr:     "status == 200",
			expected: true,
		},
		{
			name:     "and_operator",
			expr:     "status == 200 AND error_rate < 1",
			expected: true,
		},
		{
			name:     "complex_expression",
			expr:     "(status == 200 OR status == 201) AND NOT error_rate > 1",
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := EvalCondition(test.expr, ctx)
			assert.Equal(t, test.expected, result, "expr: %s", test.expr)
		})
	}
}
