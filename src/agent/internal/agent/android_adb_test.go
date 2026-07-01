package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAndroidADBStatusRequiresPairWhenFrameUnavailableAndDeviceMismatched(t *testing.T) {
	manager := &androidADBManager{
		frameHealth: func(context.Context) (*FrameHealthResult, error) {
			return &FrameHealthResult{State: "RECOVERING"}, nil
		},
		runADB: func(_ context.Context, args ...string) (string, error) {
			switch strings.Join(args, " ") {
			case "devices":
				return "List of devices attached\nserial-1\tdevice\n", nil
			case "-s serial-1 shell ip addr":
				return "2: rndis0    inet 192.168.42.88/24 brd 192.168.42.255", nil
			default:
				t.Fatalf("unexpected adb command: %q", strings.Join(args, " "))
				return "", nil
			}
		},
	}

	status := manager.Status(context.Background(), "192.168.42.123")
	if !status.PairRequired {
		t.Fatalf("PairRequired = false, want true: %#v", status)
	}
	if !status.ADB.HasConnectedDevice {
		t.Fatalf("HasConnectedDevice = false, want true: %#v", status.ADB)
	}
	if status.ADB.MatchedDevice {
		t.Fatalf("MatchedDevice = true, want false: %#v", status.ADB)
	}
	if status.Frame.Available {
		t.Fatalf("Frame.Available = true, want false: %#v", status.Frame)
	}
}

func TestAndroidADBStatusMatchesCurrentPhoneUSBIP(t *testing.T) {
	manager := &androidADBManager{
		frameHealth: func(context.Context) (*FrameHealthResult, error) {
			return &FrameHealthResult{State: "RUNNING", LatestSeq: 12}, nil
		},
		runADB: func(_ context.Context, args ...string) (string, error) {
			switch strings.Join(args, " ") {
			case "devices":
				return "List of devices attached\n192.168.1.10:35555\tdevice\n", nil
			case "-s 192.168.1.10:35555 shell ip addr":
				return "2: rndis0    inet 192.168.42.123/24 brd 192.168.42.255", nil
			default:
				t.Fatalf("unexpected adb command: %q", strings.Join(args, " "))
				return "", nil
			}
		},
	}

	status := manager.Status(context.Background(), "192.168.42.123")
	if status.PairRequired {
		t.Fatalf("PairRequired = true, want false: %#v", status)
	}
	if !status.Frame.Available {
		t.Fatalf("Frame.Available = false, want true: %#v", status.Frame)
	}
	if !status.ADB.MatchedDevice {
		t.Fatalf("MatchedDevice = false, want true: %#v", status.ADB)
	}
	if status.ADB.MatchedSerial != "192.168.1.10:35555" {
		t.Fatalf("MatchedSerial = %q, want %q", status.ADB.MatchedSerial, "192.168.1.10:35555")
	}
}

