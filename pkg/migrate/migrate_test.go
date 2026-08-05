package migrate_test

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
	"monks.co/pkg/migrate"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func makeFS(files map[string]string) fs.FS {
	m := fstest.MapFS{}
	for name, content := range files {
		m[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}

func TestFreshDatabase(t *testing.T) {
	db := openTestDB(t)
	migrations := makeFS(map[string]string{
		"migrations/001_create_users.sql":  "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
		"migrations/002_create_posts.sql":  "CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER, body TEXT);",
	})

	err := migrate.Run(context.Background(), migrate.Config{
		DB: db, FS: migrations, Dir: "migrations",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify tables were created
	var count int
	if err := db.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil {
		t.Fatal("users table should exist:", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM posts").Scan(&count); err != nil {
		t.Fatal("posts table should exist:", err)
	}
}

func TestIdempotent(t *testing.T) {
	db := openTestDB(t)
	migrations := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
	})

	cfg := migrate.Config{DB: db, FS: migrations, Dir: "migrations"}
	if err := migrate.Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	// Second run should be a no-op
	if err := migrate.Run(context.Background(), cfg); err != nil {
		t.Fatal("second run should be no-op:", err)
	}
}

func TestIncremental(t *testing.T) {
	db := openTestDB(t)

	// First run with one migration
	fs1 := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
	})
	if err := migrate.Run(context.Background(), migrate.Config{DB: db, FS: fs1, Dir: "migrations"}); err != nil {
		t.Fatal(err)
	}

	// Second run with an additional migration
	fs2 := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
		"migrations/002_create_posts.sql": "CREATE TABLE posts (id INTEGER PRIMARY KEY, body TEXT);",
	})
	if err := migrate.Run(context.Background(), migrate.Config{DB: db, FS: fs2, Dir: "migrations"}); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM posts").Scan(&count); err != nil {
		t.Fatal("posts table should exist:", err)
	}
}

func TestDrift(t *testing.T) {
	db := openTestDB(t)

	fs1 := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
	})
	if err := migrate.Run(context.Background(), migrate.Config{DB: db, FS: fs1, Dir: "migrations"}); err != nil {
		t.Fatal(err)
	}

	// Change the content of the applied migration
	fs2 := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT);",
	})
	err := migrate.Run(context.Background(), migrate.Config{DB: db, FS: fs2, Dir: "migrations"})
	if err == nil {
		t.Fatal("expected error for diverged migration")
	}
}

func TestBaselineExistingDB(t *testing.T) {
	db := openTestDB(t)

	// Simulate an existing database with a table already present
	if _, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);"); err != nil {
		t.Fatal(err)
	}

	migrations := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
		"migrations/002_create_posts.sql": "CREATE TABLE posts (id INTEGER PRIMARY KEY, body TEXT);",
	})

	err := migrate.Run(context.Background(), migrate.Config{
		DB:       db,
		FS:       migrations,
		Dir:      "migrations",
		Baseline: []string{"001_create_users.sql"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// posts table should have been created (not baselined)
	var count int
	if err := db.QueryRow("SELECT count(*) FROM posts").Scan(&count); err != nil {
		t.Fatal("posts table should exist:", err)
	}
}

func TestBaselineFreshDB(t *testing.T) {
	db := openTestDB(t)

	migrations := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
		"migrations/002_create_posts.sql": "CREATE TABLE posts (id INTEGER PRIMARY KEY, body TEXT);",
	})

	err := migrate.Run(context.Background(), migrate.Config{
		DB:       db,
		FS:       migrations,
		Dir:      "migrations",
		Baseline: []string{"001_create_users.sql"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// On a fresh DB, baseline files should execute normally
	var count int
	if err := db.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil {
		t.Fatal("users table should exist on fresh DB:", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM posts").Scan(&count); err != nil {
		t.Fatal("posts table should exist on fresh DB:", err)
	}
}

// A replicated database that has never held app data still carries
// litestream's bookkeeping tables and pkg/database's heartbeat. Those must
// not make it look like an existing database, or baseline migrations get
// recorded without executing and the app's tables are never created.
func TestInfrastructureTablesDoNotCountAsExisting(t *testing.T) {
	db := openTestDB(t)

	for _, ddl := range []string{
		"CREATE TABLE _litestream_seq (id INTEGER PRIMARY KEY, seq INTEGER);",
		"CREATE TABLE _litestream_lock (id INTEGER);",
		"CREATE TABLE _monks_replication_heartbeat (id INTEGER PRIMARY KEY, at TEXT);",
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}

	migrations := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
	})

	err := migrate.Run(context.Background(), migrate.Config{
		DB:       db,
		FS:       migrations,
		Dir:      "migrations",
		Baseline: []string{"001_create_users.sql"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil {
		t.Fatal("users table should exist:", err)
	}
}

// Infrastructure tables alongside real app tables still mean an existing
// database — the baseline must not re-execute over live data.
func TestInfrastructureTablesAlongsideAppTables(t *testing.T) {
	db := openTestDB(t)

	for _, ddl := range []string{
		"CREATE TABLE _litestream_seq (id INTEGER PRIMARY KEY, seq INTEGER);",
		"CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}

	migrations := makeFS(map[string]string{
		// Would fail if executed: users already exists.
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
	})

	err := migrate.Run(context.Background(), migrate.Config{
		DB:       db,
		FS:       migrations,
		Dir:      "migrations",
		Baseline: []string{"001_create_users.sql"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeletedMigration(t *testing.T) {
	db := openTestDB(t)

	fs1 := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
		"migrations/002_create_posts.sql": "CREATE TABLE posts (id INTEGER PRIMARY KEY, body TEXT);",
	})
	if err := migrate.Run(context.Background(), migrate.Config{DB: db, FS: fs1, Dir: "migrations"}); err != nil {
		t.Fatal(err)
	}

	// Remove a migration from disk
	fs2 := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
	})
	err := migrate.Run(context.Background(), migrate.Config{DB: db, FS: fs2, Dir: "migrations"})
	if err == nil {
		t.Fatal("expected error for deleted migration")
	}
}

func TestNonSQLFilesIgnored(t *testing.T) {
	db := openTestDB(t)
	migrations := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
		"migrations/README.md":            "# Migrations",
		"migrations/.gitkeep":             "",
	})

	err := migrate.Run(context.Background(), migrate.Config{
		DB: db, FS: migrations, Dir: "migrations",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify only the SQL migration was recorded
	var count int
	if err := db.QueryRow("SELECT count(*) FROM applied_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 applied migration, got %d", count)
	}
}

func TestTransactionRollbackOnBadSQL(t *testing.T) {
	db := openTestDB(t)
	migrations := makeFS(map[string]string{
		"migrations/001_create_users.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
		"migrations/002_bad.sql":          "THIS IS NOT VALID SQL;",
	})

	err := migrate.Run(context.Background(), migrate.Config{
		DB: db, FS: migrations, Dir: "migrations",
	})
	if err == nil {
		t.Fatal("expected error for bad SQL")
	}

	// First migration should have been applied (committed before the bad one)
	var count int
	if err := db.QueryRow("SELECT count(*) FROM applied_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 applied migration (the good one), got %d", count)
	}

	// The bad migration should NOT be recorded
	var name string
	if err := db.QueryRow("SELECT migration_filename FROM applied_migrations").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "001_create_users.sql" {
		t.Fatalf("expected 001_create_users.sql, got %s", name)
	}
}
