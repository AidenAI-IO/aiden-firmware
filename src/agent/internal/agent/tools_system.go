package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const (
	defaultWeatherGeocodingURL = "https://geocoding-api.open-meteo.com/v1/search"
	defaultWeatherForecastURL  = "https://api.open-meteo.com/v1/forecast"
	defaultWeatherForecastDays = 3
)

type CurrentTimeTool struct {
	now func() time.Time
}

func NewCurrentTimeTool() *CurrentTimeTool {
	return &CurrentTimeTool{now: time.Now}
}

func (t *CurrentTimeTool) Name() string { return "current_time" }

func (t *CurrentTimeTool) Description() string {
	return `Get the current date and time. Input JSON: {"timezone":"Asia/Shanghai"} or a bare timezone string. ` +
		`The timezone may be an IANA name such as "America/New_York", "UTC", "local", or a UTC offset such as "+08:00".`
}

func (t *CurrentTimeTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"timezone": stringArgSchema(`IANA timezone such as "Asia/Shanghai", "UTC", "local", or a UTC offset such as "+08:00".`),
	})
}

func (t *CurrentTimeTool) Call(ctx context.Context, input string) (string, error) {
	timezone := strings.TrimSpace(input)
	if strings.HasPrefix(timezone, "{") {
		var args struct {
			Timezone string `json:"timezone"`
		}
		if err := json.Unmarshal([]byte(timezone), &args); err != nil {
			return fmt.Sprintf("error: invalid input: %v. Expected JSON format: {\"timezone\": \"Asia/Shanghai\"} or a bare timezone string like \"UTC\" or \"+08:00\"", err), nil
		}
		timezone = strings.TrimSpace(args.Timezone)
	}

	loc, normalized, err := parseTimezone(timezone)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	nowFn := t.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn().In(loc)
	_, offsetSeconds := now.Zone()

	return jsonString(map[string]interface{}{
		"timezone":       normalized,
		"time":           now.Format(time.RFC3339),
		"date":           now.Format("2006-01-02"),
		"clock":          now.Format("15:04:05"),
		"weekday":        now.Weekday().String(),
		"unix":           now.Unix(),
		"utc_offset":     formatUTCOffset(offsetSeconds),
		"offset_seconds": offsetSeconds,
	}), nil
}

func parseTimezone(input string) (*time.Location, string, error) {
	timezone := strings.TrimSpace(input)
	if timezone == "" || strings.EqualFold(timezone, "local") {
		return time.Local, "local", nil
	}
	if strings.EqualFold(timezone, "utc") || timezone == "Z" {
		return time.UTC, "UTC", nil
	}
	if loc, err := time.LoadLocation(timezone); err == nil {
		return loc, timezone, nil
	}
	if seconds, ok := parseUTCOffset(timezone); ok {
		name := "UTC" + formatUTCOffset(seconds)
		return time.FixedZone(name, seconds), name, nil
	}
	return nil, "", fmt.Errorf("unknown timezone %q; use an IANA timezone like Asia/Shanghai or an offset like +08:00", timezone)
}

var utcOffsetPattern = regexp.MustCompile(`(?i)^(?:UTC|GMT)?([+-])(\d{1,2})(?::?(\d{2}))?$`)

func parseUTCOffset(input string) (int, bool) {
	matches := utcOffsetPattern.FindStringSubmatch(strings.TrimSpace(input))
	if matches == nil {
		return 0, false
	}
	hours, err := strconv.Atoi(matches[2])
	if err != nil || hours > 23 {
		return 0, false
	}
	minutes := 0
	if matches[3] != "" {
		minutes, err = strconv.Atoi(matches[3])
		if err != nil || minutes > 59 {
			return 0, false
		}
	}
	total := hours*3600 + minutes*60
	if matches[1] == "-" {
		total = -total
	}
	return total, true
}

func formatUTCOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return fmt.Sprintf("%s%02d:%02d", sign, seconds/3600, (seconds%3600)/60)
}

type WeatherTool struct {
	client       *http.Client
	geocodingURL string
	forecastURL  string
}

func NewWeatherTool(proxy ProxyConfig) *WeatherTool {
	client := newProxyHTTPClient(proxy)
	client.Timeout = defaultWebToolTimeout
	return &WeatherTool{
		client:       client,
		geocodingURL: defaultWeatherGeocodingURL,
		forecastURL:  defaultWeatherForecastURL,
	}
}

func (t *WeatherTool) Name() string { return "weather" }

func (t *WeatherTool) Description() string {
	return `Get current weather and a short forecast for a location. ` +
		`Input JSON: {"location":"Shanghai"} or {"latitude":31.23,"longitude":121.47,"location":"Shanghai"}. ` +
		`A bare location string is also accepted. Uses Open-Meteo public weather data.`
}

