package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/CodeClarityCE/service-knowledge/src/mirrors/js"
	"github.com/CodeClarityCE/service-knowledge/src/mirrors/php"
	amqp_helper "github.com/CodeClarityCE/utility-amqp-helper"
	"github.com/CodeClarityCE/utility-boilerplates"
	types_amqp "github.com/CodeClarityCE/utility-types/amqp"
	codeclarity "github.com/CodeClarityCE/utility-types/codeclarity_db"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"
	"github.com/uptrace/bun"
)

// updateStartedKey is the Config key the dispatcher's reaper reads to time a stuck update.
const updateStartedKey = "package_follower_started_on"

// markUpdateStarted sets updating_db and records when the update began.
// A single UPDATE locks the row, so an analysis that already ended stays ended.
func markUpdateStarted(ctx context.Context, db bun.IDB, analysisId uuid.UUID, began time.Time) error {
	terminal := []codeclarity.AnalysisStatus{codeclarity.COMPLETED, codeclarity.FAILURE, codeclarity.CANCELLED}
	_, err := db.NewUpdate().Model((*codeclarity.Analysis)(nil)).
		Set("status = ?", codeclarity.UPDATING_DB).
		Set("config = jsonb_set(COALESCE(config, '{}'::jsonb), '{"+updateStartedKey+"}', to_jsonb(?::text))", began.Format(time.RFC3339Nano)).
		Where("id = ?", analysisId).
		Where("status NOT IN (?)", bun.List(terminal)).
		Exec(ctx)
	return err
}

// packageImportDoneKey is the Config key the dispatcher reads, per language, before it
// starts the plugins that depend on an SBOM.
const packageImportDoneKey = "package_imports_done"

// importedLanguage names the language of an import; an empty one is JavaScript.
func importedLanguage(language string) string {
	if language == "" {
		return "javascript"
	}
	return language
}

// markPackageImportDone records the language's import as done and resumes an updating analysis.
// One UPDATE locks the row, so an analysis that already ended stays ended.
func markPackageImportDone(ctx context.Context, db bun.IDB, analysisId uuid.UUID, language string, finished time.Time) error {
	terminal := []codeclarity.AnalysisStatus{codeclarity.COMPLETED, codeclarity.FAILURE, codeclarity.CANCELLED}
	_, err := db.NewUpdate().Model((*codeclarity.Analysis)(nil)).
		Set("status = CASE WHEN status = ? THEN ? ELSE status END", codeclarity.UPDATING_DB, codeclarity.ONGOING).
		Set("config = jsonb_set(COALESCE(config, '{}'::jsonb), '{"+packageImportDoneKey+"}', COALESCE(config->'"+packageImportDoneKey+"', '{}'::jsonb) || jsonb_build_object(?::text, ?::text))", language, finished.Format(time.RFC3339Nano)).
		Where("id = ?", analysisId).
		Where("status NOT IN (?)", bun.List(terminal)).
		Exec(ctx)
	return err
}

// importer fetches a language's packages into the knowledge database and returns what it could not store.
type importer func(*bun.DB, []string) error

// registryImporters are the languages whose registries the follower imports from.
var registryImporters = map[string]importer{
	"javascript": js.ImportListWithBatching,
	"php":        php.ImportListWithBatching,
}

// importPackages imports the packages of an analysis.
// A failure only logs: the analysis carries on with the rows that exist.
func importPackages(db *bun.DB, importers map[string]importer, language string, names []string, analysisId uuid.UUID) {
	language = importedLanguage(language)
	logger := logrus.WithFields(logrus.Fields{
		"analysis_id": analysisId.String(),
		"language":    language,
		"packages":    len(names),
	})
	importList, ok := importers[language]
	if !ok {
		logger.Warn("Skipping the package import of an unknown language")
		return
	}
	logger.Info("Importing package metadata")
	if err := importList(db, names); err != nil {
		logger.WithError(err).Error("Package metadata import failed")
		return
	}
	logger.Info("Package metadata imported")
}

// dispatch handles package following messages using ServiceBase databases
func dispatch(db *boilerplates.ServiceDatabases, d amqp.Delivery) {
	start := time.Now()

	var apiMessage types_amqp.SbomPackageFollowerMessage
	if err := json.Unmarshal(d.Body, &apiMessage); err != nil {
		logrus.WithError(err).Warn("Dropping a malformed package follower message")
		return
	}

	var err error

	if err = markUpdateStarted(context.Background(), db.CodeClarity, apiMessage.AnalysisId, time.Now()); err != nil {
		logrus.WithError(err).WithField("analysis_id", apiMessage.AnalysisId.String()).Error("Could not record the package import start")
	}

	importPackages(db.Knowledge, registryImporters, apiMessage.Language, apiMessage.PackagesNames, apiMessage.AnalysisId)

	// The dispatcher starts the finders only once this is recorded, so record it after the import.
	if err = markPackageImportDone(context.Background(), db.CodeClarity, apiMessage.AnalysisId, importedLanguage(apiMessage.Language), time.Now()); err != nil {
		logrus.WithError(err).WithField("analysis_id", apiMessage.AnalysisId.String()).Error("Could not record the package import as done")
	}

	// Notify dispatcher that the knowledge DB update is complete.
	// This re-triggers the plugins_dispatcher handler so the dispatcher
	// can advance to the next stage without polling.
	notification := types_amqp.PluginDispatcherMessage{
		AnalysisId: apiMessage.AnalysisId,
		Plugin:     "packageFollower",
	}
	notifData, err := json.Marshal(notification)
	if err != nil {
		log.Printf("Failed to marshal dispatcher notification: %v", err)
	} else {
		amqp_helper.Send("plugins_dispatcher", notifData)
	}

	// Print time elapsed and completion status
	elapsed := time.Since(start)
	log.Printf("🏁 PACKAGE FOLLOWER: Completed processing for analysis %s in %v",
		apiMessage.AnalysisId.String()[:8], elapsed)
}
