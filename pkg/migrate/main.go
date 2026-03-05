package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

type Config struct {
	DB       *sql.DB
	FS       fs.FS
	Dir      string   // directory within FS, e.g. "migrations"
	Baseline []string // filenames to record-but-not-execute on existing databases
}

func Run(ctx context.Context, cfg Config) error {
	isExisting, err := initTrackingTable(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("migrate: init tracking table: %w", err)
	}

	baselineSet := make(map[string]bool, len(cfg.Baseline))
	for _, b := range cfg.Baseline {
		baselineSet[b] = true
	}

	applied, err := loadApplied(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("migrate: load applied: %w", err)
	}

	files, err := loadMigrationFiles(cfg.FS, cfg.Dir)
	if err != nil {
		return fmt.Errorf("migrate: load files: %w", err)
	}

	// Build set of filenames on disk for deleted-migration check
	onDisk := make(map[string]bool, len(files))
	for _, f := range files {
		onDisk[f.name] = true
	}
	for name := range applied {
		if !onDisk[name] {
			return fmt.Errorf("migrate: migration %q was previously applied but no longer exists on disk", name)
		}
	}

	for _, f := range files {
		if prev, ok := applied[f.name]; ok {
			if prev != f.content {
				return fmt.Errorf("migrate: migration %q has diverged (content changed after being applied)", f.name)
			}
			continue
		}

		// Baseline: on existing databases, record baseline files without executing
		if isExisting && baselineSet[f.name] {
			if err := recordMigration(ctx, cfg.DB, f.name, f.content); err != nil {
				return fmt.Errorf("migrate: record baseline %q: %w", f.name, err)
			}
			continue
		}

		if err := applyMigration(ctx, cfg.DB, f.name, f.content); err != nil {
			return fmt.Errorf("migrate: apply %q: %w", f.name, err)
		}
	}

	return nil
}

type migrationFile struct {
	name    string
	content string
}

func loadMigrationFiles(fsys fs.FS, dir string) ([]migrationFile, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}

	var files []migrationFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		data, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		files = append(files, migrationFile{name: e.Name(), content: string(data)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

// initTrackingTable ensures applied_migrations exists. Returns true if this
// is an existing database (has user tables already).
func initTrackingTable(ctx context.Context, db *sql.DB) (isExisting bool, err error) {
	var count int
	err = db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='applied_migrations'`,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil // table already exists, not first run
	}

	// Check if there are other user tables (existing database)
	err = db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	isExisting = count > 0

	_, err = db.ExecContext(ctx, `CREATE TABLE applied_migrations (
		migration_filename TEXT NOT NULL PRIMARY KEY,
		applied_at TEXT NOT NULL,
		ddl TEXT NOT NULL
	)`)
	if err != nil {
		return false, err
	}

	return isExisting, nil
}

func loadApplied(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT migration_filename, ddl FROM applied_migrations ORDER BY migration_filename`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[string]string)
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			return nil, err
		}
		applied[name] = ddl
	}
	return applied, rows.Err()
}

func applyMigration(ctx context.Context, db *sql.DB, name, ddl string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO applied_migrations (migration_filename, applied_at, ddl) VALUES (?, ?, ?)`,
		name, time.Now().UTC().Format("2006-01-02 15:04:05.000"), ddl,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func recordMigration(ctx context.Context, db *sql.DB, name, ddl string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO applied_migrations (migration_filename, applied_at, ddl) VALUES (?, ?, ?)`,
		name, time.Now().UTC().Format("2006-01-02 15:04:05.000"), ddl,
	)
	return err
}
