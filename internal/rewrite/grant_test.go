package rewrite

import "testing"

func TestExtractCreateRole(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`create user "nettantra_mydb" with password 'xxx'`, "nettantra_mydb"},
		{`CREATE ROLE foo NOLOGIN`, "foo"},
		{`CREATE GROUP admins`, "admins"},
		{`create user mapping for u server s`, ""},
		{`CREATE USER MAPPING FOR public SERVER s`, ""},
		{`CREATE TABLE x (id int)`, ""},
		{`drop user x`, ""},
		{`select 1 from create_user_log`, ""},
		{`  CREATE  USER   "weird ""name"  WITH LOGIN`, `weird "name`},
		{`CREATE ROLE "x"`, "x"},
	}
	for _, tc := range cases {
		got := ExtractCreateRole(tc.in)
		if got != tc.want {
			t.Errorf("ExtractCreateRole(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`x`, `"x"`},
		{`a"b`, `"a""b"`},
		{`nettantra_mydb`, `"nettantra_mydb"`},
	}
	for _, tc := range cases {
		if got := QuoteIdent(tc.in); got != tc.want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildGrant(t *testing.T) {
	got := BuildGrant("nettantra_mydb", "nettantrapgmaster")
	want := `GRANT "nettantra_mydb" TO "nettantrapgmaster"`
	if got != want {
		t.Errorf("BuildGrant: got %q, want %q", got, want)
	}
}
