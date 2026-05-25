package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultWeatherGeocodeURL  = "https://geocoding-api.open-meteo.com/v1/search"
	defaultWeatherForecastURL = "https://api.open-meteo.com/v1/forecast"
	defaultWeatherWttrURL     = "https://wttr.in"
)

// GetWeatherTool resolves current weather for a location with Open-Meteo first
// and falls back to wttr.in when geocoding/forecast fetch fails.
type GetWeatherTool struct {
	client      *http.Client
	geocodeURL  string
	forecastURL string
	wttrURL     string
}

func NewGetWeatherTool() *GetWeatherTool {
	return NewGetWeatherToolWithConfig(nil, "", "", "")
}

func NewGetWeatherToolWithConfig(
	client *http.Client,
	geocodeURL string,
	forecastURL string,
	wttrURL string,
) *GetWeatherTool {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	if strings.TrimSpace(geocodeURL) == "" {
		geocodeURL = defaultWeatherGeocodeURL
	}
	if strings.TrimSpace(forecastURL) == "" {
		forecastURL = defaultWeatherForecastURL
	}
	if strings.TrimSpace(wttrURL) == "" {
		wttrURL = defaultWeatherWttrURL
	}
	return &GetWeatherTool{
		client:      client,
		geocodeURL:  strings.TrimRight(geocodeURL, "/"),
		forecastURL: strings.TrimRight(forecastURL, "/"),
		wttrURL:     strings.TrimRight(wttrURL, "/"),
	}
}

func (t *GetWeatherTool) Name() string {
	return "get_weather"
}

func (t *GetWeatherTool) Description() string {
	return "Get current weather for a location. Uses Open-Meteo first with timezone-aware output and falls back to wttr.in if needed."
}

func (t *GetWeatherTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"location": map[string]any{
				"type":        "string",
				"description": "City or place name, e.g. Mumbai or New York.",
			},
			"unit": map[string]any{
				"type":        "string",
				"description": "Optional unit system: metric or imperial.",
			},
		},
		"required": []string{"location"},
	}
}

func (t *GetWeatherTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	location, _ := args["location"].(string)
	location = strings.TrimSpace(location)
	if location == "" {
		return ErrorResult("location is required")
	}

	unit := "metric"
	if raw, ok := args["unit"].(string); ok {
		v := strings.ToLower(strings.TrimSpace(raw))
		if v == "imperial" || v == "metric" {
			unit = v
		}
	}

	openMeteo, openErr := t.fetchOpenMeteo(ctx, location, unit)
	if openErr == nil {
		return openMeteo
	}

	wttr, wttrErr := t.fetchWttr(ctx, location)
	if wttrErr == nil {
		return wttr
	}

	return ErrorResult(fmt.Sprintf("weather lookup failed (open-meteo: %v; wttr: %v)", openErr, wttrErr))
}

