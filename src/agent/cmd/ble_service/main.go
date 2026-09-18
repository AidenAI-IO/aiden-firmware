package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aiden-agent/internal/ble"
	"aiden-agent/internal/logging"
)

func main() {
	socketPath := flag.String("socket", "/run/ble_service/ble_service.sock", "Unix domain socket path")
	deviceName := flag.String("device-name", "Aiden", "BLE advertised device name")
	eventCapacity := flag.Int("event-capacity", 512, "maximum in-memory ANCS events")
	pairingWindow := flag.Duration("pairing-window", 5*time.Minute, "first-device BLE pairing window")
	flag.Parse()
	if *eventCapacity <= 0 {
		logging.Fatalf("ble_service", "startup", "event-capacity must be positive")
	}
	if *pairingWindow <= 0 {
		logging.Fatalf("ble_service", "startup", "pairing-window must be positive")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	service := ble.NewService(*eventCapacity)
	uds := ble.NewUDSServer(*socketPath, service)
	if err := uds.Start(); err != nil {
		logging.Fatalf("ble_service", "startup", "start BLE UDS service: %v", err)
	}
	defer uds.Close()
	logging.Infof("ble_service", "startup", "ble_service listening on %s", *socketPath)

	for ctx.Err() == nil {
		err := service.RunBlueZ(ctx, *deviceName, *pairingWindow)
		if ctx.Err() != nil {
			break
		}
		logging.Warnf("ble_service", "main", "BlueZ backend stopped: %v", err)
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
	logging.Infof("ble_service", "main", "ble_service stopped")
}
