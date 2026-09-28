package store

import "testing"

func TestGet(t *testing.T) {
	if got := (DB{}).Get("k"); got != "k" {
		t.Fatalf("Get(k) = %q", got)
	}
}
