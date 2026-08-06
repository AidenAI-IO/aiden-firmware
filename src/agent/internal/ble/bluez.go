package ble

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	dbusPropertiesInterface      = "org.freedesktop.DBus.Properties"
	dbusObjectManagerInterface   = "org.freedesktop.DBus.ObjectManager"
	blueZAdapterInterface        = "org.bluez.Adapter1"
	blueZDeviceInterface         = "org.bluez.Device1"
	blueZGattManagerInterface    = "org.bluez.GattManager1"
	blueZGattServiceInterface    = "org.bluez.GattService1"
	blueZGattCharInterface       = "org.bluez.GattCharacteristic1"
	blueZGattDescriptorInterface = "org.bluez.GattDescriptor1"
	blueZAdvManagerInterface     = "org.bluez.LEAdvertisingManager1"
	blueZAdvertisementInterface  = "org.bluez.LEAdvertisement1"
	blueZAgentManagerInterface   = "org.bluez.AgentManager1"
	blueZAgentInterface          = "org.bluez.Agent1"

	applicationPath         = dbus.ObjectPath("/com/aiden/ble")
	wakeServicePath         = dbus.ObjectPath("/com/aiden/ble/service0")
	wakeCharPath            = dbus.ObjectPath("/com/aiden/ble/service0/char0")
	hidServicePath          = dbus.ObjectPath("/com/aiden/ble/service1")
	hidInformationCharPath  = dbus.ObjectPath("/com/aiden/ble/service1/char0")
	hidReportMapCharPath    = dbus.ObjectPath("/com/aiden/ble/service1/char1")
	hidControlPointCharPath = dbus.ObjectPath("/com/aiden/ble/service1/char2")
	hidReportCharPath       = dbus.ObjectPath("/com/aiden/ble/service1/char3")
	hidReportReferencePath  = dbus.ObjectPath("/com/aiden/ble/service1/char3/desc0")
	advertisementPath       = dbus.ObjectPath("/com/aiden/ble/advertisement0")
	agentPath               = dbus.ObjectPath("/com/aiden/ble/agent0")
	blueZRootPath           = dbus.ObjectPath("/")
)

type managedObjects map[dbus.ObjectPath]map[string]map[string]dbus.Variant

type ancsPaths struct {
	device             dbus.ObjectPath
	notificationSource dbus.ObjectPath
	controlPoint       dbus.ObjectPath
	dataSource         dbus.ObjectPath
}

type exportedGattObject struct {
	path          dbus.ObjectPath
	interfaceName string
	properties    *prop.Properties
}

func (p ancsPaths) complete() bool {
	return p.device.IsValid() && p.notificationSource.IsValid() &&
		p.controlPoint.IsValid() && p.dataSource.IsValid()
}

type blueZBackend struct {
	service       *Service
	deviceName    string
	pairingWindow time.Duration
	conn          *dbus.Conn
	adapter       dbus.ObjectPath
	signals       chan *dbus.Signal

	wakeProps      *prop.Properties
	advProps       *prop.Properties
	wakeObject     *wakeCharacteristic
	hidReportProps *prop.Properties
	gattObjects    []exportedGattObject

	ancsMu          sync.Mutex
	ancs            ancsPaths
	stateMu         sync.Mutex
	trustedDevice   dbus.ObjectPath
	pairingOpen     bool
	pairingDeadline time.Time
	closed          bool
}

func newBlueZBackend(service *Service, deviceName string, pairingWindow time.Duration) *blueZBackend {
	if strings.TrimSpace(deviceName) == "" {
		deviceName = "Aiden"
	}
	if pairingWindow < 0 {
		pairingWindow = 0
	}
	return &blueZBackend{service: service, deviceName: deviceName, pairingWindow: pairingWindow}
}

