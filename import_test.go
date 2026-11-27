package main

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/uptrace/bun"
)

func failing(err error) func(*bun.DB, []string) error {
	return func(*bun.DB, []string) error { return err }
}

func succeeding(called *[]string) func(*bun.DB, []string) error {
	return func(_ *bun.DB, names []string) error {
		*called = append(*called, names...)
		return nil
	}
}

func TestAFailedBatchImportIsLoggedAsAnErrorWithTheAnalysis(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()
	analysis := uuid.New()
	var retried []string
	importers := map[string]importer{
		"javascript": {batch: failing(errors.New("permission denied for table package")), each: succeeding(&retried)},
	}

	importPackages(nil, importers, "javascript", []string{"left-pad"}, analysis)

	var found *logrus.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.ErrorLevel {
			found = entry
		}
	}
	if found == nil {
		t.Fatalf("no error logged, got %v", hook.AllEntries())
	}
	if found.Data["analysis_id"] != analysis.String() || found.Data["language"] != "javascript" {
		t.Errorf("error must name the analysis and language: %v", found.Data)
	}
	if err, _ := found.Data[logrus.ErrorKey].(error); err == nil || err.Error() != "permission denied for table package" {
		t.Errorf("error must carry the cause: %v", found.Data)
	}
	if len(retried) != 1 {
		t.Errorf("must retry the packages one at a time, got %v", retried)
	}
}

func TestAFailedRetryIsLoggedAsAnErrorWithTheAnalysis(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()
	analysis := uuid.New()
	importers := map[string]importer{
		"php": {batch: failing(errors.New("batch broke")), each: failing(errors.New("retry broke"))},
	}

	importPackages(nil, importers, "php", []string{"monolog/monolog"}, analysis)

	errorsLogged := 0
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.ErrorLevel && entry.Data["analysis_id"] == analysis.String() {
			errorsLogged++
		}
	}
	if errorsLogged != 2 {
		t.Errorf("expected the batch and the retry failures as errors, got %v", hook.AllEntries())
	}
}

func TestAnEmptyLanguageImportsJavaScriptWithoutAnError(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()
	var imported []string
	importers := map[string]importer{
		"javascript": {batch: succeeding(&imported), each: failing(errors.New("not called"))},
	}

	importPackages(nil, importers, "", []string{"ms"}, uuid.New())

	if len(imported) != 1 {
		t.Errorf("expected ms imported, got %v", imported)
	}
	for _, entry := range hook.AllEntries() {
		if entry.Level <= logrus.WarnLevel {
			t.Errorf("unexpected %s: %s", entry.Level, entry.Message)
		}
	}
}

func TestAnUnknownLanguageIsLoggedWithTheAnalysis(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()
	analysis := uuid.New()

	importPackages(nil, map[string]importer{}, "rust", []string{"serde"}, analysis)

	entry := hook.LastEntry()
	if entry == nil || entry.Level != logrus.WarnLevel || entry.Data["analysis_id"] != analysis.String() {
		t.Errorf("expected a warning naming the analysis, got %v", hook.AllEntries())
	}
}
