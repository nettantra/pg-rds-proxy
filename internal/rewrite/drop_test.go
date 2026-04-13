package rewrite

import "testing"

func TestExtractDropDatabase(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`DROP DATABASE "mydb"`, "mydb"},
		{`drop database mydb`, "mydb"},
		{`DROP DATABASE IF EXISTS "mydb"`, "mydb"},
		{`drop database if exists mydb`, "mydb"},
		{`DROP DATABASE "weird ""name"`, `weird "name`},
		{`DROP TABLE mydb`, ""},
		{`SELECT * FROM pg_database`, ""},
		{`drop user mydb`, ""},
	}
	for _, tc := range cases {
		if got := ExtractDropDatabase(tc.in); got != tc.want {
			t.Errorf("ExtractDropDatabase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQuoteString(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`mydb`, `'mydb'`},
		{`bob's`, `'bob''s'`},
		{``, `''`},
	}
	for _, tc := range cases {
		if got := QuoteString(tc.in); got != tc.want {
			t.Errorf("QuoteString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildTerminate(t *testing.T) {
	got := BuildTerminate("mydb")
	want := `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'mydb' AND pid <> pg_backend_pid()`
	if got != want {
		t.Errorf("BuildTerminate: got %q, want %q", got, want)
	}
}