func (b *blueZBackend) start() error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect system D-Bus: %w", err)
	}
	b.conn = conn
	b.signals = make(chan *dbus.Signal, 64)
	b.conn.Signal(b.signals)

	if err := b.addSignalMatches(); err != nil {
		return fmt.Errorf("subscribe BlueZ signals: %w", err)
	}

	objects, err := b.getManagedObjects()
	if err != nil {
		return err
	}
	b.adapter = findAdapter(objects)
	if !b.adapter.IsValid() {
		return errors.New("BlueZ adapter hci0 is unavailable")
	}
	adapterProps := objects[b.adapter][blueZAdapterInterface]
	b.deviceName = stableDeviceName(b.deviceName, variantString(adapterProps, "Address"))
	trusted, bondedCount := selectBondedDevice(objects)
	b.stateMu.Lock()
	b.trustedDevice = trusted
	if !trusted.IsValid() && b.pairingWindow > 0 {
		b.pairingOpen = true
		b.pairingDeadline = time.Now().Add(b.pairingWindow)
	}
	pairingOpen := b.pairingOpen
	pairingDeadline := b.pairingDeadline
	b.stateMu.Unlock()

	if err := b.exportObjects(); err != nil {
		return err
	}
	if err := b.configureAdapter(pairingOpen); err != nil {
		return err
	}
	if err := b.registerAgent(); err != nil {
		return err
	}
	if err := b.registerGatt(); err != nil {
		return err
	}
	if err := b.registerAdvertisement(); err != nil {
		return err
	}

	if trusted.IsValid() {
		if !variantBool(objects[trusted][blueZDeviceInterface], "Trusted") {
			if err := b.markDeviceTrusted(trusted); err != nil {
				return err
			}
		}
	}
	b.service.status.update(func(status *RuntimeStatus) {
		status.BackendAvailable = true
		status.DeviceName = b.deviceName
		status.AdapterPath = string(b.adapter)
		status.AdapterAddress = variantString(adapterProps, "Address")
		status.AdapterPowered = true
		status.GattRegistered = true
		status.HOGPRegistered = true
		status.Advertising = true
		status.PairingOpen = pairingOpen
		status.PairingDeadline = formatDeadline(pairingDeadline)
		status.BondedDeviceCount = bondedCount
	})
	if err := b.rescanBluetoothState(); err != nil {
		b.service.status.update(func(status *RuntimeStatus) { status.LastError = err.Error() })
	}
	return nil
}

func (b *blueZBackend) run(ctx context.Context) error {
	maintenance := time.NewTicker(time.Second)
	defer maintenance.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-b.conn.Context().Done():
			return errors.New("system D-Bus connection closed")
		case signal, ok := <-b.signals:
			if !ok {
				return errors.New("BlueZ signal channel closed")
			}
			b.handleSignal(signal)
		case now := <-maintenance.C:
			b.expirePairingWindow(now)
			b.service.consumer.ExpireActive(now)
		}
	}
}

func (b *blueZBackend) close() {
	if b.conn == nil {
		return
	}
	b.ancsMu.Lock()
	if b.closed {
		b.ancsMu.Unlock()
		return
	}
	b.closed = true
	paths := b.ancs
	b.ancs = ancsPaths{}
	b.ancsMu.Unlock()

	if paths.notificationSource.IsValid() {
		_ = b.conn.Object(BlueZBusName, paths.notificationSource).
			Call(blueZGattCharInterface+".StopNotify", 0).Err
	}
	if paths.dataSource.IsValid() {
		_ = b.conn.Object(BlueZBusName, paths.dataSource).
			Call(blueZGattCharInterface+".StopNotify", 0).Err
	}
	b.service.consumer.ResetConnection("Bluetooth connection closed")
	if b.adapter.IsValid() {
		adapter := b.conn.Object(BlueZBusName, b.adapter)
		_ = adapter.Call(blueZAdvManagerInterface+".UnregisterAdvertisement", 0, advertisementPath).Err
		_ = adapter.Call(blueZGattManagerInterface+".UnregisterApplication", 0, applicationPath).Err
	}
	_ = b.conn.Object(BlueZBusName, dbus.ObjectPath("/org/bluez")).
		Call(blueZAgentManagerInterface+".UnregisterAgent", 0, agentPath).Err
	b.conn.RemoveSignal(b.signals)
	_ = b.conn.Close()
	b.service.status.update(func(status *RuntimeStatus) {
		status.BackendAvailable = false
		status.GattRegistered = false
		status.HOGPRegistered = false
		status.Advertising = false
		status.PairingOpen = false
		status.PairingDeadline = ""
		status.WakeSubscriber = false
		status.ANCSSubscribed = false
	})
}

