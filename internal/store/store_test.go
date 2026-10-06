package store

import "testing"

func TestLiteralPattern(t *testing.T) {
	if got := LiteralPattern(`ABC%_\123`); got != `ABC\%\_\\123` {
		t.Fatal(got)
	}
	if got := LiteralPattern("ПК-01"); got != "ПК-01" {
		t.Fatal(got)
	}
}
