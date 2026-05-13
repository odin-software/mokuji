// Package migrations aplica migraciones SQL al startup del binario.
//
// El runner es deliberadamente simple (ADR 0009): registra cada archivo
// aplicado en la tabla schema_migrations y aplica los pendientes en orden
// alfabético dentro de su propia transacción. Solo soporta migraciones up.
package migrations

import (
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// Run aplica todas las migraciones .sql encontradas en src que aún no estén
// registradas en schema_migrations. Cada migración corre en su propia
// transacción. Retorna la cantidad aplicada en esta llamada.
func Run(db *sql.DB, src fs.FS) (int, error) {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT    PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		return 0, fmt.Errorf("crear schema_migrations: %w", err)
	}

	done, err := applied(db)
	if err != nil {
		return 0, fmt.Errorf("leer migraciones aplicadas: %w", err)
	}

	pending, err := pendingFiles(src, done)
	if err != nil {
		return 0, fmt.Errorf("listar migraciones pendientes: %w", err)
	}

	count := 0
	for _, name := range pending {
		if err := apply(db, src, name); err != nil {
			return count, fmt.Errorf("aplicar %s: %w", name, err)
		}
		count++
	}
	return count, nil
}

func applied(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

func pendingFiles(src fs.FS, done map[string]bool) ([]string, error) {
	entries, err := fs.ReadDir(src, ".")
	if err != nil {
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if done[e.Name()] {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func apply(db *sql.DB, src fs.FS, name string) error {
	content, err := fs.ReadFile(src, name)
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(string(content)); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		name, time.Now().Unix(),
	); err != nil {
		return err
	}
	return tx.Commit()
}
