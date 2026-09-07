package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/owenrumney/go-lsp/lsp"
)

func TestCountParams(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"no params", "Qt.rect()", 0},
		{"one param", "Qt.rect(100)", 0},
		{"two params", "Qt.rect(100, 200)", 1},
		{"three params", "Qt.rect(100, 200, 50)", 2},
		{"params with strings", "console.log(\"hello\", \"world\")", 1},
		{"nested parens", "String(value)", 0},
		{"empty", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := countParams(tt.input)
			if result != tt.expected {
				t.Errorf("countParams(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsColorValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"red", "red", true},
		{"green", "green", true},
		{"blue", "blue", true},
		{"white", "white", true},
		{"black", "black", true},
		{"yellow", "yellow", true},
		{"cyan", "cyan", true},
		{"magenta", "magenta", true},
		{"gray", "gray", true},
		{"grey", "grey", true},
		{"transparent", "transparent", true},
		{"purple", "purple", false},
		{"orange", "orange", false},
		{"empty", "", false},
		{"uppercase", "RED", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isColorValue(tt.input)
			if result != tt.expected {
				t.Errorf("isColorValue(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsQuotedString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"double quoted", "\"hello\"", true},
		{"single quoted", "'hello'", false},
		{"unquoted", "hello", false},
		{"partial quote", "\"hello", false},
		{"empty quotes", "\"\"", true},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isQuotedString(tt.input)
			if result != tt.expected {
				t.Errorf("isQuotedString(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSafeString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"normal string", "hello", "hello"},
		{"empty string", "", "<empty>"},
		{"spaces only", "   ", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := safeString(tt.input)
			if result != tt.expected {
				t.Errorf("safeString(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSafeSliceLen(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected int
	}{
		{"normal slice", []string{"a", "b", "c"}, 3},
		{"empty slice", []string{}, 0},
		{"nil slice", nil, 0},
		{"single element", []string{"a"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := safeSliceLen(tt.input)
			if result != tt.expected {
				t.Errorf("safeSliceLen(%v) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestHandlerError(t *testing.T) {
	err := newHandlerError("TEST_ERROR", "test message", nil)

	if err.Code != "TEST_ERROR" {
		t.Errorf("err.Code = %q, want %q", err.Code, "TEST_ERROR")
	}

	if err.Message != "test message" {
		t.Errorf("err.Message = %q, want %q", err.Message, "test message")
	}

	if err.Error() != "TEST_ERROR: test message" {
		t.Errorf("err.Error() = %q, want %q", err.Error(), "TEST_ERROR: test message")
	}
}

func TestHandlerErrorWithCause(t *testing.T) {
	cause := newHandlerError("CAUSE", "cause message", nil)
	err := newHandlerError("WRAPPER", "wrapper message", cause)

	if err.Unwrap() != cause {
		t.Errorf("err.Unwrap() = %v, want %v", err.Unwrap(), cause)
	}

	expected := "WRAPPER: wrapper message (CAUSE: cause message)"
	if err.Error() != expected {
		t.Errorf("err.Error() = %q, want %q", err.Error(), expected)
	}
}

func TestQMLTypeInfo(t *testing.T) {
	// Inject a fake type into the registry so the lookup path is exercised
	// without relying on qmltypes discovery having run.
	registerSymbols(QMLSymbol{
		Label:    "FakeTypeInfoWidget",
		Kind:     lsp.CompletionItemKindClass,
		Module:   "FakeModule",
		Category: "type",
	})

	tests := []struct {
		name       string
		typeName   string
		wantOK     bool
		wantModule string
		wantType   string
	}{
		{"injected", "FakeTypeInfoWidget", true, "FakeModule", "Object"},
		{"Unknown", "UnknownType", false, "", ""},
		{"Empty", "", false, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := getTypeInfo(tt.typeName)
			if ok != tt.wantOK {
				t.Errorf("getTypeInfo(%q) ok = %v, want %v", tt.typeName, ok, tt.wantOK)
			}
			if ok {
				if info.Module != tt.wantModule {
					t.Errorf("getTypeInfo(%q).Module = %q, want %q", tt.typeName, info.Module, tt.wantModule)
				}
				if info.Type != tt.wantType {
					t.Errorf("getTypeInfo(%q).Type = %q, want %q", tt.typeName, info.Type, tt.wantType)
				}
			}
		})
	}
}

func TestHoverOnKnownType(t *testing.T) {
	// Seed a synthetic type/property so the hover lookup doesn't depend on
	// qmltypes discovery having populated the registry at startup.
	registerSymbols(
		QMLSymbol{Label: "HoverShape", Kind: lsp.CompletionItemKindClass, Module: "FakeModule", Category: "type"},
		QMLSymbol{Label: "thickness", Kind: lsp.CompletionItemKindProperty, Detail: "real — stroke thickness", Signature: "thickness: real", Category: "property"},
	)

	h := newTestHandler(t, "test://foo.qml", "HoverShape {\n    thickness: 100\n}\n")

	cases := []struct {
		name     string
		pos      lsp.Position
		wantText string
	}{
		{"on type name", lsp.Position{Line: 0, Character: 3}, "HoverShape"},
		{"on property", lsp.Position{Line: 1, Character: 6}, "thickness"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := h.Hover(context.Background(), &lsp.HoverParams{
				TextDocumentPositionParams: lsp.TextDocumentPositionParams{
					TextDocument: lsp.TextDocumentIdentifier{URI: "test://foo.qml"},
					Position:     tt.pos,
				},
			})
			if err != nil {
				t.Fatalf("Hover returned error: %v", err)
			}
			if got == nil {
				t.Fatal("Hover returned nil")
			}
			if !strings.Contains(got.Contents.Value, tt.wantText) {
				t.Errorf("Hover value %q does not mention %q", got.Contents.Value, tt.wantText)
			}
			if got.Range == nil {
				t.Error("Hover range is nil")
			}
		})
	}
}

// TestCompletionReturnsEmptyWithoutQmlls verifies that when qmlls isn't
// running the completion handler returns an empty list rather than nil or
// an error — the client expects a valid CompletionList either way.
func TestCompletionReturnsEmptyWithoutQmlls(t *testing.T) {
	h := newTestHandler(t, "test://foo.qml", "import QtQuick\n\nRectangle {\n    \n}\n")
	// newTestHandler doesn't start qmlls (no initialize call) so h.qmlls is nil.
	list, err := h.Completion(context.Background(), &lsp.CompletionParams{
		TextDocumentPositionParams: lsp.TextDocumentPositionParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: "test://foo.qml"},
			Position:     lsp.Position{Line: 3, Character: 4},
		},
	})
	if err != nil {
		t.Fatalf("Completion: %v", err)
	}
	if list == nil || list.Items == nil {
		t.Fatal("expected empty CompletionList, got nil")
	}
	if len(list.Items) != 0 {
		t.Errorf("expected 0 items without qmlls, got %d", len(list.Items))
	}
}

func newTestHandler(t *testing.T, uri lsp.DocumentURI, text string) *Handler {
	t.Helper()
	h := New(nil)
	if h.parser == nil {
		t.Skip("parser unavailable in test environment")
	}
	// Language-feature tests: switch off the diagnostic producers, whose
	// qmllint staging file can outlive t.TempDir's cleanup.
	h.qmllint = nil
	h.conventions = nil
	h.trscheck = nil
	if err := h.DidOpen(context.Background(), &lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: uri, Text: text},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}
	return h
}

func TestQMLPropertyInfo(t *testing.T) {
	// Inject a fake property into the registry so the lookup path is
	// exercised without relying on qmltypes discovery having run.
	registerSymbols(QMLSymbol{
		Label:    "fakePropInfoField",
		Kind:     lsp.CompletionItemKindProperty,
		Detail:   "real — Some synthetic property",
		Category: "property",
	})

	tests := []struct {
		name     string
		propName string
		wantOK   bool
		wantType string
	}{
		{"injected", "fakePropInfoField", true, "real"},
		{"Unknown", "unknownProp", false, ""},
		{"Empty", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := getPropertyInfo(tt.propName)
			if ok != tt.wantOK {
				t.Errorf("getPropertyInfo(%q) ok = %v, want %v", tt.propName, ok, tt.wantOK)
			}
			if ok && info.Type != tt.wantType {
				t.Errorf("getPropertyInfo(%q).Type = %q, want %q", tt.propName, info.Type, tt.wantType)
			}
		})
	}
}
