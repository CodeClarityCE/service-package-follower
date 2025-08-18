package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/CodeClarityCE/service-knowledge/src/mirrors/js"
	"github.com/CodeClarityCE/service-knowledge/src/mirrors/php"
	dbhelper "github.com/CodeClarityCE/utility-dbhelper/helper"
	types_amqp "github.com/CodeClarityCE/utility-types/amqp"
	codeclarity "github.com/CodeClarityCE/utility-types/codeclarity_db"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

// receiveMessage receives messages from a RabbitMQ queue and dispatches them for processing.
// It establishes a connection to RabbitMQ, opens a channel, declares a queue, and consumes messages from the queue.
// Each received message is passed to the dispatch function for further processing.
// The function runs indefinitely until interrupted by a signal.
//
// Parameters:
// - connection: The name of the RabbitMQ queue to consume messages from.
//
// Example usage:
// receiveMessage("my_queue")
func receiveMessage(connection string) {
	pg_user := os.Getenv("PG_DB_USER")
	pg_db_password := os.Getenv("PG_DB_PASSWORD")
	pg_db_host := os.Getenv("PG_DB_HOST")
	pg_db_port := os.Getenv("PG_DB_PORT")

	dsn_knowledge := "postgres://" + pg_user + ":" + pg_db_password + "@" + pg_db_host + ":" + pg_db_port + "/" + dbhelper.Config.Database.Knowledge + "?sslmode=disable"
	sqldb_knowledge := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn_knowledge), pgdriver.WithTimeout(120*time.Second)))
	db_knowledge := bun.NewDB(sqldb_knowledge, pgdialect.New())
	defer db_knowledge.Close()

	dsn_codeclarity := "postgres://" + pg_user + ":" + pg_db_password + "@" + pg_db_host + ":" + pg_db_port + "/" + dbhelper.Config.Database.Results + "?sslmode=disable"
	sqldb_codeclarity := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn_codeclarity), pgdriver.WithTimeout(50*time.Second)))
	db_codeclarity := bun.NewDB(sqldb_codeclarity, pgdialect.New())
	defer db_codeclarity.Close()
	// Create connexion
	url := ""
	protocol := os.Getenv("AMQP_PROTOCOL")
	if protocol == "" {
		protocol = "amqp"
	}
	host := os.Getenv("AMQP_HOST")
	if host == "" {
		host = "localhost"
	}
	port := os.Getenv("AMQP_PORT")
	if port == "" {
		port = "5672"
	}
	user := os.Getenv("AMQP_USER")
	if user == "" {
		user = "guest"
	}
	password := os.Getenv("AMQP_PASSWORD")
	if password == "" {
		password = "guest"
	}
	url = protocol + "://" + user + ":" + password + "@" + host + ":" + port + "/"

	conn, err := amqp.Dial(url)
	if err != nil {
		failOnError(err, "Failed to connect to RabbitMQ")
	}
	defer conn.Close()

	// Open channel
	ch, err := conn.Channel()
	if err != nil {
		failOnError(err, "Failed to open a channel")
	}
	defer ch.Close()

	// Declare queue
	q, err := ch.QueueDeclare(
		connection, // name
		true,       // durable
		false,      // delete when unused
		false,      // exclusive
		false,      // no-wait
		nil,        // arguments
	)
	if err != nil {
		failOnError(err, "Failed to declare a queue")
	}

	// Consume messages
	msgs, err := ch.Consume(
		q.Name, // queue
		"",     // consumer
		true,   // auto-ack
		false,  // exclusive
		false,  // no-local
		false,  // no-wait
		nil,    // args
	)
	if err != nil {
		failOnError(err, "Failed to register a consumer")
	}

	var forever = make(chan struct{}, 5)
	go func() {
		for d := range msgs {
			// Start timer
			start := time.Now()

			var apiMessage types_amqp.SbomPackageFollowerMessage
			json.Unmarshal([]byte(d.Body), &apiMessage)

			// Update analysis status to UPDATING_DB
			analysis_document := codeclarity.Analysis{
				Id: apiMessage.AnalysisId,
			}

			err := db_codeclarity.RunInTx(context.Background(), &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
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
				err := php.ImportListWithBatching(db_knowledge, apiMessage.PackagesNames)
				if err != nil {
					log.Printf("⚠️  PHP batch import failed, using individual processing: %v", err)
					php.ImportList(db_knowledge, apiMessage.PackagesNames)
				} else {
					log.Printf("✅ PHP batch import completed successfully")
				}
			case "javascript", "":
				// Default to JavaScript for backward compatibility (empty language field)
				err := js.ImportListWithBatching(db_knowledge, apiMessage.PackagesNames)
				if err != nil {
					log.Printf("⚠️  JavaScript batch import failed, using individual processing: %v", err)
					js.ImportList(db_knowledge, apiMessage.PackagesNames)
				} else {
					log.Printf("✅ JavaScript batch import completed successfully")
				}
			default:
				log.Printf("❌ Unknown language: %s, skipping package import", apiMessage.Language)
			}

			err = db_codeclarity.RunInTx(context.Background(), &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
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
	}()

	log.Printf("%s", " [*] PACKAGE FOLLOWER Waiting for messages on "+connection+". To exit press CTRL+C")
	<-forever
}

func failOnError(err error, msg string) {
	if err != nil {
		log.Panicf("%s: %s", msg, err)
	}
}
