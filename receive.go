package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"time"

	"github.com/CodeClarityCE/service-knowledge/src/mirrors/js"
	"github.com/CodeClarityCE/service-knowledge/src/mirrors/php"
	"github.com/CodeClarityCE/utility-boilerplates"
	types_amqp "github.com/CodeClarityCE/utility-types/amqp"
	codeclarity "github.com/CodeClarityCE/utility-types/codeclarity_db"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/uptrace/bun"
)

// dispatch handles package following messages using ServiceBase databases
func dispatch(db *boilerplates.ServiceDatabases, d amqp.Delivery) {
	start := time.Now()

	var apiMessage types_amqp.SbomPackageFollowerMessage
	json.Unmarshal([]byte(d.Body), &apiMessage)

	var err error

	// Update analysis status to UPDATING_DB
	analysis_document := codeclarity.Analysis{
		Id: apiMessage.AnalysisId,
	}

	err = db.CodeClarity.RunInTx(context.Background(), &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		// Retrieve analysis document
		err := tx.NewSelect().Model(&analysis_document).WherePK().Scan(ctx)
		if err != nil {
			return err
		}
		analysis_document.Status = codeclarity.UPDATING_DB
		// Update analysis document
		_, err = tx.NewUpdate().Model(&analysis_document).WherePK().Exec(ctx)
		return err
	})
	if err != nil {
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

	err = db.CodeClarity.RunInTx(context.Background(), &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		// Retrieve analysis document
		err := tx.NewSelect().Model(&analysis_document).WherePK().Scan(ctx)
		if err != nil {
			return err
		}
		analysis_document.Status = codeclarity.ONGOING
		// Update analysis document
		_, err = tx.NewUpdate().Model(&analysis_document).WherePK().Exec(ctx)
		return err
	})
	if err != nil {
		log.Printf("%v", err)
	}

	// Print time elapsed and completion status
	elapsed := time.Since(start)
	log.Printf("🏁 PACKAGE FOLLOWER: Completed processing for analysis %s in %v",
		apiMessage.AnalysisId.String()[:8], elapsed)
}