func (b *blueZBackend) exportObjects() error {
	manager := &gattObjectManager{backend: b}
	if err := b.conn.Export(manager, applicationPath, dbusObjectManagerInterface); err != nil {
		return fmt.Errorf("export GATT object manager: %w", err)
	}
	if err := exportIntrospection(b.conn, applicationPath, dbusObjectManagerInterface, manager, nil); err != nil {
		return err
	}

	var err error
	_, err = b.exportGattObject(wakeServicePath, blueZGattServiceInterface, prop.Map{
		blueZGattServiceInterface: {
			"UUID":            {Value: WakeServiceUUID, Emit: prop.EmitConst},
			"Primary":         {Value: true, Emit: prop.EmitConst},
			"Characteristics": {Value: []dbus.ObjectPath{wakeCharPath}, Emit: prop.EmitConst},
		},
	}, &struct{}{})
	if err != nil {
		return fmt.Errorf("export Wake service: %w", err)
	}

	b.wakeObject = &wakeCharacteristic{backend: b}
	b.wakeProps, err = b.exportGattObject(wakeCharPath, blueZGattCharInterface, prop.Map{
		blueZGattCharInterface: {
			"UUID":        {Value: WakeCharacteristicUUID, Emit: prop.EmitConst},
			"Service":     {Value: wakeServicePath, Emit: prop.EmitConst},
			"Flags":       {Value: []string{"read", "notify", "encrypt-read", "encrypt-notify"}, Emit: prop.EmitConst},
			"Descriptors": {Value: []dbus.ObjectPath{}, Emit: prop.EmitConst},
			"Value":       {Value: []byte{}, Emit: prop.EmitTrue},
			"Notifying":   {Value: false, Emit: prop.EmitTrue},
		},
	}, b.wakeObject)
	if err != nil {
		return fmt.Errorf("export Wake characteristic: %w", err)
	}

	if _, err := b.exportGattObject(hidServicePath, blueZGattServiceInterface, prop.Map{
		blueZGattServiceInterface: {
			"UUID":    {Value: HIDServiceUUID, Emit: prop.EmitConst},
			"Primary": {Value: true, Emit: prop.EmitConst},
			"Characteristics": {Value: []dbus.ObjectPath{
				hidInformationCharPath,
				hidReportMapCharPath,
				hidControlPointCharPath,
				hidReportCharPath,
			}, Emit: prop.EmitConst},
		},
	}, &struct{}{}); err != nil {
		return fmt.Errorf("export HID service: %w", err)
	}
	if _, err := b.exportGattObject(hidInformationCharPath, blueZGattCharInterface, prop.Map{
		blueZGattCharInterface: {
			"UUID":        {Value: HIDInformationCharacteristicUUID, Emit: prop.EmitConst},
			"Service":     {Value: hidServicePath, Emit: prop.EmitConst},
			"Flags":       {Value: []string{"read", "encrypt-read"}, Emit: prop.EmitConst},
			"Descriptors": {Value: []dbus.ObjectPath{}, Emit: prop.EmitConst},
		},
	}, &staticReadCharacteristic{value: hidInformationValue()}); err != nil {
		return fmt.Errorf("export HID Information characteristic: %w", err)
	}
	if _, err := b.exportGattObject(hidReportMapCharPath, blueZGattCharInterface, prop.Map{
		blueZGattCharInterface: {
			"UUID":        {Value: HIDReportMapCharacteristicUUID, Emit: prop.EmitConst},
			"Service":     {Value: hidServicePath, Emit: prop.EmitConst},
			"Flags":       {Value: []string{"read", "encrypt-read"}, Emit: prop.EmitConst},
			"Descriptors": {Value: []dbus.ObjectPath{}, Emit: prop.EmitConst},
		},
	}, &staticReadCharacteristic{value: hidReportMapValue()}); err != nil {
		return fmt.Errorf("export HID Report Map characteristic: %w", err)
	}
	if _, err := b.exportGattObject(hidControlPointCharPath, blueZGattCharInterface, prop.Map{
		blueZGattCharInterface: {
			"UUID":        {Value: HIDControlPointCharacteristicUUID, Emit: prop.EmitConst},
			"Service":     {Value: hidServicePath, Emit: prop.EmitConst},
			"Flags":       {Value: []string{"write-without-response", "encrypt-write"}, Emit: prop.EmitConst},
			"Descriptors": {Value: []dbus.ObjectPath{}, Emit: prop.EmitConst},
		},
	}, &hidControlPointCharacteristic{}); err != nil {
		return fmt.Errorf("export HID Control Point characteristic: %w", err)
	}

	hidReportObject := &hidReportCharacteristic{}
	b.hidReportProps, err = b.exportGattObject(hidReportCharPath, blueZGattCharInterface, prop.Map{
		blueZGattCharInterface: {
			"UUID":        {Value: HIDReportCharacteristicUUID, Emit: prop.EmitConst},
			"Service":     {Value: hidServicePath, Emit: prop.EmitConst},
			"Flags":       {Value: []string{"read", "notify", "encrypt-read", "encrypt-notify"}, Emit: prop.EmitConst},
			"Descriptors": {Value: []dbus.ObjectPath{hidReportReferencePath}, Emit: prop.EmitConst},
			"Value":       {Value: []byte{0x00, 0x00}, Emit: prop.EmitTrue},
			"Notifying":   {Value: false, Emit: prop.EmitTrue},
		},
	}, hidReportObject)
	if err != nil {
		return fmt.Errorf("export HID Report characteristic: %w", err)
	}
	hidReportObject.properties = b.hidReportProps
	if _, err := b.exportGattObject(hidReportReferencePath, blueZGattDescriptorInterface, prop.Map{
		blueZGattDescriptorInterface: {
			"UUID":           {Value: HIDReportReferenceDescriptorUUID, Emit: prop.EmitConst},
			"Characteristic": {Value: hidReportCharPath, Emit: prop.EmitConst},
			"Flags":          {Value: []string{"read", "encrypt-read"}, Emit: prop.EmitConst},
		},
	}, &staticReadDescriptor{value: hidReportReferenceValue()}); err != nil {
		return fmt.Errorf("export HID Report Reference descriptor: %w", err)
	}

	b.advProps, err = prop.Export(b.conn, advertisementPath, prop.Map{
		blueZAdvertisementInterface: {
			"Type":         {Value: "peripheral", Emit: prop.EmitConst},
			"ServiceUUIDs": {Value: []string{HIDServiceUUID, WakeServiceUUID}, Emit: prop.EmitConst},
			"LocalName":    {Value: b.deviceName, Emit: prop.EmitConst},
			"Appearance":   {Value: HIDGenericAppearance, Emit: prop.EmitConst},
			"Discoverable": {Value: b.pairingOpen, Emit: prop.EmitTrue},
		},
	})
	if err != nil {
		return fmt.Errorf("export BLE advertisement properties: %w", err)
	}
	advertisement := &advertisementObject{}
	if err := b.conn.Export(advertisement, advertisementPath, blueZAdvertisementInterface); err != nil {
		return fmt.Errorf("export BLE advertisement: %w", err)
	}
	agent := &pairingAgent{backend: b}
	if err := b.conn.Export(agent, agentPath, blueZAgentInterface); err != nil {
		return fmt.Errorf("export pairing agent: %w", err)
	}

	if err := exportIntrospection(b.conn, advertisementPath, blueZAdvertisementInterface, advertisement, b.advProps); err != nil {
		return err
	}
	return exportIntrospection(b.conn, agentPath, blueZAgentInterface, agent, nil)
}

