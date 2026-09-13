package testdb

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
)

const EnvDSN = "MARSPAY_TEST_DATABASE_URL"

func New(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()

	baseDSN := os.Getenv(EnvDSN)
	if baseDSN == "" {
		t.Skipf("set %s to run database tests", EnvDSN)
	}

	ctx := context.Background()
	name := "marspay_test_" + strings.ToLower(id.ULID())

	admin, err := pgx.Connect(ctx, withDatabase(t, baseDSN, "postgres"))
	if err != nil {
		t.Fatalf("connect to admin database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create database %s: %v", name, err)
	}
	_ = admin.Close(ctx)

	pool, err := pgxpool.New(ctx, withDatabase(t, baseDSN, name))
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}

	t.Cleanup(func() {
		pool.Close()

		cleanup, err := pgx.Connect(context.Background(), withDatabase(t, baseDSN, "postgres"))
		if err != nil {
			return
		}
		defer func() { _ = cleanup.Close(context.Background()) }()
		_, _ = cleanup.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	migrate(t, ctx, pool)
	return pool, ctx
}

func migrate(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	pattern := filepath.Join(repoRoot(), "migrations", "*.up.sql")
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	if len(files) == 0 {
		t.Fatalf("no migrations found at %s", pattern)
	}
	sort.Strings(files)

	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func withDatabase(t *testing.T, dsn, database string) string {
	t.Helper()

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvDSN, err)
	}
	u.Path = "/" + database
	return u.String()
}
