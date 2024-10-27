// Package main provides the entry point for the dispatcher service.
package main

func Follow() {
	forever := make(chan bool)
	go receiveMessage("sbom_packageFollower")
	<-forever
}

func main() {
	Follow()
}
