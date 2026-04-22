package handler

import (
	"context"
	"testing"

	"github.com/owenrumney/go-lsp/lsp"
)

func TestDiagnosticsCleanDocumentReturnsEmpty(t *testing.T) {
	doc := "import QtQuick\n\nRectangle {\n    width: 100\n}\n"
	h := newTestHandler(t, "test://clean.qml", doc)

	report, err := h.DocumentDiagnostic(context.Background(), &lsp.DocumentDiagnosticParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: "test://clean.qml"},
	})
	if err != nil {
		t.Fatalf("DocumentDiagnostic: %v", err)
	}
	full, ok := report.(lsp.FullDocumentDiagnosticReport)
	if !ok {
		t.Fatalf("expected FullDocumentDiagnosticReport, got %T", report)
	}
	if full.Items == nil {
		t.Error("Items must be normalized to [], not nil")
	}
	if len(full.Items) != 0 {
		t.Errorf("expected no diagnostics on clean doc, got %d", len(full.Items))
	}
}

func TestDiagnosticsReportsSyntaxError(t *testing.T) {
	// Missing closing brace.
	doc := "import QtQuick\n\nRectangle {\n    width: 100\n"
	h := newTestHandler(t, "test://broken.qml", doc)

	report, err := h.DocumentDiagnostic(context.Background(), &lsp.DocumentDiagnosticParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: "test://broken.qml"},
	})
	if err != nil {
		t.Fatalf("DocumentDiagnostic: %v", err)
	}
	full, ok := report.(lsp.FullDocumentDiagnosticReport)
	if !ok {
		t.Fatalf("expected FullDocumentDiagnosticReport, got %T", report)
	}
	if len(full.Items) == 0 {
		t.Fatal("expected at least one diagnostic for broken doc")
	}
	for _, d := range full.Items {
		if d.Source != "qml-language-server" {
			t.Errorf("unexpected diagnostic source %q", d.Source)
		}
		if d.Severity == nil || *d.Severity != lsp.SeverityError {
			t.Errorf("expected error severity, got %v", d.Severity)
		}
	}
}

// TestDiagnosticsSkipsOuterErrorWrappers guards against the "whole file
// lights up red on one typo" behavior: tree-sitter wraps a small syntax
// error in a giant outer ERROR that spans the document, so emitting every
// ERROR doubles the noise and draws a full-file underline. We only want
// the innermost precise error.
func TestDiagnosticsSkipsOuterErrorWrappers(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"missing-close", "import QtQuick\n\nRectangle {\n    width: 100\n"},
		{"half-binding", "import QtQuick\n\nRectangle {\n    color:\n    width: 100\n}\n"},
		{"junk-midfile", "import QtQuick\n\nRectangle {\n    id: r\n    @broken@\n    width: 100\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uri := lsp.DocumentURI("test://" + tc.name + ".qml")
			h := newTestHandler(t, uri, tc.doc)
			rep, err := h.DocumentDiagnostic(context.Background(), &lsp.DocumentDiagnosticParams{
				TextDocument: lsp.TextDocumentIdentifier{URI: uri},
			})
			if err != nil {
				t.Fatalf("DocumentDiagnostic: %v", err)
			}
			full := rep.(lsp.FullDocumentDiagnosticReport)
			for _, d := range full.Items {
				span := d.Range.End.Line - d.Range.Start.Line
				if span > 2 {
					t.Errorf("%s: diagnostic spans %d lines (%v) — outer wrapper leaked", tc.name, span, d.Range)
				}
			}
		})
	}
}
