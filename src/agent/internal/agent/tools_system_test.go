package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuiltinToolSetRegistersSystemTools(t *testing.T) {
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	for _, name := range []string{"weather", "shell"} {
		if _, ok := tools.Get(name); !ok {
			t.Fatalf("builtin tool %q was not registered", name)
		}
	}
	for _, name := range []string{"calculator", "current_time"} {
		if _, ok := tools.Get(name); ok {
			t.Fatalf("removed utility tool %q is still registered", name)
		}
	}
	if _, ok := tools.Get("wait_for_wakeup"); ok {
		t.Fatal("wait_for_wakeup should not be registered")
	}
}

func TestWeatherToolGeocodesAndFetchesForecast(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("name"); got != "Shanghai" {
			t.Fatalf("geocoding name = %q, want Shanghai", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"name":"Shanghai","admin1":"Shanghai","country":"China","latitude":31.2304,"longitude":121.4737}]}`))
	})
	mux.HandleFunc("/v1/forecast", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("timezone"); got != "auto" {
			t.Fatalf("forecast timezone = %q, want auto", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"timezone":"Asia/Shanghai",
			"current":{
				"time":"2026-05-23T12:00",
				"temperature_2m":24.4,
				"apparent_temperature":25.1,
				"relative_humidity_2m":61,
				"precipitation":0,
				"weather_code":1,
				"cloud_cover":35,
				"wind_speed_10m":12.3,
				"wind_direction_10m":90
			},
			"current_units":{
				"temperature_2m":"C",
				"apparent_temperature":"C",
				"relative_humidity_2m":"%",
				"precipitation":"mm",
				"cloud_cover":"%",
				"wind_speed_10m":"km/h",
				"wind_direction_10m":"deg"
			},
			"daily":{
				"time":["2026-05-23"],
				"weather_code":[1],
				"temperature_2m_max":[27.2],
				"temperature_2m_min":[20.8],
				"precipitation_probability_max":[20]
			},
			"daily_units":{
				"temperature_2m_max":"C",
				"temperature_2m_min":"C",
				"precipitation_probability_max":"%"
			}
		}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	tool := &WeatherTool{
		client:       server.Client(),
		geocodingURL: server.URL + "/v1/search",
		forecastURL:  server.URL + "/v1/forecast",
	}

	out, err := tool.Call(context.Background(), `{"location":"Shanghai"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if payload["timezone"] != "Asia/Shanghai" {
		t.Fatalf("timezone = %#v, want Asia/Shanghai", payload["timezone"])
	}
	if !strings.Contains(out, "Mainly clear") {
		t.Fatalf("expected weather condition in output: %s", out)
	}
}

func TestWeatherToolAcceptsCoordinatesWithoutGeocoding(t *testing.T) {
	geocodeCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, r *http.Request) {
		geocodeCalled = true
		http.Error(w, "should not geocode", http.StatusInternalServerError)
	})
	mux.HandleFunc("/v1/forecast", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"timezone":"UTC","current":{"time":"2026-05-23T00:00","temperature_2m":1,"weather_code":0},"current_units":{"temperature_2m":"C"},"daily":{"time":[]},"daily_units":{}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	tool := &WeatherTool{
		client:       server.Client(),
		geocodingURL: server.URL + "/v1/search",
		forecastURL:  server.URL + "/v1/forecast",
	}

	out, err := tool.Call(context.Background(), `{"latitude":1,"longitude":2,"location":"point"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if geocodeCalled {
		t.Fatal("geocoding endpoint was called despite coordinates")
	}
	if !strings.Contains(out, `"timezone": "UTC"`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestWeatherToolPropagatesContextCancellation(t *testing.T) {
	tool := &WeatherTool{
		client:      http.DefaultClient,
		forecastURL: "https://example.invalid/v1/forecast",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tool.Call(ctx, `{"latitude":1,"longitude":2,"location":"point"}`)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Call error = %v, want context.Canceled", err)
	}
}

func TestWeatherToolRejectsPartialCoordinates(t *testing.T) {
	tool := NewWeatherTool(ProxyConfig{})

	out, err := tool.Call(context.Background(), `{"location":"point","latitude":1}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "latitude and longitude must be provided together") {
		t.Fatalf("unexpected output: %s", out)
	}
}
