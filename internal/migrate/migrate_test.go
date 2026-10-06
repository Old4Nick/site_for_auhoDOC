package migrate

import "testing"

func TestMigrationHistory(t *testing.T) {
	known := []migration{{version: "001.sql", checksum: "a"}, {version: "002.sql", checksum: "b"}}
	for _, tc := range []struct {
		name    string
		applied map[string]string
		current bool
		wantErr bool
	}{
		{"new", map[string]string{}, false, false},
		{"partial", map[string]string{"001.sql": "a"}, false, false},
		{"current", map[string]string{"001.sql": "a", "002.sql": "b"}, true, false},
		{"missing", map[string]string{"001.sql": "a"}, true, true},
		{"changed", map[string]string{"001.sql": "changed"}, false, true},
		{"gap", map[string]string{"002.sql": "b"}, false, true},
		{"newer database", map[string]string{"001.sql": "a", "002.sql": "b", "003.sql": "c"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHistory(known, tc.applied, tc.current)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestEmbeddedMigrations(t *testing.T) {
	known, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(known) != 2 || known[0].version != "001_init.sql" || known[1].version != "002_search.sql" {
		t.Fatalf("unexpected versions: %+v", known)
	}
	for _, m := range known {
		if len(m.checksum) != 64 || m.sql == "" {
			t.Fatal("empty migration or invalid checksum")
		}
	}
}
