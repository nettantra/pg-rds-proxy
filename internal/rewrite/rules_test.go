package rewrite

import "testing"

func TestApply(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		fired  bool
	}{
		{
			name:  "virtualmin postgres_user_exists",
			in:    "select * from pg_shadow where usename = $1",
			want:  "select * from pg_user where usename = $1",
			fired: true,
		},
		{
			name:  "virtualmin domain delete join on pg_authid",
			in:    "select datname from pg_database join pg_authid on pg_database.datdba = pg_authid.oid where rolname = 'u'",
			want:  "select datname from pg_database join pg_roles on pg_database.datdba = pg_roles.oid where rolname = 'u'",
			fired: true,
		},
		{
			name:  "webmin get_pg_shadow_table full column list",
			in:    "select usename,usesysid,usecreatedb,usesuper,usecatupd,passwd,valuntil from pg_shadow",
			want:  "select usename,usesysid,usecreatedb,usesuper,usecatupd,passwd,valuntil from pg_user",
			fired: true,
		},
		{
			name:  "passthrough for unrelated catalog query",
			in:    "select datname from pg_database order by datname",
			want:  "select datname from pg_database order by datname",
			fired: false,
		},
		{
			name:  "word boundary protects pg_shadowy",
			in:    "select * from pg_shadowy",
			want:  "select * from pg_shadowy",
			fired: false,
		},
		{
			name:  "word boundary protects xpg_authid",
			in:    "select * from xpg_authid",
			want:  "select * from xpg_authid",
			fired: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, applied := Apply(tc.in)
			if got != tc.want {
				t.Errorf("Apply(%q) sql = %q, want %q", tc.in, got, tc.want)
			}
			if tc.fired && len(applied) == 0 {
				t.Errorf("expected a rule to fire, none did")
			}
			if !tc.fired && len(applied) != 0 {
				t.Errorf("expected no rule to fire, got %v", applied)
			}
		})
	}
}
