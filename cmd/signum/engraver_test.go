package main

import (
	"strings"
	"testing"
)

func TestPackingsBalanceLines(t *testing.T) {
	width := func(s string) float64 { return float64(len(s)) }

	texts := packings([]string{"aaaa", "bb", "cc"}, width)
	want := []string{
		"aaaa bb cc",  // one line
		"aaaa\nbb cc", // two: splitting after aaaa is the narrowest layout
		"aaaa\nbb\ncc",
	}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("packings:\n got %q\nwant %q", texts, want)
	}

	// Deterministic.
	again := packings([]string{"aaaa", "bb", "cc"}, width)
	if strings.Join(again, "|") != strings.Join(texts, "|") {
		t.Fatal("packings is not deterministic")
	}

	// One aspect is one candidate.
	if got := packings([]string{"only"}, width); len(got) != 1 || got[0] != "only" {
		t.Fatalf("single aspect packed as %q", got)
	}
	if got := packings(nil, width); got != nil {
		t.Fatalf("no aspects packed as %q", got)
	}
}