func (b *blueZBackend) exportGattObject(
	path dbus.ObjectPath,
	interfaceName string,
	properties prop.Map,
	object any,
) (*prop.Properties, error) {
	exported, err := prop.Export(b.conn, path, properties)
	if err != nil {
		return nil, err
	}
	if _, ok := object.(*struct{}); !ok {
		if err := b.conn.Export(object, path, interfaceName); err != nil {
			return nil, err
		}
	}
	if err := exportIntrospection(b.conn, path, interfaceName, object, exported); err != nil {
		return nil, err
	}
	b.gattObjects = append(b.gattObjects, exportedGattObject{
		path:          path,
		interfaceName: interfaceName,
		properties:    exported,
	})
	return exported, nil
}

func exportIntrospection(conn *dbus.Conn, path dbus.ObjectPath, interfaceName string, object any, properties *prop.Properties) error {
	iface := introspect.Interface{Name: interfaceName, Methods: introspect.Methods(object)}
	if properties != nil {
		iface.Properties = properties.Introspection(interfaceName)
	}
	node := &introspect.Node{Name: string(path), Interfaces: []introspect.Interface{iface}}
	return conn.Export(introspect.NewIntrospectable(node), path, introspect.IntrospectData.Name)
}

func (b *blueZBackend) addSignalMatches() error {
	if err := b.conn.AddMatchSignal(
		dbus.WithMatchInterface(dbusPropertiesInterface),
		dbus.WithMatchPathNamespace(dbus.ObjectPath("/org/bluez")),
	); err != nil {
		return err
	}
	return b.conn.AddMatchSignal(
		dbus.WithMatchInterface(dbusObjectManagerInterface),
		dbus.WithMatchPathNamespace(dbus.ObjectPath("/org/bluez")),
	)
}

