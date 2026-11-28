package main

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/uptrace/bun"
)

func failing(err error) importer {
	return func(*bun.DB, []string) error { return err }
}

func succeeding(called *[]string) importer {
	return func(_ *bun.DB, names []string) error {
		*called = append(*called, names...)
		return nil
	}
}

// levelEntries returns the logged entries at level.
func levelEntries(hook *test.Hook, level logrus.Level) []*logrus.Entry {
	var found []*logrus.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Level == level {
			found = append(found, entry)
		}
	}
	return found
}

func TestAFailedImportIsLoggedAsAnErrorWithTheAnalysis(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()
	analysis := uuid.New()
	importers := map[string]importer{
		"php": failing(errors.New("1 of 1 PHP packages were not imported, the first: permission denied for table package")),
	}

	importPackages(nil, importers, "php", []string{"monolog/monolog"}, analysis)

	logged := levelEntries(hook, logrus.ErrorLevel)
	if len(logged) != 1 {
		t.Fatalf("expected one error, got %v", hook.AllEntries())
	}
	if logged[0].Data["analysis_id"] != analysis.String() || logged[0].Data["language"] != "php" || logged[0].Data["packages"] != 1 {
		t.Errorf("error must name the analysis, language and package count: %v", logged[0].Data)
	}
	if err, _ := logged[0].Data[logrus.ErrorKey].(error); err == nil || err.Error() != "1 of 1 PHP packages were not imported, the first: permission denied for table package" {
		t.Errorf("error must carry the cause: %v", logged[0].Data)
	}
}

func TestAnEmptyLanguageImportsJavaScriptWithoutAnError(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()
	var imported []string
	importers := map[string]importer{"javascript": succeeding(&imported)}

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
