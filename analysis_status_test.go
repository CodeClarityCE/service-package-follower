package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// recordingConnector captures each statement so writes can be checked without a database.
type recordingConnector struct{ queries *[]string }

func (c recordingConnector) Connect(context.Context) (driver.Conn, error) {
	return recordingConn(c), nil
}
func (recordingConnector) Driver() driver.Driver { return nil }

type recordingConn struct{ queries *[]string }

func (recordingConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (recordingConn) Close() error                        { return nil }
func (recordingConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (c recordingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	*c.queries = append(*c.queries, query)
	return driver.RowsAffected(1), nil
}

func recordingDB(queries *[]string) *bun.DB {
	return bun.NewDB(sql.OpenDB(recordingConnector{queries}), pgdialect.New())
}

func TestFinishingAnUpdateRecordsTheImportAsDone(t *testing.T) {
	var queries []string
	db := recordingDB(&queries)
	defer db.Close()

	finished := time.Date(2026, 11, 27, 9, 30, 0, 0, time.UTC)
	if err := markPackageImportDone(context.Background(), db, uuid.New(), "php", finished); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 {
		t.Fatalf("expected one write, got %v", queries)
	}
	query := queries[0]
	for _, want := range []string{"package_imports_done", "'php'", "2026-11-27T09:30:00Z"} {
		if !strings.Contains(query, want) {
			t.Errorf("missing %s in: %s", want, query)
		}
	}
	if !strings.Contains(query, "CASE WHEN status = 'updating_db' THEN 'ongoing' ELSE status END") {
		t.Errorf("must resume only an analysis still updating: %s", query)
	}
	for _, terminal := range []string{"'completed'", "'failure'", "'cancelled'"} {
		if !strings.Contains(query, terminal) {
			t.Errorf("must spare %s analyses: %s", terminal, query)
		}
	}
	if strings.Contains(query, "steps") {
		t.Errorf("must not rewrite steps: %s", query)
	}
}

func TestAnEmptyLanguageIsJavaScript(t *testing.T) {
	for language, want := range map[string]string{"": "javascript", "javascript": "javascript", "php": "php"} {
		if got := importedLanguage(language); got != want {
			t.Errorf("importedLanguage(%q) = %q, want %q", language, got, want)
		}
	}
}

func TestStartingAnUpdateRecordsWhenItBeganAndSparesFinishedAnalyses(t *testing.T) {
	var queries []string
	db := recordingDB(&queries)
	defer db.Close()

	began := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	if err := markUpdateStarted(context.Background(), db, uuid.New(), began); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 {
		t.Fatalf("expected one write, got %v", queries)
	}
	query := queries[0]
	for _, want := range []string{"'updating_db'", "package_follower_started_on", "2026-11-08T12:00:00Z", "'completed'", "'failure'", "'cancelled'"} {
		if !strings.Contains(query, want) {
			t.Errorf("missing %s in: %s", want, query)
		}
	}
	if strings.Contains(query, "steps") {
		t.Errorf("must not rewrite steps: %s", query)
	}
}
