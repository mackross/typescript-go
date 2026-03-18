package tstojs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFixtureHarness(t *testing.T) {
	fixtures, err := DiscoverFixtures("testdata/programs")
	if err != nil {
		t.Fatalf("discover fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("expected copied schema fixtures")
	}

	var notImplemented []string
	for _, fixture := range fixtures {
		_, err := GenerateFixture(context.Background(), fixture)
		if err == nil {
			t.Fatalf("fixture %q unexpectedly passed", fixture.Name)
		}
		if !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("fixture %q returned unexpected error: %v", fixture.Name, err)
		}
		notImplemented = append(notImplemented, fixture.Name)
	}

	t.Fatalf(
		"tstojs harness is wired and discovered %d fixtures, but generation is still stubbed; first fixtures: %s",
		len(notImplemented),
		strings.Join(notImplemented[:min(10, len(notImplemented))], ", "),
	)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