func (b *blueZBackend) configureAdapter(pairingOpen bool) error {
	properties := []struct {
		name  string
		value any
	}{
		{name: "Powered", value: true},
		{name: "Alias", value: b.deviceName},
		{name: "Pairable", value: pairingOpen},
		{name: "Discoverable", value: pairingOpen},
	}
	for _, property := range properties {
		if err := b.conn.Object(BlueZBusName, b.adapter).
			Call(dbusPropertiesInterface+".Set", 0, blueZAdapterInterface, property.name, dbus.MakeVariant(property.value)).Err; err != nil {
			return fmt.Errorf("set adapter %s: %w", property.name, err)
		}
	}
	return nil
}

func (b *blueZBackend) registerAgent() error {
	manager := b.conn.Object(BlueZBusName, dbus.ObjectPath("/org/bluez"))
	if err := manager.Call(blueZAgentManagerInterface+".RegisterAgent", 0, agentPath, "NoInputNoOutput").Err; err != nil {
		return fmt.Errorf("register pairing agent: %w", err)
	}
	if err := manager.Call(blueZAgentManagerInterface+".RequestDefaultAgent", 0, agentPath).Err; err != nil {
		return fmt.Errorf("select default pairing agent: %w", err)
	}
	return nil
}

func (b *blueZBackend) registerGatt() error {
	options := map[string]dbus.Variant{}
	if err := b.conn.Object(BlueZBusName, b.adapter).
		Call(blueZGattManagerInterface+".RegisterApplication", 0, applicationPath, options).Err; err != nil {
		return fmt.Errorf("register BLE GATT application: %w", err)
	}
	return nil
}

func (b *blueZBackend) registerAdvertisement() error {
	options := map[string]dbus.Variant{}
	if err := b.conn.Object(BlueZBusName, b.adapter).
		Call(blueZAdvManagerInterface+".RegisterAdvertisement", 0, advertisementPath, options).Err; err != nil {
		return fmt.Errorf("register BLE advertisement: %w", err)
	}
	return nil
}

