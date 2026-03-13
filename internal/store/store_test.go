package store

import "testing"

func TestMigrationsEmbed(t *testing.T) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 migration files, got %d", len(entries))
	}
	// Verify sorted order
	for i := 1; i < len(entries); i++ {
		if entries[i].Name() < entries[i-1].Name() {
			t.Errorf("not sorted: %s < %s", entries[i].Name(), entries[i-1].Name())
		}
	}
}
