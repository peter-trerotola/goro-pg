package guard

import (
	"errors"
	"testing"
)

// filterConfig holds schema/table filter rules for testing.
type filterConfig struct {
	schemas []string
	include []string
	exclude []string
}

func (f *filterConfig) shouldIncludeSchema(schema string) bool {
	if len(f.schemas) == 0 {
		return true
	}
	for _, s := range f.schemas {
		if s == schema {
			return true
		}
	}
	return false
}

func (f *filterConfig) shouldIncludeTable(schema, table string) bool {
	qualified := schema + "." + table
	if len(f.include) > 0 {
		for _, t := range f.include {
			if t == qualified {
				return true
			}
		}
		return false
	}
	if len(f.exclude) > 0 {
		for _, t := range f.exclude {
			if t == qualified {
				return false
			}
		}
	}
	return true
}

func (f *filterConfig) allowed(schema, table string) bool {
	return f.shouldIncludeSchema(schema) && f.shouldIncludeTable(schema, table)
}

// --- Schema filter tests ---

func TestCheckTableFilter_SchemaFilter(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		schemas []string
		blocked bool
	}{
		// Allowed
		{"allowed_simple select from included schema", "SELECT * FROM public.users", []string{"public"}, false},
		{"allowed_unqualified defaults to public", "SELECT * FROM users", []string{"public"}, false},
		{"allowed_multiple included schemas", "SELECT * FROM public.users JOIN billing.invoices ON users.id = invoices.user_id", []string{"public", "billing"}, false},
		{"allowed_no schema filter means all pass", "SELECT * FROM any_schema.any_table", nil, false},
		{"allowed_select 1 no tables", "SELECT 1", []string{"public"}, false},
		{"allowed_schema qualified in subquery", "SELECT * FROM public.users WHERE id IN (SELECT user_id FROM public.orders)", []string{"public"}, false},

		// Blocked
		{"blocked_schema not in list", "SELECT * FROM secret.data", []string{"public"}, true},
		{"blocked_unqualified when public not included", "SELECT * FROM users", []string{"billing"}, true},
		{"blocked_one schema ok one blocked", "SELECT * FROM public.users JOIN secret.data ON users.id = data.user_id", []string{"public"}, true},
		{"blocked_subquery references blocked schema", "SELECT * FROM public.users WHERE id IN (SELECT user_id FROM secret.orders)", []string{"public"}, true},
		{"blocked_select list subquery blocked schema", "SELECT (SELECT count(*) FROM secret.logs) FROM public.users", []string{"public"}, true},
		{"blocked_having subquery blocked schema", "SELECT user_id, count(*) FROM public.orders GROUP BY user_id HAVING count(*) > (SELECT avg(c) FROM secret.stats)", []string{"public"}, true},
		{"blocked_nested subquery blocked schema", "SELECT * FROM public.users WHERE id IN (SELECT user_id FROM public.orders WHERE product_id IN (SELECT id FROM secret.products))", []string{"public"}, true},
		{"blocked_explain wrapping blocked schema", "EXPLAIN SELECT * FROM secret.data", []string{"public"}, true},
		{"blocked_union with blocked schema", "SELECT id FROM public.users UNION SELECT id FROM secret.admins", []string{"public"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &filterConfig{schemas: tc.schemas}
			err := CheckTableFilter(tc.sql, f.allowed)
			if tc.blocked && err == nil {
				t.Errorf("expected query to be blocked, but it was allowed")
			}
			if !tc.blocked && err != nil {
				t.Errorf("expected query to be allowed, but got: %v", err)
			}
			if tc.blocked && err != nil {
				var forbidden *ForbiddenError
				if !errors.As(err, &forbidden) {
					t.Errorf("expected ForbiddenError, got %T: %v", err, err)
				}
			}
		})
	}
}

// --- Table include filter tests ---

