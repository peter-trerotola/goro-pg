package guard

import (
	"testing"
)

func TestExtractTableRefs_SimpleSelect(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM users")
	assertTableRefs(t, refs, []TableRef{{Schema: "public", Table: "users"}})
}

func TestExtractTableRefs_SchemaQualified(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM myschema.users")
	assertTableRefs(t, refs, []TableRef{{Schema: "myschema", Table: "users"}})
}

func TestExtractTableRefs_InnerJoin(t *testing.T) {
	refs := ExtractTableRefs("SELECT u.name, o.total FROM users u JOIN orders o ON u.id = o.user_id")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "orders"},
	})
}

func TestExtractTableRefs_LeftJoin(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM users u LEFT JOIN orders o ON u.id = o.user_id")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "orders"},
	})
}

func TestExtractTableRefs_MultipleJoins(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM users u JOIN orders o ON u.id = o.user_id JOIN products p ON o.product_id = p.id")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "orders"},
		{Schema: "public", Table: "products"},
	})
}

func TestExtractTableRefs_SubqueryInFrom(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM (SELECT id FROM orders) sub JOIN users u ON sub.id = u.id")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "orders"},
		{Schema: "public", Table: "users"},
	})
}

func TestExtractTableRefs_CTE(t *testing.T) {
	refs := ExtractTableRefs("WITH active AS (SELECT * FROM users WHERE active = true) SELECT * FROM active JOIN orders ON active.id = orders.user_id")
	// CTE "active" appears as a table ref in the outer FROM clause.
	// The parser sees it as a RangeVar — this is expected.
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "active"},
		{Schema: "public", Table: "orders"},
	})
}

func TestExtractTableRefs_Union(t *testing.T) {
	refs := ExtractTableRefs("SELECT id FROM users UNION SELECT id FROM admins")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "admins"},
	})
}

func TestExtractTableRefs_Intersect(t *testing.T) {
	refs := ExtractTableRefs("SELECT id FROM users INTERSECT SELECT id FROM admins")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "admins"},
	})
}

func TestExtractTableRefs_Except(t *testing.T) {
	refs := ExtractTableRefs("SELECT id FROM users EXCEPT SELECT id FROM banned")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "banned"},
	})
}

func TestExtractTableRefs_AliasedTable(t *testing.T) {
	refs := ExtractTableRefs("SELECT t.id FROM users AS t")
	assertTableRefs(t, refs, []TableRef{{Schema: "public", Table: "users"}})
}

func TestExtractTableRefs_NoFromClause(t *testing.T) {
	refs := ExtractTableRefs("SELECT 1+1")
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d: %v", len(refs), refs)
	}
}

func TestExtractTableRefs_InvalidSQL(t *testing.T) {
	refs := ExtractTableRefs("NOT VALID SQL {{{}}")
	if refs != nil {
		t.Errorf("expected nil for invalid SQL, got %v", refs)
	}
}

func TestExtractTableRefs_Explain(t *testing.T) {
	refs := ExtractTableRefs("EXPLAIN SELECT * FROM users JOIN orders ON users.id = orders.user_id")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "orders"},
	})
}

func TestExtractTableRefs_DuplicateTablesDeduped(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM users WHERE id IN (SELECT user_id FROM users)")
	// "users" appears in FROM and again in WHERE subquery — dedup should produce exactly 1 ref.
	assertTableRefs(t, refs, []TableRef{{Schema: "public", Table: "users"}})
}

func TestExtractTableRefs_MultipleSchemasQualified(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM public.users JOIN audit.logs ON users.id = logs.user_id")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "audit", Table: "logs"},
	})
}

// --- Subquery extraction tests ---

func TestExtractTableRefs_WhereIN(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM users WHERE id IN (SELECT user_id FROM orders)")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "orders"},
	})
}

func TestExtractTableRefs_WhereExists(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM users u WHERE EXISTS (SELECT 1 FROM orders o WHERE o.user_id = u.id)")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "orders"},
	})
}

func TestExtractTableRefs_WhereNotExists(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM users u WHERE NOT EXISTS (SELECT 1 FROM banned b WHERE b.user_id = u.id)")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "banned"},
	})
}

func TestExtractTableRefs_WhereScalarSubquery(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM users WHERE age > (SELECT AVG(age) FROM stats)")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "stats"},
	})
}

func TestExtractTableRefs_SelectListSubquery(t *testing.T) {
	refs := ExtractTableRefs("SELECT id, (SELECT count(*) FROM orders WHERE orders.user_id = users.id) AS order_count FROM users")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "orders"},
	})
}

func TestExtractTableRefs_HavingSubquery(t *testing.T) {
	refs := ExtractTableRefs("SELECT user_id, count(*) FROM orders GROUP BY user_id HAVING count(*) > (SELECT avg(cnt) FROM (SELECT count(*) cnt FROM orders GROUP BY user_id) sub)")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "orders"},
	})
}

func TestExtractTableRefs_NestedSubqueries(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM users WHERE id IN (SELECT user_id FROM orders WHERE product_id IN (SELECT id FROM products))")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "orders"},
		{Schema: "public", Table: "products"},
	})
}

func TestExtractTableRefs_WhereAndFromSubquery(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM (SELECT id FROM users) sub WHERE sub.id IN (SELECT user_id FROM orders)")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "orders"},
	})
}

func TestExtractTableRefs_CaseSubquery(t *testing.T) {
	refs := ExtractTableRefs("SELECT CASE WHEN (SELECT count(*) FROM admins) > 0 THEN 'yes' ELSE 'no' END FROM users")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "admins"},
	})
}

func TestExtractTableRefs_CoalesceSubquery(t *testing.T) {
	refs := ExtractTableRefs("SELECT COALESCE((SELECT name FROM settings WHERE key = 'default'), 'fallback') FROM users")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "settings"},
	})
}

func TestExtractTableRefs_FunctionArgSubquery(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM users WHERE id = ANY(SELECT user_id FROM orders)")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "orders"},
	})
}

func TestExtractTableRefs_MultipleWhereSubqueries(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM users WHERE id IN (SELECT user_id FROM orders) AND email IN (SELECT email FROM verified)")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "public", Table: "orders"},
		{Schema: "public", Table: "verified"},
	})
}

func TestExtractTableRefs_SchemaQualifiedSubquery(t *testing.T) {
	refs := ExtractTableRefs("SELECT * FROM public.users WHERE id IN (SELECT user_id FROM audit.logs)")
	assertTableRefs(t, refs, []TableRef{
		{Schema: "public", Table: "users"},
		{Schema: "audit", Table: "logs"},
	})
}

func assertTableRefs(t *testing.T, got []TableRef, want []TableRef) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %d table refs, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ref[%d]: expected %v, got %v", i, want[i], got[i])
		}
	}
}
