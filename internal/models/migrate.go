// api/internal/models/migrate.go
package models

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate runs database migrations
func Migrate(ctx context.Context, db *pgxpool.Pool) error {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		content, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		statements := strings.Split(string(content), ";")
		for _, stmt := range statements {
			statement := strings.TrimSpace(stmt)
			if statement == "" {
				continue
			}

			if _, err := db.Exec(ctx, statement); err != nil {
				return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
			}
		}
	}

	return nil
}