func TestCheckTableFilter_TableInclude(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		include []string
		blocked bool
	}{
		// Allowed
		{"allowed_included table", "SELECT * FROM users", []string{"public.users"}, false},
		{"allowed_schema qualified included", "SELECT * FROM public.users", []string{"public.users"}, false},
		{"allowed_multiple included tables join", "SELECT * FROM users JOIN orders ON users.id = orders.user_id", []string{"public.users", "public.orders"}, false},
		{"allowed_subquery all included", "SELECT * FROM users WHERE id IN (SELECT user_id FROM orders)", []string{"public.users", "public.orders"}, false},
		{"allowed_no tables referenced", "SELECT 1 + 1", []string{"public.users"}, false},
		{"allowed_cte with included table", "WITH active AS (SELECT * FROM users WHERE active = true) SELECT * FROM active", []string{"public.users"}, false},
		{"allowed_union all included", "SELECT id FROM users UNION SELECT id FROM orders", []string{"public.users", "public.orders"}, false},
		{"allowed_explain included", "EXPLAIN SELECT * FROM users", []string{"public.users"}, false},
		{"allowed_self join", "SELECT a.id FROM users a JOIN users b ON a.id = b.manager_id", []string{"public.users"}, false},
		{"allowed_cross schema included", "SELECT * FROM public.users JOIN billing.invoices ON users.id = invoices.user_id", []string{"public.users", "billing.invoices"}, false},
		{"allowed_nested subquery all included", "SELECT * FROM users WHERE id IN (SELECT user_id FROM orders WHERE product_id IN (SELECT id FROM products))", []string{"public.users", "public.orders", "public.products"}, false},
		{"allowed_case subquery included", "SELECT CASE WHEN (SELECT count(*) FROM users) > 0 THEN 'yes' ELSE 'no' END", []string{"public.users"}, false},
		{"allowed_from subquery included", "SELECT * FROM (SELECT id FROM users) sub", []string{"public.users"}, false},
		{"allowed_multiple ctes", "WITH a AS (SELECT * FROM users), b AS (SELECT * FROM orders) SELECT * FROM a JOIN b ON a.id = b.user_id", []string{"public.users", "public.orders"}, false},

		// Blocked
		{"blocked_not included", "SELECT * FROM secrets", []string{"public.users"}, true},
		{"blocked_one included one not", "SELECT * FROM users JOIN secrets ON users.id = secrets.user_id", []string{"public.users"}, true},
		{"blocked_subquery not included", "SELECT * FROM users WHERE id IN (SELECT user_id FROM secrets)", []string{"public.users"}, true},
		{"blocked_select list subquery not included", "SELECT (SELECT count(*) FROM secrets) FROM users", []string{"public.users"}, true},
		{"blocked_having subquery not included", "SELECT user_id, count(*) FROM users GROUP BY user_id HAVING count(*) > (SELECT avg(c) FROM secrets)", []string{"public.users"}, true},
		{"blocked_nested subquery not included", "SELECT * FROM users WHERE id IN (SELECT user_id FROM orders WHERE product_id IN (SELECT id FROM secrets))", []string{"public.users", "public.orders"}, true},
		{"blocked_union not included", "SELECT id FROM users UNION SELECT id FROM secrets", []string{"public.users"}, true},
		{"blocked_explain not included", "EXPLAIN SELECT * FROM secrets", []string{"public.users"}, true},
		{"blocked_from subquery not included", "SELECT * FROM (SELECT id FROM secrets) sub", []string{"public.users"}, true},
		{"blocked_cte body not included", "WITH leaked AS (SELECT * FROM secrets) SELECT * FROM leaked", []string{"public.users"}, true},
		{"blocked_exists subquery not included", "SELECT * FROM users WHERE EXISTS (SELECT 1 FROM secrets WHERE secrets.user_id = users.id)", []string{"public.users"}, true},
		{"blocked_not exists subquery not included", "SELECT * FROM users WHERE NOT EXISTS (SELECT 1 FROM secrets)", []string{"public.users"}, true},
		{"blocked_any subquery not included", "SELECT * FROM users WHERE id = ANY(SELECT user_id FROM secrets)", []string{"public.users"}, true},
		{"blocked_scalar comparison subquery", "SELECT * FROM users WHERE age > (SELECT max(age) FROM secrets)", []string{"public.users"}, true},
		{"blocked_case subquery not included", "SELECT CASE WHEN (SELECT count(*) FROM secrets) > 0 THEN 'yes' END FROM users", []string{"public.users"}, true},
		{"blocked_coalesce subquery not included", "SELECT COALESCE((SELECT name FROM secrets LIMIT 1), 'default') FROM users", []string{"public.users"}, true},
		{"blocked_cross join not included", "SELECT * FROM users CROSS JOIN secrets", []string{"public.users"}, true},
		{"blocked_left join not included", "SELECT * FROM users LEFT JOIN secrets ON users.id = secrets.user_id", []string{"public.users"}, true},
		{"blocked_right join not included", "SELECT * FROM users RIGHT JOIN secrets ON users.id = secrets.user_id", []string{"public.users"}, true},
		{"blocked_full outer join not included", "SELECT * FROM users FULL OUTER JOIN secrets ON users.id = secrets.user_id", []string{"public.users"}, true},
		{"blocked_intersect not included", "SELECT id FROM users INTERSECT SELECT id FROM secrets", []string{"public.users"}, true},
		{"blocked_except not included", "SELECT id FROM users EXCEPT SELECT id FROM secrets", []string{"public.users"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &filterConfig{include: tc.include}
			err := CheckTableFilter(tc.sql, f.allowed)
			if tc.blocked && err == nil {
				t.Errorf("expected query to be blocked, but it was allowed")
			}
			if !tc.blocked && err != nil {
				t.Errorf("expected query to be allowed, but got: %v", err)
			}
			if tc.blocked && err != nil {
				var forbidden *ForbiddenError
				if !errors.As(err, &forbidden) {
					t.Errorf("expected ForbiddenError, got %T: %v", err, err)
				}
			}
		})
	}
}