func (t *WeatherTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"location":  stringArgSchema("Location name to geocode, or a display name when latitude/longitude are provided."),
		"latitude":  rangedNumberArgSchema("Latitude in decimal degrees.", -90, 90),
		"longitude": rangedNumberArgSchema("Longitude in decimal degrees.", -180, 180),
	})
}

func (t *WeatherTool) Call(ctx context.Context, input string) (string, error) {
	args, err := parseWeatherArgs(input)
	if err != nil {
		return "error: " + err.Error(), nil
	}

	callCtx, cancel := contextWithDefaultTimeout(ctx)
	defer cancel()

	locationName := args.Location
	var lat float64
	var lon float64
	hasCoordinates := args.hasCoordinates()
	if hasCoordinates {
		lat = *args.Latitude
		lon = *args.Longitude
	} else {
		geo, err := t.geocode(callCtx, args.Location)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		locationName = geo.DisplayName()
		lat = geo.Latitude
		lon = geo.Longitude
	}

	forecast, err := t.fetchForecast(callCtx, lat, lon)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	result := map[string]interface{}{
		"location": map[string]interface{}{
			"name":      locationName,
			"latitude":  roundFloat(lat, 4),
			"longitude": roundFloat(lon, 4),
		},
		"timezone": forecast.Timezone,
		"current":  forecast.CurrentSummary(),
		"daily":    forecast.DailySummaries(),
		"source":   "Open-Meteo",
	}
	return jsonString(result), nil
}

type weatherArgs struct {
	Location  string   `json:"location"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

func (a weatherArgs) hasCoordinates() bool {
	return a.Latitude != nil && a.Longitude != nil
}

func parseWeatherArgs(input string) (weatherArgs, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return weatherArgs{}, fmt.Errorf("location is required")
	}
	if strings.HasPrefix(trimmed, "{") {
		var args weatherArgs
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			return weatherArgs{}, fmt.Errorf("invalid input: %w", err)
		}
		args.Location = strings.TrimSpace(args.Location)
		if (args.Latitude == nil) != (args.Longitude == nil) {
			return weatherArgs{}, fmt.Errorf("latitude and longitude must be provided together")
		}
		if args.hasCoordinates() {
			if err := validateCoordinates(*args.Latitude, *args.Longitude); err != nil {
				return weatherArgs{}, err
			}
			if args.Location == "" {
				args.Location = fmt.Sprintf("%.4f, %.4f", *args.Latitude, *args.Longitude)
			}
			return args, nil
		}
		if args.Location == "" {
			return weatherArgs{}, fmt.Errorf("location is required unless latitude and longitude are provided")
		}
		return args, nil
	}
	return weatherArgs{Location: trimmed}, nil
}

func validateCoordinates(lat, lon float64) error {
	if lat < -90 || lat > 90 {
		return fmt.Errorf("latitude must be between -90 and 90")
	}
	if lon < -180 || lon > 180 {
		return fmt.Errorf("longitude must be between -180 and 180")
	}
	return nil
}

type openMeteoGeocodingResponse struct {
	Results []openMeteoLocation `json:"results"`
}

type openMeteoLocation struct {
	Name      string  `json:"name"`
	Country   string  `json:"country"`
	Admin1    string  `json:"admin1"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func (l openMeteoLocation) DisplayName() string {
	parts := make([]string, 0, 3)
	for _, part := range []string{l.Name, l.Admin1, l.Country} {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, ", ")
}

func (t *WeatherTool) geocode(ctx context.Context, location string) (openMeteoLocation, error) {
	if strings.TrimSpace(location) == "" {
		return openMeteoLocation{}, fmt.Errorf("location is required")
	}
	base, err := url.Parse(defaultString(t.geocodingURL, defaultWeatherGeocodingURL))
	if err != nil {
		return openMeteoLocation{}, err
	}
	q := base.Query()
	q.Set("name", location)
	q.Set("count", "1")
	q.Set("language", "en")
	q.Set("format", "json")
	base.RawQuery = q.Encode()

	var payload openMeteoGeocodingResponse
	if err := t.getJSON(ctx, base.String(), &payload); err != nil {
		return openMeteoLocation{}, fmt.Errorf("geocode location: %w", err)
	}
	if len(payload.Results) == 0 {
		return openMeteoLocation{}, fmt.Errorf("location not found: %s", location)
	}
	return payload.Results[0], nil
}

type openMeteoForecastResponse struct {
	Timezone     string                 `json:"timezone"`
	Current      map[string]interface{} `json:"current"`
	CurrentUnits map[string]string      `json:"current_units"`
	Daily        map[string]interface{} `json:"daily"`
	DailyUnits   map[string]string      `json:"daily_units"`
}

func (r openMeteoForecastResponse) CurrentSummary() map[string]interface{} {
	current := map[string]interface{}{}
	if value, ok := r.Current["time"].(string); ok {
		current["time"] = value
	}
	for _, key := range []string{
		"temperature_2m",
		"apparent_temperature",
		"relative_humidity_2m",
		"precipitation",
		"weather_code",
		"cloud_cover",
		"wind_speed_10m",
		"wind_direction_10m",
	} {
		if value, ok := currentFloat(r.Current[key]); ok {
			current[key] = valueWithUnit(roundFloat(value, 1), r.CurrentUnits[key])
		}
	}
	if value, ok := currentFloat(r.Current["weather_code"]); ok {
		current["condition"] = weatherCodeDescription(int(value))
	}
	return current
}

func currentFloat(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	default:
		return 0, false
	}
}

