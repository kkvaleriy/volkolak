package cmdx

import (
	"context"
	"strings"
	"testing"
)

func TestExecEcho(t *testing.T) {
	out, err := Exec(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Fatalf("stdout %q", out)
	}
}

func TestExecMissingBinary(t *testing.T) {
	_, err := Exec(context.Background(), "volkolak-missing-binary")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBeatOutsideActivity(t *testing.T) {
	stop := Beat(context.Background(), "x")
	stop()
}
