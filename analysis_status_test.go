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

func TestFinishingAnUpdateWritesOnlyStatusWhileStillUpdating(t *testing.T) {
	var queries []string
	db := recordingDB(&queries)
	defer db.Close()

	if err := markUpdateFinished(context.Background(), db, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 {
		t.Fatalf("expected one write, got %v", queries)
	}
	query := queries[0]
	if !strings.Contains(query, `SET status = 'ongoing'`) {
		t.Errorf("does not set the status: %s", query)
	}
	if strings.Contains(query, "steps") || strings.Contains(query, "config") {
		t.Errorf("must write only the status: %s", query)
	}
	if !strings.Contains(query, "status = 'updating_db'") {
		t.Errorf("must only apply while still updating: %s", query)
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