// --- Table exclude filter tests ---

func TestCheckTableFilter_TableExclude(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		exclude []string
		blocked bool
	}{
		// Allowed
		{"allowed_non-excluded table", "SELECT * FROM users", []string{"public.secrets"}, false},
		{"allowed_multiple non-excluded", "SELECT * FROM users JOIN orders ON users.id = orders.user_id", []string{"public.secrets"}, false},
		{"allowed_subquery non-excluded", "SELECT * FROM users WHERE id IN (SELECT user_id FROM orders)", []string{"public.secrets"}, false},
		{"allowed_no tables", "SELECT 1 + 1", []string{"public.secrets"}, false},
		{"allowed_cte non-excluded", "WITH active AS (SELECT * FROM users) SELECT * FROM active", []string{"public.secrets"}, false},

		// Blocked
		{"blocked_excluded table", "SELECT * FROM secrets", []string{"public.secrets"}, true},
		{"blocked_excluded schema qualified", "SELECT * FROM public.secrets", []string{"public.secrets"}, true},
		{"blocked_one ok one excluded", "SELECT * FROM users JOIN secrets ON users.id = secrets.user_id", []string{"public.secrets"}, true},
		{"blocked_subquery excluded", "SELECT * FROM users WHERE id IN (SELECT token FROM secrets)", []string{"public.secrets"}, true},
		{"blocked_select list subquery excluded", "SELECT (SELECT count(*) FROM secrets) FROM users", []string{"public.secrets"}, true},
		{"blocked_exists excluded", "SELECT * FROM users WHERE EXISTS (SELECT 1 FROM secrets)", []string{"public.secrets"}, true},
		{"blocked_nested subquery excluded", "SELECT * FROM users WHERE id IN (SELECT user_id FROM orders WHERE id IN (SELECT order_id FROM secrets))", []string{"public.secrets"}, true},
		{"blocked_union excluded", "SELECT id FROM users UNION SELECT id FROM secrets", []string{"public.secrets"}, true},
		{"blocked_cte body excluded", "WITH leaked AS (SELECT * FROM secrets) SELECT * FROM leaked", []string{"public.secrets"}, true},
		{"blocked_multiple excludes first hit", "SELECT * FROM secrets", []string{"public.secrets", "public.tokens"}, true},
		{"blocked_multiple excludes second hit", "SELECT * FROM tokens", []string{"public.secrets", "public.tokens"}, true},
		{"blocked_from subquery excluded", "SELECT * FROM (SELECT * FROM secrets) sub", []string{"public.secrets"}, true},
		{"blocked_explain excluded", "EXPLAIN SELECT * FROM secrets", []string{"public.secrets"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &filterConfig{exclude: tc.exclude}
			err := CheckTableFilter(tc.sql, f.allowed)
			if tc.blocked && err == nil {
				t.Errorf("expected query to be blocked, but it was allowed")
			}
			if !tc.blocked && err != nil {
				t.Errorf("expected query to be allowed, but got: %v", err)
			}
			if tc.blocked && err != nil {
				var forbidden *ForbiddenError
				if !errors.As(err, &forbidden) {
					t.Errorf("expected ForbiddenError, got %T: %v", err, err)
				}
			}
		})
	}
}

// --- Combined schema + table filter tests ---

func TestCheckTableFilter_Combined(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		schemas []string
		include []string
		exclude []string
		blocked bool
	}{
		// Schema + include
		{"allowed_schema and include both pass", "SELECT * FROM public.users", []string{"public"}, []string{"public.users"}, nil, false},
		{"blocked_schema pass include fail", "SELECT * FROM public.orders", []string{"public"}, []string{"public.users"}, nil, true},
		{"blocked_schema fail include pass", "SELECT * FROM billing.invoices", []string{"public"}, []string{"billing.invoices"}, nil, true},

		// Schema + exclude
		{"allowed_schema pass exclude pass", "SELECT * FROM public.users", []string{"public"}, nil, []string{"public.secrets"}, false},
		{"blocked_schema pass exclude fail", "SELECT * FROM public.secrets", []string{"public"}, nil, []string{"public.secrets"}, true},
		{"blocked_schema fail exclude pass", "SELECT * FROM billing.invoices", []string{"public"}, nil, []string{"public.secrets"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &filterConfig{schemas: tc.schemas, include: tc.include, exclude: tc.exclude}
			err := CheckTableFilter(tc.sql, f.allowed)
			if tc.blocked && err == nil {
				t.Errorf("expected query to be blocked, but it was allowed")
			}
			if !tc.blocked && err != nil {
				t.Errorf("expected query to be allowed, but got: %v", err)
			}
		})
	}
}

