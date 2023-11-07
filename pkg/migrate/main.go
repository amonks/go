package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

func Migrate(ctx context.Context, dbPath string, migrationsPath string) error {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}

	q := New(db)

	appliedMigrations, err := q.GetAppliedMigrations(ctx)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	migrationFiles, err := os.ReadDir(migrationsPath)
	if err != nil {
		return err
	}

	for _, m := range migrationFiles {
		if m.IsDir() {
			continue
		}
		name := m.Name()
		bs, err := os.ReadFile(filepath.Join(migrationsPath, name))
		if err != nil {
			return err
		}
		ddl := string(bs)
		if len(appliedMigrations) > 0 {
			nextMigration := appliedMigrations[0]
			if name != nextMigration.MigrationFilename {
				return fmt.Errorf("migration history has diverged from migrations folder")
			}
			if ddl != nextMigration.Ddl {
				return fmt.Errorf("migration history has diverged from migrations folder")
			}
			appliedMigrations = appliedMigrations[1:]
			log.Println("already applied", name)
			continue
		}

		if _, err := db.ExecContext(ctx, "begin;"); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return err
		}
		if err := q.RecordMigration(ctx, RecordMigrationParams{
			MigrationFilename: name,
			AppliedAt:         time.Now().Format("2006-01-02 15:04:05.000"),
			Ddl:               ddl,
		}); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "commit"); err != nil {
			return err
		}
		log.Println("applied", name)
	}
}
