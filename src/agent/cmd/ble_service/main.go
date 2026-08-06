package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aiden-agent/internal/ble"
)

func main() {
	socketPath := flag.String("socket", "/run/ble_service/ble_service.sock", "Unix domain socket path")
	deviceName := flag.String("device-name", "Aiden", "BLE advertised device name")
	eventCapacity := flag.Int("event-capacity", 512, "maximum in-memory ANCS events")
	flag.Parse()
	if *eventCapacity <= 0 {
		log.Fatalf("event-capacity must be positive")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	service := ble.NewService(*eventCapacity)
	uds := ble.NewUDSServer(*socketPath, service)
	if err := uds.Start(); err != nil {
		log.Fatalf("start BLE UDS service: %v", err)
	}
	defer uds.Close()
	log.Printf("ble_service listening on %s", *socketPath)

	for ctx.Err() == nil {
		err := service.RunBlueZ(ctx, *deviceName)
		if ctx.Err() != nil {
			break
		}
		log.Printf("BlueZ backend stopped: %v", err)
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
	log.Printf("ble_service stopped")
}