func (b *blueZBackend) getManagedObjects() (managedObjects, error) {
	objects := managedObjects{}
	if err := b.conn.Object(BlueZBusName, blueZRootPath).
		Call(dbusObjectManagerInterface+".GetManagedObjects", 0).Store(&objects); err != nil {
		return nil, fmt.Errorf("read BlueZ managed objects: %w", err)
	}
	return objects, nil
}

func findAdapter(objects managedObjects) dbus.ObjectPath {
	preferred := dbus.ObjectPath("/org/bluez/hci0")
	if _, ok := objects[preferred][blueZAdapterInterface]; ok {
		return preferred
	}
	for path, interfaces := range objects {
		if _, ok := interfaces[blueZAdapterInterface]; ok {
			return path
		}
	}
	return ""
}

func (b *blueZBackend) handleSignal(signal *dbus.Signal) {
	if signal == nil {
		return
	}
	switch signal.Name {
	case dbusPropertiesInterface + ".PropertiesChanged":
		b.handlePropertiesChanged(signal)
	case dbusObjectManagerInterface + ".InterfacesAdded",
		dbusObjectManagerInterface + ".InterfacesRemoved":
		if err := b.rescanBluetoothState(); err != nil {
			b.service.status.update(func(status *RuntimeStatus) { status.LastError = err.Error() })
		}
	}
}

func (b *blueZBackend) handlePropertiesChanged(signal *dbus.Signal) {
	if len(signal.Body) < 2 {
		return
	}
	interfaceName, _ := signal.Body[0].(string)
	changed, _ := signal.Body[1].(map[string]dbus.Variant)
	if interfaceName == blueZGattCharInterface {
		if value, ok := changed["Value"]; ok {
			if bytes, ok := value.Value().([]byte); ok {
				b.ancsMu.Lock()
				paths := b.ancs
				b.ancsMu.Unlock()
				switch signal.Path {
				case paths.notificationSource:
					if err := b.service.consumer.HandleNotificationSource(bytes); err != nil {
						b.service.status.update(func(status *RuntimeStatus) { status.LastError = err.Error() })
					}
				case paths.dataSource:
					if err := b.service.consumer.HandleDataSource(bytes); err != nil {
						b.service.status.update(func(status *RuntimeStatus) { status.LastError = err.Error() })
					}
				}
			}
		}
	}
	needsRescan := interfaceName == blueZDeviceInterface
	if interfaceName == blueZGattCharInterface {
		_, notifyingChanged := changed["Notifying"]
		_, serviceChanged := changed["Service"]
		_, uuidChanged := changed["UUID"]
		needsRescan = notifyingChanged || serviceChanged || uuidChanged
	}
	if needsRescan {
		if err := b.rescanBluetoothState(); err != nil {
			b.service.status.update(func(status *RuntimeStatus) { status.LastError = err.Error() })
		}
	}
}

func (b *blueZBackend) rescanBluetoothState() error {
	objects, err := b.getManagedObjects()
	if err != nil {
		return err
	}
	trusted, bondedCount := b.refreshTrustedDevice(objects)
	var trustedProps map[string]dbus.Variant
	if trusted.IsValid() {
		trustedProps = objects[trusted][blueZDeviceInterface]
	}
	b.updateDeviceStatus(trusted, trustedProps, bondedCount)
	return b.rescanANCS(objects, trusted)
}

