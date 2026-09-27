// Package db opens the SQLite database through GORM and owns the schema.
package db

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open opens (or creates) the database at path, applies pragmas and migrates the schema.
func Open(path string) (*gorm.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	// WAL lets readers run while one writer writes; _txlock=immediate takes the write lock at
	// BEGIN so two write transactions queue on busy_timeout instead of failing mid-transaction.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	g, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.New(log.New(os.Stderr, "", log.LstdFlags), logger.Config{
			SlowThreshold: time.Second, LogLevel: logger.Warn, IgnoreRecordNotFoundError: true}),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	sqlDB, err := g.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(4)
	if err := Migrate(g); err != nil {
		return nil, err
	}
	return g, nil
}

// Migrate brings the schema up to date: AutoMigrate for additive changes, then the hand-written
// steps in migrations for renames and data fixes (each runs once, recorded in schema_migrations).
func Migrate(g *gorm.DB) error {
	if err := g.AutoMigrate(models...); err != nil {
		return fmt.Errorf("automigrate: %w", err)
	}
	for _, m := range migrations {
		var n int64
		g.Model(&SchemaMigration{}).Where("id = ?", m.ID).Count(&n)
		if n > 0 {
			continue
		}
		if err := g.Transaction(func(tx *gorm.DB) error {
			if err := m.Up(tx); err != nil {
				return err
			}
			return tx.Create(&SchemaMigration{ID: m.ID, AppliedAt: time.Now()}).Error
		}); err != nil {
			return fmt.Errorf("migration %s: %w", m.ID, err)
		}
		slog.Info("applied migration", "id", m.ID)
	}
	return nil
}

// Snapshot writes a consistent copy of the live database to dst (VACUUM INTO), replacing any
// earlier copy. Copying the .db file of a WAL database directly can produce a torn copy.
func Snapshot(ctx context.Context, g *gorm.DB, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	if err := g.WithContext(ctx).Exec("VACUUM INTO ?", tmp).Error; err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}
	return os.Rename(tmp, dst)
}

// RunBackups snapshots the database every interval until ctx ends.
func RunBackups(ctx context.Context, g *gorm.DB, dst string, every time.Duration) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := Snapshot(ctx, g, dst); err != nil && ctx.Err() == nil {
			slog.Error("database snapshot failed", "err", err)
		} else if err == nil {
			slog.Info("database snapshot written", "path", dst)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// IsUniqueViolation reports whether err is a unique-constraint failure.
func IsUniqueViolation(err error) bool {
	return err != nil && (err == gorm.ErrDuplicatedKey || strings.Contains(err.Error(), "UNIQUE constraint failed"))
}
