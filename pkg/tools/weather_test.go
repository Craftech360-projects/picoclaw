package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetWeatherToolUsesOpenMeteoFirst(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/geo", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"name":"Mumbai","country":"India","admin1":"Maharashtra","latitude":19.07,"longitude":72.88,"timezone":"Asia/Kolkata"}]}`))
	})
	mux.HandleFunc("/forecast", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"timezone":"Asia/Kolkata","current":{"time":"2026-05-25T16:30","temperature_2m":33.4,"relative_humidity_2m":62,"weather_code":1,"wind_speed_10m":14.2}}`))
	})
	mux.HandleFunc("/wttr", func(w http.ResponseWriter, _ *http.Request) {
		t.Fatalf("wttr fallback should not be called when open-meteo succeeds")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	tool := NewGetWeatherToolWithConfig(
		&http.Client{Timeout: 2 * time.Second},
		server.URL+"/geo",
		server.URL+"/forecast",
		server.URL+"/wttr",
	)
	result := tool.Execute(context.Background(), map[string]any{"location": "Mumbai"})
	if result == nil || result.IsError {
		t.Fatalf("result = %#v, want success", result)
	}
	if !strings.Contains(result.ForLLM, `"source": "open-meteo"`) {
		t.Fatalf("expected open-meteo payload, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForUser, "Mumbai") {
		t.Fatalf("expected user-friendly location output, got: %s", result.ForUser)
	}
}

func TestGetWeatherToolFallsBackToWttr(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/geo", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`upstream unavailable`))
	})
	mux.HandleFunc("/wttr/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"current_condition":[{"temp_C":"27","temp_F":"81","humidity":"70","windspeedKmph":"11","windspeedMiles":"7","weatherDesc":[{"value":"Partly cloudy"}],"observation_time":"09:10 AM"}],"nearest_area":[{"areaName":[{"value":"Chennai"}],"country":[{"value":"India"}]}]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	tool := NewGetWeatherToolWithConfig(
		&http.Client{Timeout: 2 * time.Second},
		server.URL+"/geo",
		server.URL+"/forecast",
		server.URL+"/wttr",
	)
	result := tool.Execute(context.Background(), map[string]any{"location": "Chennai"})
	if result == nil || result.IsError {
		t.Fatalf("result = %#v, want fallback success", result)
	}
	if !strings.Contains(result.ForLLM, `"source": "wttr.in"`) {
		t.Fatalf("expected wttr fallback payload, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForUser, "Chennai") {
		t.Fatalf("expected Chennai in user output, got: %s", result.ForUser)
	}
}
