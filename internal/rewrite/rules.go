// Package rewrite implements the tiny set of SQL rewrites that let Webmin and
// Virtualmin talk to Amazon RDS PostgreSQL. See README.md for the rationale.
package rewrite

import (
	"regexp"
	"strings"
)

type Rule struct {
	Name    string
	match   *regexp.Regexp
	replace string
}

var rules = []Rule{
	{
		Name:    "pg_shadow->pg_user",
		match:   regexp.MustCompile(`\bpg_shadow\b`),
		replace: "pg_user",
	},
	{
		Name:    "pg_authid->pg_roles",
		match:   regexp.MustCompile(`\bpg_authid\b`),
		replace: "pg_roles",
	},
}

var triggers = []string{"pg_shadow", "pg_authid"}

// Apply returns the rewritten SQL and the names of rules that fired. If no
// rule fires, the input is returned unchanged and the slice is nil.
//
// A cheap substring prefilter skips the regex engine entirely for statements
// that don't mention any restricted catalog, which is the overwhelming
// majority of traffic from Webmin/Virtualmin.
func Apply(sql string) (string, []string) {
	hit := false
	for _, t := range triggers {
		if strings.Contains(sql, t) {
			hit = true
			break
		}
	}
	if !hit {
		return sql, nil
	}

	out := sql
	var applied []string
	for _, r := range rules {
		if r.match.MatchString(out) {
			out = r.match.ReplaceAllString(out, r.replace)
			applied = append(applied, r.Name)
		}
	}
	return out, applied
}
