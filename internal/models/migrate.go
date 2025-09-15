// api/internal/models/migrate.go
package models

import (
	"context"
	"embed"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed models.sql
var schema embed.FS

// Migrate runs database migrations
func Migrate(ctx context.Context, db *pgxpool.Pool) error {
	// Read SQL file
	sqlContent, err := schema.ReadFile("models.sql")
	if err != nil {
		return err
	}

	// Split into individual statements
	statements := strings.Split(string(sqlContent), ";\n")

	// Execute each statement
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}

		if _, err := db.Exec(ctx, stmt); err != nil {
			return err
		}
	}

	return nil
}