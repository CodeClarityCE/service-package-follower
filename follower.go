// Package main provides the entry point for the packageFollower service.
package main

import (
	"log"

	"github.com/CodeClarityCE/utility-types/boilerplates"
	amqp "github.com/rabbitmq/amqp091-go"
)

// PackageFollowerService wraps the ServiceBase with packageFollower-specific functionality
type PackageFollowerService struct {
	*boilerplates.ServiceBase
}

// CreatePackageFollowerService creates a new PackageFollowerService
func CreatePackageFollowerService() (*PackageFollowerService, error) {
	base, err := boilerplates.CreateServiceBase()
	if err != nil {
		return nil, err
	}

	service := &PackageFollowerService{
		ServiceBase: base,
	}

	// Setup queue handler
	service.AddQueue("sbom_packageFollower", true, service.handleMessage)

	return service, nil
}

// handleMessage handles messages from sbom_packageFollower queue
func (s *PackageFollowerService) handleMessage(d amqp.Delivery) {
	dispatch(s.DB, d)
}

func Follow() {
	service, err := CreatePackageFollowerService()
	if err != nil {
		log.Fatalf("Failed to create packageFollower service: %v", err)
	}
	defer service.Close()

	log.Printf("Starting PackageFollower Service...")
	if err := service.StartListening(); err != nil {
		log.Fatalf("Failed to start listening: %v", err)
	}

	log.Printf("PackageFollower Service started")
	service.WaitForever()
}

func main() {
	Follow()
}
