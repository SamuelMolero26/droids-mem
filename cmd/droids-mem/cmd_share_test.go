package main

import (
	"reflect"
	"testing"
)

func TestParsePicks(t *testing.T) {
	cases := []struct {
		line string
		n    int
		want []int
	}{
		{"", 5, nil},
		{"   ", 5, nil},
		{"all", 3, []int{0, 1, 2}},
		{"ALL", 2, []int{0, 1}},
		{"1,3", 5, []int{0, 2}},
		{" 2 , 4 ", 5, []int{1, 3}},
		{"0,6,x,2", 5, []int{1}}, // out-of-range + non-numeric dropped
		{"2,2", 5, []int{1, 1}},  // dupes preserved; SetScope is idempotent
	}
	for _, c := range cases {
		if got := parsePicks(c.line, c.n); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parsePicks(%q, %d) = %v, want %v", c.line, c.n, got, c.want)
		}
	}
}
