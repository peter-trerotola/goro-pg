package postgres

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/petros/go-postgres-mcp/internal/config"
	"github.com/petros/go-postgres-mcp/internal/knowledgemap"
)

// Discover crawls a PostgreSQL database schema and populates the knowledge map.
// All existing knowledge map data for the database is cleared and then re-inserted.
// Schema and table filters from the config are applied during discovery.
//
// All 8 PG queries (schemas, tables, columns, constraints, indexes, foreign
// keys, views, functions) run concurrently using separate read-only transactions.
// This trades strict snapshot consistency for parallelism — concurrent DDL during
// discovery could yield slightly inconsistent results. This is acceptable because
// the target is a read-only replica where DDL does not occur, and re-discovery
// corrects any transient inconsistencies.
// Results are written to SQLite in dependency order: schemas → tables → everything else.
func Discover(ctx context.Context, pool *pgxpool.Pool, dbCfg config.DatabaseConfig, store *knowledgemap.Store) error {
	if err := store.ClearDatabase(dbCfg.Name); err != nil {
		return fmt.Errorf("clearing database %q: %w", dbCfg.Name, err)
	}

	if err := store.InsertDatabase(dbCfg.Name, dbCfg.Host, dbCfg.Port, dbCfg.Database); err != nil {
		return fmt.Errorf("inserting database record: %w", err)
	}

	// Fetch all metadata from PG concurrently.
	var (
		schemas     []string
		tables      []knowledgemap.TableInfo
		columns     []knowledgemap.ColumnInfo
		constraints []knowledgemap.ConstraintInfo
		indexes     []knowledgemap.IndexInfo
		foreignKeys []knowledgemap.ForeignKeyInfo
		views       []knowledgemap.ViewInfo
		functions   []knowledgemap.FunctionInfo
		wg          sync.WaitGroup
		mu          sync.Mutex
		firstErr    error
	)

	setErr := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}

	wg.Add(8)
	go func() {
		defer wg.Done()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			setErr(fmt.Errorf("beginning schemas transaction: %w", err))
			return
		}
		defer tx.Rollback(ctx)
		res, err := discoverSchemas(ctx, tx)
		if err != nil {
			setErr(err)
			return
		}
		mu.Lock()
		schemas = res
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			setErr(fmt.Errorf("beginning tables transaction: %w", err))
			return
		}
		defer tx.Rollback(ctx)
		res, err := fetchTables(ctx, tx, dbCfg)
		if err != nil {
			setErr(err)
			return
		}
		mu.Lock()
		tables = res
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		res, err := fetchColumns(ctx, pool, dbCfg)
		if err != nil {
			setErr(err)
			return
		}
		mu.Lock()
		columns = res
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		res, err := fetchConstraints(ctx, pool, dbCfg)
		if err != nil {
			setErr(err)
			return
		}
		mu.Lock()
		constraints = res
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		res, err := fetchIndexes(ctx, pool, dbCfg)
		if err != nil {
			setErr(err)
			return
		}
		mu.Lock()
		indexes = res
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		res, err := fetchForeignKeys(ctx, pool, dbCfg)
		if err != nil {
			setErr(err)
			return
		}
		mu.Lock()
		foreignKeys = res
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		res, err := fetchViews(ctx, pool, dbCfg)
		if err != nil {
			setErr(err)
			return
		}
		mu.Lock()
		views = res
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		res, err := fetchFunctions(ctx, pool, dbCfg)
		if err != nil {
			setErr(err)
			return
		}
		mu.Lock()
		functions = res
		mu.Unlock()
	}()
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	// Write to SQLite in dependency order: schemas → tables → rest.
	for _, s := range schemas {
		if !dbCfg.ShouldIncludeSchema(s) {
			continue
		}
		if err := store.InsertSchema(dbCfg.Name, s); err != nil {
			return fmt.Errorf("inserting schema %q: %w", s, err)
		}
	}
	for _, t := range tables {
		if err := store.InsertTable(dbCfg.Name, t); err != nil {
			return err
		}
	}
	for _, c := range columns {
		if err := store.InsertColumn(dbCfg.Name, c); err != nil {
			return err
		}
	}
	for _, c := range constraints {
		if err := store.InsertConstraint(dbCfg.Name, c); err != nil {
			return err
		}
	}
	for _, idx := range indexes {
		if err := store.InsertIndex(dbCfg.Name, idx); err != nil {
			return err
		}
	}
	for _, fk := range foreignKeys {
		if err := store.InsertForeignKey(dbCfg.Name, fk); err != nil {
			return err
		}
	}
	for _, v := range views {
		if err := store.InsertView(dbCfg.Name, v); err != nil {
			return err
		}
	}
	for _, f := range functions {
		if err := store.InsertFunction(dbCfg.Name, f); err != nil {
			return err
		}
	}

	if err := store.IndexForSearch(dbCfg.Name); err != nil {
		return fmt.Errorf("building search index: %w", err)
	}

	return nil
}