func (r openMeteoForecastResponse) DailySummaries() []map[string]interface{} {
	times := dailyStringSlice(r.Daily["time"])
	result := make([]map[string]interface{}, 0, len(times))
	for i, day := range times {
		item := map[string]interface{}{"date": day}
		addDailyValue(item, r, "weather_code", i, "condition")
		addDailyValue(item, r, "temperature_2m_max", i, "")
		addDailyValue(item, r, "temperature_2m_min", i, "")
		addDailyValue(item, r, "precipitation_probability_max", i, "")
		result = append(result, item)
	}
	return result
}

func addDailyValue(item map[string]interface{}, forecast openMeteoForecastResponse, key string, index int, alias string) {
	values := dailyFloatSlice(forecast.Daily[key])
	if index >= len(values) {
		return
	}
	outKey := key
	if alias != "" {
		outKey = alias
	}
	if key == "weather_code" {
		item[outKey] = weatherCodeDescription(int(values[index]))
		item[key] = int(values[index])
		return
	}
	item[outKey] = valueWithUnit(roundFloat(values[index], 1), forecast.DailyUnits[key])
}

func dailyStringSlice(value interface{}) []string {
	raw, ok := value.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func dailyFloatSlice(value interface{}) []float64 {
	raw, ok := value.([]interface{})
	if !ok {
		return nil
	}
	result := make([]float64, 0, len(raw))
	for _, item := range raw {
		if f, ok := item.(float64); ok {
			result = append(result, f)
		}
	}
	return result
}

func (t *WeatherTool) fetchForecast(ctx context.Context, lat, lon float64) (openMeteoForecastResponse, error) {
	base, err := url.Parse(defaultString(t.forecastURL, defaultWeatherForecastURL))
	if err != nil {
		return openMeteoForecastResponse{}, err
	}
	q := base.Query()
	q.Set("latitude", strconv.FormatFloat(lat, 'f', 6, 64))
	q.Set("longitude", strconv.FormatFloat(lon, 'f', 6, 64))
	q.Set("current", "temperature_2m,relative_humidity_2m,apparent_temperature,precipitation,weather_code,cloud_cover,wind_speed_10m,wind_direction_10m")
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max")
	q.Set("forecast_days", strconv.Itoa(defaultWeatherForecastDays))
	q.Set("timezone", "auto")
	base.RawQuery = q.Encode()

	var payload openMeteoForecastResponse
	if err := t.getJSON(ctx, base.String(), &payload); err != nil {
		return openMeteoForecastResponse{}, fmt.Errorf("fetch forecast: %w", err)
	}
	return payload, nil
}

func (t *WeatherTool) getJSON(ctx context.Context, rawURL string, target interface{}) error {
	client := t.client
	if client == nil {
		client = &http.Client{Timeout: defaultWebToolTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", defaultUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebToolOutputBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}

func valueWithUnit(value interface{}, unit string) map[string]interface{} {
	result := map[string]interface{}{"value": value}
	if strings.TrimSpace(unit) != "" {
		result["unit"] = unit
	}
	return result
}

func roundFloat(value float64, places int) float64 {
	if places < 0 {
		return value
	}
	pow := math.Pow10(places)
	return math.Round(value*pow) / pow
}

func weatherCodeDescription(code int) string {
	switch code {
	case 0:
		return "Clear sky"
	case 1, 2, 3:
		return "Mainly clear, partly cloudy, or overcast"
	case 45, 48:
		return "Fog"
	case 51, 53, 55:
		return "Drizzle"
	case 56, 57:
		return "Freezing drizzle"
	case 61, 63, 65:
		return "Rain"
	case 66, 67:
		return "Freezing rain"
	case 71, 73, 75:
		return "Snow fall"
	case 77:
		return "Snow grains"
	case 80, 81, 82:
		return "Rain showers"
	case 85, 86:
		return "Snow showers"
	case 95:
		return "Thunderstorm"
	case 96, 99:
		return "Thunderstorm with hail"
	default:
		return fmt.Sprintf("Unknown weather code %d", code)
	}
}

func jsonString(value interface{}) string {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("error: encode response: %v", err)
	}
	return string(payload)
}