func TestExtractUSBIPv4sUsesOnlyInetAddresses(t *testing.T) {
	output := strings.Join([]string{
		"2: rndis0    inet 192.168.42.123/24 brd 192.168.42.255 scope global rndis0",
		"3: rndis1    inet6 fe80::1234/64 scope link",
		"4: rndis2    brd 192.168.42.200",
		"5: rndis3    inet 192.168.42.260/24 scope global rndis3",
		"6: rndis4    inet 192.168.42.123/24 scope global secondary rndis4",
		"7: rndis5    inet 192.168.42.88/24 scope global rndis5",
	}, "\n")

	got := extractUSBIPv4s(output)
	want := []string{"192.168.42.123", "192.168.42.88"}
	if len(got) != len(want) {
		t.Fatalf("extractUSBIPv4s() len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extractUSBIPv4s()[%d] = %q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

func TestAndroidADBPairOnlyPairsAndReturnsStatus(t *testing.T) {
	var commands []string
	manager := &androidADBManager{
		frameHealth: func(context.Context) (*FrameHealthResult, error) {
			return &FrameHealthResult{State: "RECOVERING"}, nil
		},
		runADB: func(_ context.Context, args ...string) (string, error) {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			switch command {
			case "pair 192.168.1.10:37099 123456":
				return "Successfully paired to 192.168.1.10:37099", nil
			case "devices":
				return "List of devices attached\n", nil
			default:
				t.Fatalf("unexpected adb command: %q", command)
				return "", nil
			}
		},
	}

	result, err := manager.Pair(context.Background(), AndroidADBPairRequest{
		PairHost: "192.168.1.10",
		PairPort: "37099",
		PairCode: "123456",
		AppUSBIP: "192.168.42.123",
	})
	if err != nil {
		t.Fatalf("Pair() error = %v", err)
	}
	if !result.OK {
		t.Fatalf("Pair() OK = false, want true: %#v", result)
	}
	if result.Status.ADB.HasConnectedDevice {
		t.Fatalf("HasConnectedDevice = true, want false: %#v", result.Status.ADB)
	}
	if !result.Status.PairRequired {
		t.Fatalf("PairRequired = false, want true until a device appears: %#v", result.Status)
	}
	if len(commands) != 2 || commands[0] != "pair 192.168.1.10:37099 123456" || commands[1] != "devices" {
		t.Fatalf("unexpected adb command sequence: %#v", commands)
	}
}

func TestNormalizeAndroidADBPairRequestRejectsNonPrivatePairHost(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		wantErr string
	}{
		{
			name:    "hostname",
			host:    "example.com",
			wantErr: "pair_host must be a private IPv4 address shown by Android wireless debugging",
		},
		{
			name:    "publicIPv4",
			host:    "8.8.8.8",
			wantErr: "pair_host must be in a private IPv4 range shown by Android wireless debugging",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeAndroidADBPairRequest(AndroidADBPairRequest{
				PairHost: tc.host,
				PairPort: "37099",
				PairCode: "123456",
			})
			if err == nil {
				t.Fatal("normalizeAndroidADBPairRequest() error = nil, want non-nil")
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("normalizeAndroidADBPairRequest() error = %q, want %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestNormalizeAndroidADBPairRequestAcceptsPrivateIPv4(t *testing.T) {
	got, err := normalizeAndroidADBPairRequest(AndroidADBPairRequest{
		PairHost: "[192.168.1.10]",
		PairPort: "37099",
		PairCode: "123456",
	})
	if err != nil {
		t.Fatalf("normalizeAndroidADBPairRequest() error = %v", err)
	}
	if got.PairHost != "192.168.1.10" {
		t.Fatalf("PairHost = %q, want %q", got.PairHost, "192.168.1.10")
	}
}

type stubAndroidADBController struct {
	statusFn func(context.Context, string) AndroidADBStatusResponse
	pairFn   func(context.Context, AndroidADBPairRequest) (AndroidADBPairResponse, error)
}

func (s stubAndroidADBController) Status(ctx context.Context, appUSBIP string) AndroidADBStatusResponse {
	return s.statusFn(ctx, appUSBIP)
}

func (s stubAndroidADBController) Pair(ctx context.Context, req AndroidADBPairRequest) (AndroidADBPairResponse, error) {
	return s.pairFn(ctx, req)
}

func TestServerHandleAndroidADBStatusForwardsAppUSBIP(t *testing.T) {
	var gotIP string
	server := &Server{
		androidADB: stubAndroidADBController{
			statusFn: func(_ context.Context, appUSBIP string) AndroidADBStatusResponse {
				gotIP = appUSBIP
				return AndroidADBStatusResponse{OK: true}
			},
			pairFn: func(context.Context, AndroidADBPairRequest) (AndroidADBPairResponse, error) {
				t.Fatal("unexpected Pair call")
				return AndroidADBPairResponse{}, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/android-adb/status?app_usb_ip=192.168.42.123", nil)
	rec := httptest.NewRecorder()
	server.handleAndroidADBStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	if gotIP != "192.168.42.123" {
		t.Fatalf("app_usb_ip = %q, want %q", gotIP, "192.168.42.123")
	}
}

func TestServerHandleAndroidADBPairReturnsControllerStatusCode(t *testing.T) {
	server := &Server{
		androidADB: stubAndroidADBController{
			statusFn: func(context.Context, string) AndroidADBStatusResponse {
				t.Fatal("unexpected Status call")
				return AndroidADBStatusResponse{}
			},
			pairFn: func(_ context.Context, req AndroidADBPairRequest) (AndroidADBPairResponse, error) {
				if req.PairHost != "192.168.1.10" {
					t.Fatalf("PairHost = %q, want %q", req.PairHost, "192.168.1.10")
				}
				return AndroidADBPairResponse{
					OK:    false,
					Error: "mismatch",
				}, &androidADBRequestError{statusCode: http.StatusConflict, message: "mismatch"}
			},
		},
	}

	body, err := json.Marshal(AndroidADBPairRequest{PairHost: "192.168.1.10"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/android-adb/pair", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleAndroidADBPair(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status code = %d, want 409", rec.Code)
	}
}