func discoverSchemas(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT schema_name FROM information_schema.schemata
		WHERE schema_name NOT IN ('pg_toast', 'pg_catalog', 'information_schema')
		ORDER BY schema_name`)
	if err != nil {
		return nil, fmt.Errorf("querying schemas: %w", err)
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		schemas = append(schemas, s)
	}
	return schemas, rows.Err()
}

func fetchTables(ctx context.Context, tx pgx.Tx, dbCfg config.DatabaseConfig) ([]knowledgemap.TableInfo, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			t.table_schema,
			t.table_name,
			t.table_type,
			COALESCE(c.reltuples::bigint, 0) AS row_estimate,
			COALESCE(pg_total_relation_size(c.oid), 0) AS size_bytes,
			COALESCE(obj_description(c.oid), '') AS description
		FROM information_schema.tables t
		LEFT JOIN pg_catalog.pg_namespace n ON n.nspname = t.table_schema
		LEFT JOIN pg_catalog.pg_class c ON c.relname = t.table_name AND c.relnamespace = n.oid
		WHERE t.table_schema NOT IN ('pg_toast', 'pg_catalog', 'information_schema')
		  AND t.table_type = 'BASE TABLE'
		ORDER BY t.table_schema, t.table_name`)
	if err != nil {
		return nil, fmt.Errorf("querying tables: %w", err)
	}
	defer rows.Close()

	var result []knowledgemap.TableInfo
	for rows.Next() {
		var t knowledgemap.TableInfo
		if err := rows.Scan(&t.SchemaName, &t.TableName, &t.TableType, &t.RowEstimate, &t.SizeBytes, &t.Description); err != nil {
			return nil, err
		}
		if !dbCfg.ShouldIncludeSchema(t.SchemaName) || !dbCfg.ShouldIncludeTable(t.SchemaName, t.TableName) {
			continue
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func fetchColumns(ctx context.Context, pool *pgxpool.Pool, dbCfg config.DatabaseConfig) ([]knowledgemap.ColumnInfo, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("beginning columns transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT
			c.table_schema,
			c.table_name,
			c.column_name,
			c.ordinal_position,
			c.data_type,
			c.is_nullable = 'YES' AS is_nullable,
			c.column_default,
			COALESCE(col_description(
				(quote_ident(c.table_schema) || '.' || quote_ident(c.table_name))::regclass,
				c.ordinal_position
			), '') AS description
		FROM information_schema.columns c
		JOIN information_schema.tables t
			ON t.table_schema = c.table_schema AND t.table_name = c.table_name AND t.table_type = 'BASE TABLE'
		WHERE c.table_schema NOT IN ('pg_toast', 'pg_catalog', 'information_schema')
		ORDER BY c.table_schema, c.table_name, c.ordinal_position`)
	if err != nil {
		return nil, fmt.Errorf("querying columns: %w", err)
	}
	defer rows.Close()

	var result []knowledgemap.ColumnInfo
	for rows.Next() {
		var col knowledgemap.ColumnInfo
		if err := rows.Scan(&col.SchemaName, &col.TableName, &col.ColumnName,
			&col.Ordinal, &col.DataType, &col.IsNullable, &col.ColumnDefault, &col.Description); err != nil {
			return nil, err
		}
		if !dbCfg.ShouldIncludeSchema(col.SchemaName) || !dbCfg.ShouldIncludeTable(col.SchemaName, col.TableName) {
			continue
		}
		result = append(result, col)
	}
	return result, rows.Err()
}

func fetchConstraints(ctx context.Context, pool *pgxpool.Pool, dbCfg config.DatabaseConfig) ([]knowledgemap.ConstraintInfo, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("beginning constraints transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT
			n.nspname AS schema_name,
			c.relname AS table_name,
			con.conname AS constraint_name,
			CASE con.contype
				WHEN 'p' THEN 'PRIMARY KEY'
				WHEN 'u' THEN 'UNIQUE'
				WHEN 'c' THEN 'CHECK'
				WHEN 'f' THEN 'FOREIGN KEY'
				WHEN 'x' THEN 'EXCLUSION'
			END AS constraint_type,
			pg_get_constraintdef(con.oid) AS definition
		FROM pg_catalog.pg_constraint con
		JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_toast', 'pg_catalog', 'information_schema')
		  AND c.relkind <> 'm'
		ORDER BY n.nspname, c.relname, con.conname`)
	if err != nil {
		return nil, fmt.Errorf("querying constraints: %w", err)
	}
	defer rows.Close()

	var result []knowledgemap.ConstraintInfo
	for rows.Next() {
		var con knowledgemap.ConstraintInfo
		if err := rows.Scan(&con.SchemaName, &con.TableName, &con.ConstraintName, &con.ConstraintType, &con.Definition); err != nil {
			return nil, err
		}
		if !dbCfg.ShouldIncludeSchema(con.SchemaName) || !dbCfg.ShouldIncludeTable(con.SchemaName, con.TableName) {
			continue
		}
		result = append(result, con)
	}
	return result, rows.Err()
}

func fetchIndexes(ctx context.Context, pool *pgxpool.Pool, dbCfg config.DatabaseConfig) ([]knowledgemap.IndexInfo, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("beginning indexes transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT
			n.nspname AS schema_name,
			t.relname AS table_name,
			i.relname AS index_name,
			ix.indisunique AS is_unique,
			ix.indisprimary AS is_primary,
			pg_get_indexdef(ix.indexrelid) AS definition
		FROM pg_catalog.pg_index ix
		JOIN pg_catalog.pg_class t ON t.oid = ix.indrelid
		JOIN pg_catalog.pg_class i ON i.oid = ix.indexrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname NOT IN ('pg_toast', 'pg_catalog', 'information_schema')
		  AND t.relkind <> 'm'
		ORDER BY n.nspname, t.relname, i.relname`)
	if err != nil {
		return nil, fmt.Errorf("querying indexes: %w", err)
	}
	defer rows.Close()

	var result []knowledgemap.IndexInfo
	for rows.Next() {
		var idx knowledgemap.IndexInfo
		if err := rows.Scan(&idx.SchemaName, &idx.TableName, &idx.IndexName, &idx.IsUnique, &idx.IsPrimary, &idx.Definition); err != nil {
			return nil, err
		}
		if !dbCfg.ShouldIncludeSchema(idx.SchemaName) || !dbCfg.ShouldIncludeTable(idx.SchemaName, idx.TableName) {
			continue
		}
		result = append(result, idx)
	}
	return result, rows.Err()
}

func fetchForeignKeys(ctx context.Context, pool *pgxpool.Pool, dbCfg config.DatabaseConfig) ([]knowledgemap.ForeignKeyInfo, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("beginning foreign keys transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT
			n.nspname AS schema_name,
			c.relname AS table_name,
			con.conname AS constraint_name,
			a.attname AS column_name,
			rn.nspname AS ref_schema,
			rc.relname AS ref_table,
			ra.attname AS ref_column
		FROM pg_catalog.pg_constraint con
		JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_catalog.pg_class rc ON rc.oid = con.confrelid
		JOIN pg_catalog.pg_namespace rn ON rn.oid = rc.relnamespace
		CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS cols(conkey, confkey, ord)
		JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = cols.conkey
		JOIN pg_catalog.pg_attribute ra ON ra.attrelid = rc.oid AND ra.attnum = cols.confkey
		WHERE con.contype = 'f'
		  AND n.nspname NOT IN ('pg_toast', 'pg_catalog', 'information_schema')
		ORDER BY n.nspname, c.relname, con.conname, cols.ord`)
	if err != nil {
		return nil, fmt.Errorf("querying foreign keys: %w", err)
	}
	defer rows.Close()

	var result []knowledgemap.ForeignKeyInfo
	for rows.Next() {
		var fk knowledgemap.ForeignKeyInfo
		if err := rows.Scan(&fk.SchemaName, &fk.TableName, &fk.ConstraintName, &fk.ColumnName,
			&fk.RefSchema, &fk.RefTable, &fk.RefColumn); err != nil {
			return nil, err
		}
		if !dbCfg.ShouldIncludeSchema(fk.SchemaName) || !dbCfg.ShouldIncludeTable(fk.SchemaName, fk.TableName) {
			continue
		}
		result = append(result, fk)
	}
	return result, rows.Err()
}

func fetchViews(ctx context.Context, pool *pgxpool.Pool, dbCfg config.DatabaseConfig) ([]knowledgemap.ViewInfo, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("beginning views transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT
			v.schemaname AS schema_name,
			v.viewname AS view_name,
			v.definition,
			COALESCE(obj_description((quote_ident(v.schemaname) || '.' || quote_ident(v.viewname))::regclass), '') AS description
		FROM pg_catalog.pg_views v
		WHERE v.schemaname NOT IN ('pg_toast', 'pg_catalog', 'information_schema')
		ORDER BY v.schemaname, v.viewname`)
	if err != nil {
		return nil, fmt.Errorf("querying views: %w", err)
	}
	defer rows.Close()

	var result []knowledgemap.ViewInfo
	for rows.Next() {
		var view knowledgemap.ViewInfo
		if err := rows.Scan(&view.SchemaName, &view.ViewName, &view.Definition, &view.Description); err != nil {
			return nil, err
		}
		if !dbCfg.ShouldIncludeSchema(view.SchemaName) {
			continue
		}
		result = append(result, view)
	}
	return result, rows.Err()
}

func fetchFunctions(ctx context.Context, pool *pgxpool.Pool, dbCfg config.DatabaseConfig) ([]knowledgemap.FunctionInfo, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("beginning functions transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT
			n.nspname AS schema_name,
			p.proname AS function_name,
			pg_get_function_result(p.oid) AS result_type,
			pg_get_function_arguments(p.oid) AS argument_types,
			COALESCE(obj_description(p.oid, 'pg_proc'), '') AS description,
			l.lanname AS language
		FROM pg_catalog.pg_proc p
		JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		JOIN pg_catalog.pg_language l ON l.oid = p.prolang
		WHERE n.nspname NOT IN ('pg_toast', 'pg_catalog', 'information_schema')
		  AND p.prokind IN ('f', 'w')
		ORDER BY n.nspname, p.proname`)
	if err != nil {
		return nil, fmt.Errorf("querying functions: %w", err)
	}
	defer rows.Close()

	var result []knowledgemap.FunctionInfo
	for rows.Next() {
		var fn knowledgemap.FunctionInfo
		if err := rows.Scan(&fn.SchemaName, &fn.FunctionName, &fn.ResultType, &fn.ArgTypes, &fn.Description, &fn.Language); err != nil {
			return nil, err
		}
		if !dbCfg.ShouldIncludeSchema(fn.SchemaName) {
			continue
		}
		result = append(result, fn)
	}
	return result, rows.Err()
}