func (t *GetWeatherTool) fetchOpenMeteo(ctx context.Context, location, unit string) (*ToolResult, error) {
	geoURL := t.geocodeURL + "?name=" + url.QueryEscape(location) + "&count=1&language=en&format=json"
	geoBody, err := t.httpGet(ctx, geoURL)
	if err != nil {
		return nil, fmt.Errorf("geocode request failed: %w", err)
	}

	var geoResp struct {
		Results []struct {
			Name      string  `json:"name"`
			Country   string  `json:"country"`
			Admin1    string  `json:"admin1"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Timezone  string  `json:"timezone"`
		} `json:"results"`
	}
	if err := json.Unmarshal(geoBody, &geoResp); err != nil {
		return nil, fmt.Errorf("geocode decode failed: %w", err)
	}
	if len(geoResp.Results) == 0 {
		return nil, fmt.Errorf("location not found")
	}
	match := geoResp.Results[0]

	tempUnit := "celsius"
	windUnit := "kmh"
	if unit == "imperial" {
		tempUnit = "fahrenheit"
		windUnit = "mph"
	}

	forecastURL := fmt.Sprintf(
		"%s?latitude=%s&longitude=%s&current=temperature_2m,relative_humidity_2m,weather_code,wind_speed_10m&timezone=auto&temperature_unit=%s&wind_speed_unit=%s",
		t.forecastURL,
		strconv.FormatFloat(match.Latitude, 'f', 5, 64),
		strconv.FormatFloat(match.Longitude, 'f', 5, 64),
		tempUnit,
		windUnit,
	)
	forecastBody, err := t.httpGet(ctx, forecastURL)
	if err != nil {
		return nil, fmt.Errorf("forecast request failed: %w", err)
	}

	var forecastResp struct {
		Timezone string `json:"timezone"`
		Current  struct {
			Time             string  `json:"time"`
			Temperature2M    float64 `json:"temperature_2m"`
			RelativeHumidity float64 `json:"relative_humidity_2m"`
			WeatherCode      int     `json:"weather_code"`
			WindSpeed10M     float64 `json:"wind_speed_10m"`
		} `json:"current"`
	}
	if err := json.Unmarshal(forecastBody, &forecastResp); err != nil {
		return nil, fmt.Errorf("forecast decode failed: %w", err)
	}

	payload := map[string]any{
		"source": "open-meteo",
		"location": map[string]any{
			"query":     location,
			"name":      match.Name,
			"region":    match.Admin1,
			"country":   match.Country,
			"timezone":  firstNonEmptyString(forecastResp.Timezone, match.Timezone),
			"latitude":  match.Latitude,
			"longitude": match.Longitude,
		},
		"current": map[string]any{
			"time":                 forecastResp.Current.Time,
			"temperature":          forecastResp.Current.Temperature2M,
			"temperature_unit":     tempUnit,
			"relative_humidity":    forecastResp.Current.RelativeHumidity,
			"weather_code":         forecastResp.Current.WeatherCode,
			"weather_description":  openMeteoWeatherDescription(forecastResp.Current.WeatherCode),
			"wind_speed":           forecastResp.Current.WindSpeed10M,
			"wind_speed_unit":      windUnit,
			"resolved_from":        "open-meteo",
			"location_match_score": "single-best",
		},
	}

	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode weather response failed: %w", err)
	}

	return &ToolResult{
		ForLLM: string(body),
		ForUser: fmt.Sprintf(
			"%s, %s: %.1f %s, %s, humidity %.0f%%, wind %.1f %s.",
			firstNonEmptyString(match.Name, location),
			firstNonEmptyString(match.Country, "unknown country"),
			forecastResp.Current.Temperature2M,
			tempUnit,
			openMeteoWeatherDescription(forecastResp.Current.WeatherCode),
			forecastResp.Current.RelativeHumidity,
			forecastResp.Current.WindSpeed10M,
			windUnit,
		),
	}, nil
}

func (t *GetWeatherTool) fetchWttr(ctx context.Context, location string) (*ToolResult, error) {
	wttrURL := t.wttrURL + "/" + url.QueryEscape(location) + "?format=j1"
	body, err := t.httpGet(ctx, wttrURL)
	if err != nil {
		return nil, err
	}

	var wttrResp struct {
		CurrentCondition []struct {
			TempC       string `json:"temp_C"`
			TempF       string `json:"temp_F"`
			Humidity    string `json:"humidity"`
			WindKmph    string `json:"windspeedKmph"`
			WindMph     string `json:"windspeedMiles"`
			WeatherDesc []struct {
				Value string `json:"value"`
			} `json:"weatherDesc"`
			ObservationTime string `json:"observation_time"`
		} `json:"current_condition"`
		NearestArea []struct {
			AreaName []struct {
				Value string `json:"value"`
			} `json:"areaName"`
			Country []struct {
				Value string `json:"value"`
			} `json:"country"`
		} `json:"nearest_area"`
	}
	if err := json.Unmarshal(body, &wttrResp); err != nil {
		return nil, fmt.Errorf("wttr decode failed: %w", err)
	}
	if len(wttrResp.CurrentCondition) == 0 {
		return nil, fmt.Errorf("wttr current_condition missing")
	}
	current := wttrResp.CurrentCondition[0]

	area := location
	country := ""
	if len(wttrResp.NearestArea) > 0 {
		if len(wttrResp.NearestArea[0].AreaName) > 0 {
			if v := strings.TrimSpace(wttrResp.NearestArea[0].AreaName[0].Value); v != "" {
				area = v
			}
		}
		if len(wttrResp.NearestArea[0].Country) > 0 {
			country = strings.TrimSpace(wttrResp.NearestArea[0].Country[0].Value)
		}
	}
	description := ""
	if len(current.WeatherDesc) > 0 {
		description = strings.TrimSpace(current.WeatherDesc[0].Value)
	}

	payload := map[string]any{
		"source": "wttr.in",
		"location": map[string]any{
			"query":   location,
			"name":    area,
			"country": country,
		},
		"current": map[string]any{
			"temperature_c": current.TempC,
			"temperature_f": current.TempF,
			"humidity":      current.Humidity,
			"wind_kmph":     current.WindKmph,
			"wind_mph":      current.WindMph,
			"description":   description,
			"observed_at":   current.ObservationTime,
		},
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode wttr response failed: %w", err)
	}

	return &ToolResult{
		ForLLM: string(encoded),
		ForUser: fmt.Sprintf(
			"%s%s: %s C (%s F), %s, humidity %s%%, wind %s km/h.",
			area,
			withCountry(country),
			current.TempC,
			current.TempF,
			firstNonEmptyString(description, "weather unavailable"),
			current.Humidity,
			current.WindKmph,
		),
	}, nil
}

func (t *GetWeatherTool) httpGet(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func openMeteoWeatherDescription(code int) string {
	switch code {
	case 0:
		return "clear sky"
	case 1, 2, 3:
		return "partly cloudy"
	case 45, 48:
		return "fog"
	case 51, 53, 55:
		return "drizzle"
	case 61, 63, 65:
		return "rain"
	case 66, 67:
		return "freezing rain"
	case 71, 73, 75, 77:
		return "snow"
	case 80, 81, 82:
		return "rain showers"
	case 85, 86:
		return "snow showers"
	case 95, 96, 99:
		return "thunderstorm"
	default:
		return "unknown"
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func withCountry(country string) string {
	country = strings.TrimSpace(country)
	if country == "" {
		return ""
	}
	return ", " + country
}
