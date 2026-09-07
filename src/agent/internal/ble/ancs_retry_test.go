package ble

import (
	"errors"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestANCSRetryBackoffAndBudget(t *testing.T) {
	now := time.Unix(100, 0)
	var retry ancsRetryState
	device := dbus.ObjectPath("/org/bluez/hci0/dev_phone")
	for attempt := 0; attempt <= len(ancsRetryDelays); attempt++ {
		if !retry.begin(device, "subscribing", now) {
			t.Fatalf("attempt %d was not allowed", attempt)
		}
		state := retry.failed(now, false)
		if attempt == len(ancsRetryDelays) {
			if state != "retry_exhausted" || !retry.next.IsZero() {
				t.Fatalf("retry budget not exhausted: %+v", retry)
			}
			break
		}
		if retry.begin(device, "subscribing", now.Add(time.Millisecond)) {
			t.Fatal("repeated BlueZ signals bypassed backoff")
		}
		now = now.Add(ancsRetryDelays[attempt])
	}
	if retry.begin(device, "subscribing", now.Add(time.Hour)) {
		t.Fatal("exhausted subscription retried without a new connection")
	}
}

func TestANCSSubscribeErrorsDistinguishPendingFromRejection(t *testing.T) {
	for _, name := range []string{"InProgress", "Failed", "NotConnected"} {
		if terminalANCSSubscribeError(dbus.NewError("org.bluez.Error."+name, nil)) {
			t.Fatalf("%s should be retried", name)
		}
	}
	for _, name := range []string{"NotAuthorized", "NotPermitted", "NotSupported"} {
		var retry ancsRetryState
		device := dbus.ObjectPath("/org/bluez/hci0/dev_phone")
		now := time.Now()
		retry.begin(device, "subscribing", now)
		state := retry.failed(now, terminalANCSSubscribeError(dbus.NewError("org.bluez.Error."+name, nil)))
		if state != "subscription_rejected" || retry.begin(device, "subscribing", now.Add(time.Hour)) {
			t.Fatalf("%s must not cause repeated authorization attempts", name)
		}
	}
}

func ancsTestObjects() (managedObjects, ancsPaths) {
	paths := ancsPaths{
		device:             "/org/bluez/hci0/dev_phone",
		notificationSource: "/org/bluez/hci0/dev_phone/service/notification",
		controlPoint:       "/org/bluez/hci0/dev_phone/service/control",
		dataSource:         "/org/bluez/hci0/dev_phone/service/data",
	}
	service := dbus.ObjectPath("/org/bluez/hci0/dev_phone/service")
	objects := managedObjects{
		paths.device: {blueZDeviceInterface: {
			"Connected": dbus.MakeVariant(true), "ServicesResolved": dbus.MakeVariant(true),
		}},
		service: {blueZGattServiceInterface: {
			"UUID": dbus.MakeVariant(ANCSServiceUUID), "Device": dbus.MakeVariant(paths.device),
		}},
	}
	for path, uuid := range map[dbus.ObjectPath]string{
		paths.notificationSource: ANCSNotificationSourceUUID,
		paths.controlPoint:       ANCSControlPointUUID,
		paths.dataSource:         ANCSDataSourceUUID,
	} {
		objects[path] = map[string]map[string]dbus.Variant{blueZGattCharInterface: {
			"UUID": dbus.MakeVariant(uuid), "Service": dbus.MakeVariant(service),
			"Notifying": dbus.MakeVariant(true),
		}}
	}
	return objects, paths
}

func TestANCSFirstConnectionRecoversAsServicesAppear(t *testing.T) {
	service := NewService(4)
	service.status.update(func(s *RuntimeStatus) { s.WakeSubscriber = true })
	backend := newBlueZBackend(service, "Aiden", 0)
	objects, paths := ancsTestObjects()
	incomplete := managedObjects{paths.device: objects[paths.device]}
	rescan := func(want string) {
		t.Helper()
		if err := backend.rescanANCS(incomplete, paths.device); err != nil {
			t.Fatal(err)
		}
		if got := service.Status(); got.ANCSState != want || !got.WakeSubscriber {
			t.Fatalf("unexpected state: %+v", got)
		}
	}
	rescan("waiting_services")
	if !backend.ancsRetryDue(time.Now().Add(2 * time.Second)) {
		t.Fatal("missing services did not schedule an independent rescan")
	}
	for path, interfaces := range objects {
		if path != paths.dataSource {
			incomplete[path] = interfaces
		}
	}
	rescan("waiting_characteristics")
	incomplete[paths.dataSource] = objects[paths.dataSource]
	rescan("subscribed")
	if !service.Status().ANCSSubscribed || backend.ancsRetryDue(time.Now().Add(time.Hour)) {
		t.Fatal("successful subscription did not cancel recovery")
	}
}

func TestANCSDiscoveryExhaustionStillAllowsLateServiceObjects(t *testing.T) {
	service := NewService(4)
	backend := newBlueZBackend(service, "Aiden", 0)
	objects, paths := ancsTestObjects()
	incomplete := managedObjects{paths.device: objects[paths.device]}
	for i := 0; i <= len(ancsRetryDelays); i++ {
		backend.ancsRetry.next = time.Now().Add(-time.Second)
		if err := backend.rescanANCS(incomplete, paths.device); err != nil {
			t.Fatal(err)
		}
	}
	if status := service.Status(); status.ANCSState != "retry_exhausted" || status.ANCSSubscribed {
		t.Fatalf("unexpected exhausted status: %+v", status)
	}
	if backend.ancsRetryDue(time.Now().Add(time.Hour)) {
		t.Fatal("exhausted discovery kept polling")
	}
	if err := backend.rescanANCS(objects, paths.device); err != nil {
		t.Fatal(err)
	}
	if !service.Status().ANCSSubscribed {
		t.Fatal("late service discovery required a manual reconnect")
	}
}

func TestANCSDisconnectCancelsPendingRecovery(t *testing.T) {
	service := NewService(4)
	backend := newBlueZBackend(service, "Aiden", 0)
	objects, paths := ancsTestObjects()
	if err := backend.rescanANCS(managedObjects{paths.device: objects[paths.device]}, paths.device); err != nil {
		t.Fatal(err)
	}
	backend.setConnectionEnabled(false)
	// A queued rescan with an old Connected snapshot must not subscribe again.
	if err := backend.rescanANCS(objects, paths.device); err != nil {
		t.Fatal(err)
	}
	if service.Status().ANCSState != "disconnected" || backend.ancsRetryDue(time.Now().Add(time.Hour)) {
		t.Fatal("disconnect left recovery active")
	}
	backend.setConnectionEnabled(true)
	if err := backend.rescanANCS(objects, paths.device); err != nil {
		t.Fatal(err)
	}
	if !service.Status().ANCSSubscribed {
		t.Fatal("new connection did not recover")
	}
}

func TestANCSBusFailureDoesNotPollForever(t *testing.T) {
	backend := newBlueZBackend(NewService(4), "Aiden", 0)
	objects, paths := ancsTestObjects()
	if err := backend.rescanANCS(managedObjects{paths.device: objects[paths.device]}, paths.device); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(ancsRetryDelays); i++ {
		backend.ancsRetry.next = time.Now().Add(-time.Second)
		backend.retryANCSScanFailure(errors.New("GetManagedObjects timed out"))
	}
	if backend.service.Status().ANCSState != "retry_exhausted" || backend.ancsRetryDue(time.Now().Add(time.Hour)) {
		t.Fatal("D-Bus failures bypassed retry budget")
	}
}
