package gsx

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNonceContextRoundTrip(t *testing.T) {
	ctx := WithNonce(context.Background(), "abc123")
	if got := NonceFromContext(ctx); got != "abc123" {
		t.Fatalf("NonceFromContext = %q, want %q", got, "abc123")
	}
}

func TestNonceFromContextAbsent(t *testing.T) {
	if got := NonceFromContext(context.Background()); got != "" {
		t.Fatalf("NonceFromContext = %q, want empty", got)
	}
}

func TestWriterNonce(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"no nonce in ctx", context.Background(), ""},
		{"empty nonce", WithNonce(context.Background(), ""), ""},
		{"plain", WithNonce(context.Background(), "abc123"), ` nonce="abc123"`},
		// entity forms must match htmlReplacer in escape.go — check
		// escape_test.go / escape.go for the exact entities and adjust if needed.
		{"hostile", WithNonce(context.Background(), `a"><script>`), ` nonce="a&#34;&gt;&lt;script&gt;"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sb strings.Builder
			gw := W(&sb)
			gw.Nonce(tt.ctx)
			if err := gw.Err(); err != nil {
				t.Fatalf("Err() = %v", err)
			}
			if sb.String() != tt.want {
				t.Fatalf("output = %q, want %q", sb.String(), tt.want)
			}
		})
	}
}

type nonceFailWriter struct{}

func (nonceFailWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

func TestWriterNonceAfterError(t *testing.T) {
	gw := W(nonceFailWriter{})
	gw.S("x") // sets the retained first-write error
	first := gw.Err()
	if first == nil {
		t.Fatal("setup: expected a retained write error")
	}
	gw.Nonce(WithNonce(context.Background(), "abc123"))
	if gw.Err() != first {
		t.Fatalf("Nonce after error replaced the retained error: %v", gw.Err())
	}
}