func (b *blueZBackend) rescanANCS(objects managedObjects, trustedDevice dbus.ObjectPath) error {
	type candidate struct {
		paths       ancsPaths
		deviceProps map[string]dbus.Variant
	}
	var selected *candidate

	for servicePath, interfaces := range objects {
		serviceProps, ok := interfaces[blueZGattServiceInterface]
		if !ok || !strings.EqualFold(variantString(serviceProps, "UUID"), ANCSServiceUUID) {
			continue
		}
		devicePath, ok := variantObjectPath(serviceProps, "Device")
		if !ok || devicePath != trustedDevice {
			continue
		}
		deviceProps := objects[devicePath][blueZDeviceInterface]
		if !variantBool(deviceProps, "Connected") || !variantBool(deviceProps, "ServicesResolved") {
			continue
		}
		paths := ancsPaths{device: devicePath}
		for charPath, charInterfaces := range objects {
			charProps, ok := charInterfaces[blueZGattCharInterface]
			if !ok {
				continue
			}
			parentService, ok := variantObjectPath(charProps, "Service")
			if !ok || parentService != servicePath {
				continue
			}
			switch strings.ToLower(variantString(charProps, "UUID")) {
			case ANCSNotificationSourceUUID:
				paths.notificationSource = charPath
			case ANCSControlPointUUID:
				paths.controlPoint = charPath
			case ANCSDataSourceUUID:
				paths.dataSource = charPath
			}
		}
		selected = &candidate{paths: paths, deviceProps: deviceProps}
		if paths.complete() {
			break
		}
	}

	if selected == nil {
		b.clearANCS("ANCS device disconnected")
		return nil
	}
	if !selected.paths.complete() {
		b.clearANCS("ANCS characteristics are incomplete")
		return nil
	}

	b.ancsMu.Lock()
	current := b.ancs
	b.ancsMu.Unlock()
	if current == selected.paths {
		return nil
	}
	b.clearANCS("ANCS connection changed")

	if err := b.startNotify(objects, selected.paths.notificationSource); err != nil {
		return fmt.Errorf("subscribe ANCS Notification Source: %w", err)
	}
	if err := b.startNotify(objects, selected.paths.dataSource); err != nil {
		_ = b.conn.Object(BlueZBusName, selected.paths.notificationSource).
			Call(blueZGattCharInterface+".StopNotify", 0).Err
		return fmt.Errorf("subscribe ANCS Data Source: %w", err)
	}

	b.ancsMu.Lock()
	b.ancs = selected.paths
	b.ancsMu.Unlock()
	b.service.consumer.SetControlPointWriter(func(command []byte) error {
		options := map[string]dbus.Variant{"type": dbus.MakeVariant("command")}
		return b.conn.Object(BlueZBusName, selected.paths.controlPoint).
			Call(blueZGattCharInterface+".WriteValue", 0, command, options).Err
	})
	b.service.status.update(func(status *RuntimeStatus) {
		status.ANCSSubscribed = true
		status.LastError = ""
	})
	return nil
}

func (b *blueZBackend) startNotify(objects managedObjects, path dbus.ObjectPath) error {
	properties := objects[path][blueZGattCharInterface]
	if variantBool(properties, "Notifying") {
		return nil
	}
	return b.conn.Object(BlueZBusName, path).Call(blueZGattCharInterface+".StartNotify", 0).Err
}

func (b *blueZBackend) clearANCS(reason string) {
	b.ancsMu.Lock()
	hadConnection := b.ancs.complete()
	b.ancs = ancsPaths{}
	b.ancsMu.Unlock()
	if hadConnection {
		b.service.consumer.ResetConnection(reason)
	}
	b.service.status.update(func(status *RuntimeStatus) { status.ANCSSubscribed = false })
}

func (b *blueZBackend) NotifyWake(sequence uint64, reason string) (bool, error) {
	b.ancsMu.Lock()
	defer b.ancsMu.Unlock()
	if b.closed {
		return false, ErrBluetoothUnavailable
	}
	if b.wakeProps == nil {
		return false, ErrBluetoothUnavailable
	}
	payload := make([]byte, 12)
	payload[0] = 1
	payload[1] = wakeReasonCode(reason)
	binary.LittleEndian.PutUint64(payload[4:], sequence)
	notifying, _ := b.wakeProps.GetMust(blueZGattCharInterface, "Notifying").(bool)
	b.wakeProps.SetMust(blueZGattCharInterface, "Value", payload)
	return notifying, nil
}