// --- CTE edge cases ---

func TestCheckTableFilter_CTENames(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		include []string
		blocked bool
	}{
		{"allowed_cte name not treated as table", "WITH active AS (SELECT * FROM users WHERE active = true) SELECT * FROM active", []string{"public.users"}, false},
		{"allowed_cte name same as real table", "WITH users AS (SELECT * FROM users WHERE active = true) SELECT * FROM users", []string{"public.users"}, false},
		{"allowed_multiple ctes", "WITH a AS (SELECT * FROM users), b AS (SELECT * FROM orders) SELECT * FROM a JOIN b ON a.id = b.user_id", []string{"public.users", "public.orders"}, false},
		{"allowed_recursive cte", "WITH RECURSIVE tree AS (SELECT id, parent_id FROM categories WHERE parent_id IS NULL UNION ALL SELECT c.id, c.parent_id FROM categories c JOIN tree t ON c.parent_id = t.id) SELECT * FROM tree", []string{"public.categories"}, false},
		{"allowed_nested cte", "WITH outer_cte AS (WITH inner_cte AS (SELECT * FROM users) SELECT * FROM inner_cte) SELECT * FROM outer_cte", []string{"public.users"}, false},
		{"blocked_cte body references blocked table", "WITH leaked AS (SELECT * FROM secrets) SELECT * FROM leaked", []string{"public.users"}, true},
		{"blocked_cte ok but outer references blocked", "WITH ok AS (SELECT * FROM users) SELECT * FROM ok JOIN secrets ON ok.id = secrets.user_id", []string{"public.users"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &filterConfig{include: tc.include}
			err := CheckTableFilter(tc.sql, f.allowed)
			if tc.blocked && err == nil {
				t.Errorf("expected query to be blocked, but it was allowed")
			}
			if !tc.blocked && err != nil {
				t.Errorf("expected query to be allowed, but got: %v", err)
			}
		})
	}
}

// --- No filters configured ---

func TestCheckTableFilter_NoFilters(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"any table", "SELECT * FROM anything"},
		{"any schema", "SELECT * FROM secret.data"},
		{"complex query", "SELECT * FROM a JOIN b.c ON a.id = c.id WHERE EXISTS (SELECT 1 FROM d.e)"},
		{"no tables", "SELECT 1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &filterConfig{} // no filters
			err := CheckTableFilter(tc.sql, f.allowed)
			if err != nil {
				t.Errorf("expected no filters to allow everything, got: %v", err)
			}
		})
	}
}

// --- Edge cases ---

func TestCheckTableFilter_EdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		include []string
		blocked bool
	}{
		{"allowed_invalid sql returns nil", "NOT VALID SQL {{{}}", []string{"public.users"}, false},
		{"allowed_empty sql", "", []string{"public.users"}, false},
		{"blocked_multiple statements second blocked", "SELECT * FROM users; SELECT * FROM secrets", []string{"public.users"}, true},
		{"allowed_aliased table still checked", "SELECT t.* FROM users AS t", []string{"public.users"}, false},
		{"blocked_aliased blocked table", "SELECT t.* FROM secrets AS t", []string{"public.users"}, true},
		{"allowed_table in function arg subquery", "SELECT * FROM users WHERE id = ANY(SELECT user_id FROM orders)", []string{"public.users", "public.orders"}, false},
		{"blocked_table in function arg subquery", "SELECT * FROM users WHERE id = ANY(SELECT user_id FROM secrets)", []string{"public.users"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &filterConfig{include: tc.include}
			err := CheckTableFilter(tc.sql, f.allowed)
			if tc.blocked && err == nil {
				t.Errorf("expected query to be blocked, but it was allowed")
			}
			if !tc.blocked && err != nil {
				t.Errorf("expected query to be allowed, but got: %v", err)
			}
		})
	}
}

// --- Error message quality ---

func TestCheckTableFilter_ErrorMessages(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		include  []string
		contains string
	}{
		{"includes table name", "SELECT * FROM secrets", []string{"public.users"}, "public.secrets"},
		{"includes restricted", "SELECT * FROM audit.logs", []string{"public.users"}, "restricted table"},
		{"schema qualified in error", "SELECT * FROM billing.invoices", []string{"public.users"}, "billing.invoices"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &filterConfig{include: tc.include}
			err := CheckTableFilter(tc.sql, f.allowed)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); !containsStr(got, tc.contains) {
				t.Errorf("expected error to contain %q, got %q", tc.contains, got)
			}
		})
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
