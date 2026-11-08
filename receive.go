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
		Where("status NOT IN (?)", bun.In(terminal)).
		Exec(ctx)
	return err
}

// markUpdateFinished moves the analysis back to ongoing, only if still updating.
// A single UPDATE locks the row and leaves every other column alone.
func markUpdateFinished(ctx context.Context, db bun.IDB, analysisId uuid.UUID) error {
	_, err := db.NewUpdate().Model((*codeclarity.Analysis)(nil)).
		Set("status = ?", codeclarity.ONGOING).
		Where("id = ?", analysisId).
		Where("status = ?", codeclarity.UPDATING_DB).
		Exec(ctx)
	return err
}

// dispatch handles package following messages using ServiceBase databases
func dispatch(db *boilerplates.ServiceDatabases, d amqp.Delivery) {
	start := time.Now()

	var apiMessage types_amqp.SbomPackageFollowerMessage
	json.Unmarshal([]byte(d.Body), &apiMessage)

	var err error

	if err = markUpdateStarted(context.Background(), db.CodeClarity, apiMessage.AnalysisId, time.Now()); err != nil {
		log.Printf("%v", err)
	}

	// Start the update based on language with batch processing
	log.Printf("📦 PACKAGE FOLLOWER: Processing %d %s packages for analysis %s",
		len(apiMessage.PackagesNames), apiMessage.Language, apiMessage.AnalysisId.String()[:8])

	switch apiMessage.Language {
	case "php":
		err := php.ImportListWithBatching(db.Knowledge, apiMessage.PackagesNames)
		if err != nil {
			log.Printf("⚠️  PHP batch import failed, using individual processing: %v", err)
			php.ImportList(db.Knowledge, apiMessage.PackagesNames)
		} else {
			log.Printf("✅ PHP batch import completed successfully")
		}
	case "javascript", "":
		// Default to JavaScript for backward compatibility (empty language field)
		err := js.ImportListWithBatching(db.Knowledge, apiMessage.PackagesNames)
		if err != nil {
			log.Printf("⚠️  JavaScript batch import failed, using individual processing: %v", err)
			js.ImportList(db.Knowledge, apiMessage.PackagesNames)
		} else {
			log.Printf("✅ JavaScript batch import completed successfully")
		}
	default:
		log.Printf("❌ Unknown language: %s, skipping package import", apiMessage.Language)
	}

	if err = markUpdateFinished(context.Background(), db.CodeClarity, apiMessage.AnalysisId); err != nil {
		log.Printf("%v", err)
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