func wakeReasonCode(reason string) byte {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "queue", "phone_bridge", "command":
		return 1
	case "manual", "user":
		return 2
	case "system", "notification":
		return 3
	default:
		return 0
	}
}

type gattObjectManager struct {
	backend *blueZBackend
}

func (m *gattObjectManager) GetManagedObjects() (managedObjects, *dbus.Error) {
	objects := make(managedObjects, len(m.backend.gattObjects))
	for _, object := range m.backend.gattObjects {
		properties, err := object.properties.GetAll(object.interfaceName)
		if err != nil {
			return nil, err
		}
		objects[object.path] = map[string]map[string]dbus.Variant{
			object.interfaceName: properties,
		}
	}
	return objects, nil
}

type wakeCharacteristic struct {
	backend *blueZBackend
}

func (c *wakeCharacteristic) ReadValue(options map[string]dbus.Variant) ([]byte, *dbus.Error) {
	value, _ := c.backend.wakeProps.GetMust(blueZGattCharInterface, "Value").([]byte)
	return readValueAtOffset(value, options)
}

func (c *wakeCharacteristic) StartNotify() *dbus.Error {
	if notifying, _ := c.backend.wakeProps.GetMust(blueZGattCharInterface, "Notifying").(bool); notifying {
		return dbus.NewError("org.bluez.Error.InProgress", []any{"notifications already enabled"})
	}
	c.backend.wakeProps.SetMust(blueZGattCharInterface, "Notifying", true)
	c.backend.service.status.update(func(status *RuntimeStatus) { status.WakeSubscriber = true })
	return nil
}

func (c *wakeCharacteristic) StopNotify() *dbus.Error {
	c.backend.wakeProps.SetMust(blueZGattCharInterface, "Notifying", false)
	c.backend.service.status.update(func(status *RuntimeStatus) { status.WakeSubscriber = false })
	return nil
}

type advertisementObject struct{}

func (a *advertisementObject) Release() *dbus.Error { return nil }

type pairingAgent struct {
	backend *blueZBackend
}

func (a *pairingAgent) Release() *dbus.Error { return nil }
func (a *pairingAgent) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	if err := a.authorize(device); err != nil {
		return "", err
	}
	return "000000", nil
}
func (a *pairingAgent) DisplayPinCode(dbus.ObjectPath, string) *dbus.Error { return nil }
func (a *pairingAgent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	if err := a.authorize(device); err != nil {
		return 0, err
	}
	return 0, nil
}
func (a *pairingAgent) DisplayPasskey(dbus.ObjectPath, uint32, uint16) *dbus.Error {
	return nil
}
func (a *pairingAgent) RequestConfirmation(device dbus.ObjectPath, _ uint32) *dbus.Error {
	return a.authorize(device)
}
func (a *pairingAgent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error {
	return a.authorize(device)
}
func (a *pairingAgent) AuthorizeService(device dbus.ObjectPath, _ string) *dbus.Error {
	return a.authorize(device)
}
func (a *pairingAgent) Cancel() *dbus.Error { return nil }

func (a *pairingAgent) authorize(device dbus.ObjectPath) *dbus.Error {
	if a.backend != nil && a.backend.deviceAllowed(device) {
		return nil
	}
	return dbus.NewError("org.bluez.Error.Rejected", []any{"only the trusted iPhone may use Aiden BLE"})
}

func variantString(properties map[string]dbus.Variant, name string) string {
	if variant, ok := properties[name]; ok {
		if value, ok := variant.Value().(string); ok {
			return value
		}
	}
	return ""
}

func variantBool(properties map[string]dbus.Variant, name string) bool {
	if variant, ok := properties[name]; ok {
		if value, ok := variant.Value().(bool); ok {
			return value
		}
	}
	return false
}

func variantObjectPath(properties map[string]dbus.Variant, name string) (dbus.ObjectPath, bool) {
	if variant, ok := properties[name]; ok {
		value, ok := variant.Value().(dbus.ObjectPath)
		return value, ok && value.IsValid()
	}
	return "", false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
