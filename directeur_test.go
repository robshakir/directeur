package main

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/tormoder/fit"
)

func TestSelectThemeName(t *testing.T) {
	tests := []struct {
		name     string
		date     time.Time
		expected string
	}{
		{"March Classics Flandrian", time.Date(2026, time.March, 15, 10, 0, 0, 0, time.UTC), "Classics Flandrian"},
		{"April Giro Pink", time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC), "Giro Pink"},
		{"May Giro Pink", time.Date(2026, time.May, 20, 10, 0, 0, 0, time.UTC), "Giro Pink"},
		{"June Tour Yellow", time.Date(2026, time.June, 10, 10, 0, 0, 0, time.UTC), "Tour Yellow"},
		{"July Tour Yellow", time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC), "Tour Yellow"},
		{"August Vuelta Red", time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC), "Vuelta Red"},
		{"September Vuelta Red", time.Date(2026, time.September, 5, 10, 0, 0, 0, time.UTC), "Vuelta Red"},
		{"October Carbon Dark", time.Date(2026, time.October, 10, 10, 0, 0, 0, time.UTC), "Carbon Dark"},
		{"January Carbon Dark", time.Date(2026, time.January, 1, 10, 0, 0, 0, time.UTC), "Carbon Dark"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectThemeName(tt.date)
			if got != tt.expected {
				t.Errorf("selectThemeName(%v) = %q, expected %q", tt.date, got, tt.expected)
			}
		})
	}
}

func TestSortEvents(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 10, 0, 5, 0, time.UTC)
	t3 := time.Date(2026, 1, 1, 10, 0, 10, 0, time.UTC)

	events := []*fit.EventMsg{
		{Timestamp: t3},
		{Timestamp: t1},
		{Timestamp: t2},
	}

	sortEvents(events)

	if !events[0].Timestamp.Equal(t1) || !events[1].Timestamp.Equal(t2) || !events[2].Timestamp.Equal(t3) {
		t.Errorf("Events not sorted correctly. Got order: %v, %v, %v", events[0].Timestamp, events[1].Timestamp, events[2].Timestamp)
	}
}

func TestCompute30sPower(t *testing.T) {
	records := make([]*fit.RecordMsg, 50)
	rawPowers := make([]int, 50)
	for i := 0; i < 50; i++ {
		records[i] = &fit.RecordMsg{
			Timestamp: time.Date(2026, 1, 1, 10, 0, i, 0, time.UTC),
			Power:     uint16(100 + i),
		}
		rawPowers[i] = 100 + i
	}

	got := compute30sPower(records, rawPowers, 40)

	sum := 0
	for i := 11; i <= 40; i++ {
		sum += 100 + i
	}
	expected := sum / 30

	if got != expected {
		t.Errorf("compute30sPower at index 40 = %d, expected %d", got, expected)
	}

	gotBoundary := compute30sPower(records, rawPowers, 10)
	sumBoundary := 0
	for i := 0; i <= 10; i++ {
		sumBoundary += 100 + i
	}
	expectedBoundary := sumBoundary / 11
	if gotBoundary != expectedBoundary {
		t.Errorf("compute30sPower at boundary index 10 = %d, expected %d", gotBoundary, expectedBoundary)
	}
}

func TestCalculateNormalizedPower(t *testing.T) {
	powersConstant := make([]int, 100)
	for i := range powersConstant {
		powersConstant[i] = 200
	}
	gotConstant := calculateNormalizedPower(powersConstant)
	if gotConstant != 200 {
		t.Errorf("calculateNormalizedPower constant 200W = %d, expected 200", gotConstant)
	}

	powersVariable := make([]int, 100)
	for i := range powersVariable {
		if i%2 == 0 {
			powersVariable[i] = 100
		} else {
			powersVariable[i] = 300
		}
	}
	gotVariable := calculateNormalizedPower(powersVariable)

	expectedVariable := int(math.Round(math.Pow(4100000000, 0.25)))
	if gotVariable != expectedVariable {
		t.Errorf("calculateNormalizedPower variable 100/300W = %d, expected %d", gotVariable, expectedVariable)
	}
}

func TestExtractUserIDFromJWT(t *testing.T) {
	claims := map[string]interface{}{
		"sub": "athlete_sub_12345",
		"iat": 1234567890,
	}
	claimsBytes, _ := json.Marshal(claims)
	encodedPayload := base64.RawURLEncoding.EncodeToString(claimsBytes)

	validToken := "header." + encodedPayload + ".signature"

	got, err := extractUserIDFromJWT(validToken)
	if err != nil {
		t.Fatalf("Unexpected error extracting user ID: %v", err)
	}
	if got != "athlete_sub_12345" {
		t.Errorf("extractUserIDFromJWT = %q, expected %q", got, "athlete_sub_12345")
	}

	claimsPadding := map[string]interface{}{
		"sub": "user",
	}
	paddingBytes, _ := json.Marshal(claimsPadding)
	encodedPadding := base64.RawURLEncoding.EncodeToString(paddingBytes)
	tokenPadding := "header." + encodedPadding + ".signature"
	gotPadding, err := extractUserIDFromJWT(tokenPadding)
	if err != nil {
		t.Fatalf("Unexpected error on padded token: %v", err)
	}
	if gotPadding != "user" {
		t.Errorf("extractUserIDFromJWT on padded = %q, expected %q", gotPadding, "user")
	}

	invalidFormatToken := "header_no_dots"
	_, errFormat := extractUserIDFromJWT(invalidFormatToken)
	if errFormat == nil {
		t.Error("Expected error for invalid token format (no dots)")
	}

	invalidBase64Token := "header.invalid_base64_$&^.signature"
	_, errBase64 := extractUserIDFromJWT(invalidBase64Token)
	if errBase64 == nil {
		t.Error("Expected error for invalid base64 payload")
	}

	missingSubToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"name":"test"}`)) + ".signature"
	_, errSub := extractUserIDFromJWT(missingSubToken)
	if errSub == nil {
		t.Error("Expected error for missing sub claim")
	}
}

func TestConfigLoadSave(t *testing.T) {
	tempFile := filepath.Join(os.TempDir(), "directeur_test_config.json")
	defer os.Remove(tempFile)

	mockConfig := Config{
		FrontGears:     []int{34, 50},
		RearGears:      []int{11, 12, 13, 14, 15, 17, 19, 21, 23, 25, 28},
		FTP:            280,
		LocalDirectory: "/test/rides",
		Bikes: []BikeProfile{
			{Name: "Road Bike", FrontGears: []int{34, 50}, RearGears: []int{11, 28}},
		},
	}

	err := saveConfig(tempFile, mockConfig)
	if err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	loaded := loadConfig(tempFile)
	if loaded.FTP != 280 {
		t.Errorf("loadConfig FTP = %d, expected 280", loaded.FTP)
	}
	if !reflect.DeepEqual(loaded.FrontGears, mockConfig.FrontGears) {
		t.Errorf("loadConfig FrontGears = %v, expected %v", loaded.FrontGears, mockConfig.FrontGears)
	}
	if len(loaded.Bikes) != 1 || loaded.Bikes[0].Name != "Road Bike" {
		t.Errorf("loadConfig Bikes = %v, expected one profile with Name 'Road Bike'", loaded.Bikes)
	}

	fallback := loadConfig("non_existent_file_path_12345.json")
	if fallback.FTP != 250 {
		t.Errorf("loadConfig fallback FTP = %d, expected default 250", fallback.FTP)
	}
}

func TestHammerheadActivityUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name            string
		jsonStr         string
		expectedID      string
		expectedName    string
		expectedDist    float64
		expectedDur     float64
		expectedTimeStr string
	}{
		{
			"Standard Feed JSON",
			`{"id": "activity_abc123", "name": "Morning Ride", "distance": 42000.5, "duration": 7200000.0, "startTime": "2026-06-01T08:00:00Z"}`,
			"activity_abc123", "Morning Ride", 42000.5, 7200.0, "2026-06-01T08:00:00Z",
		},
		{
			"Alternate Feed JSON",
			`{"_id": "activity_db456", "title": "Evening Spin", "distance_meters": 15000.0, "elapsedTime": 3600000.0, "createdAt": "2026-06-02T18:00:00.000Z"}`,
			"activity_db456", "Evening Spin", 15000.0, 3600.0, "2026-06-02T18:00:00.000Z",
		},
		{
			"Legacy Field Mappings",
			`{"activityId": "legacy_789", "name": "Laps", "distance": 5000.0, "duration_seconds": 1200.0, "start_time": "2026-06-03 12:00:00"}`,
			"legacy_789", "Laps", 5000.0, 1200.0, "2026-06-03 12:00:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var act HammerheadActivity
			err := json.Unmarshal([]byte(tt.jsonStr), &act)
			if err != nil {
				t.Fatalf("json.Unmarshal failed: %v", err)
			}

			if act.ID != tt.expectedID {
				t.Errorf("act.ID = %q, expected %q", act.ID, tt.expectedID)
			}
			if act.Name != tt.expectedName {
				t.Errorf("act.Name = %q, expected %q", act.Name, tt.expectedName)
			}
			if act.DistanceMeters != tt.expectedDist {
				t.Errorf("act.DistanceMeters = %v, expected %v", act.DistanceMeters, tt.expectedDist)
			}
			if act.DurationSeconds != tt.expectedDur {
				t.Errorf("act.DurationSeconds = %v, expected %v", act.DurationSeconds, tt.expectedDur)
			}
			if act.StartTimeString != tt.expectedTimeStr {
				t.Errorf("act.StartTimeString = %q, expected %q", act.StartTimeString, tt.expectedTimeStr)
			}

			if act.StartTime.IsZero() {
				t.Error("act.StartTime is zero, expected successfully parsed time.Time")
			}
		})
	}
}

func TestCalculatePowerCurve(t *testing.T) {
	records := make([]*fit.RecordMsg, 70)
	rawPowers := make([]int, 70)
	for i := 0; i < 70; i++ {
		pow := 100
		if i >= 20 && i < 25 {
			pow = 500
		}
		records[i] = &fit.RecordMsg{
			Timestamp: time.Date(2026, 1, 1, 10, 0, i, 0, time.UTC),
			Power:     uint16(pow),
		}
		rawPowers[i] = pow
	}

	curve := calculatePowerCurve(records, rawPowers)

	if curve["1s"] != 500 {
		t.Errorf("Peak 1s power = %d, expected 500", curve["1s"])
	}

	if curve["5s"] != 500 {
		t.Errorf("Peak 5s power = %d, expected 500", curve["5s"])
	}

	if curve["30s"] != 166 {
		t.Errorf("Peak 30s power = %d, expected 166", curve["30s"])
	}

	if curve["1m"] != 133 {
		t.Errorf("Peak 1m power = %d, expected 133", curve["1m"])
	}
}

func TestIntervalsConfig(t *testing.T) {
	tempFile := filepath.Join(os.TempDir(), "directeur_test_intervals_config.json")
	defer os.Remove(tempFile)

	mockConfig := Config{
		FTP: 250,
		IntervalsAPI: IntervalsConfig{
			Enabled:   true,
			AthleteID: "i987654",
			APIKey:    "test_api_key_xyz",
		},
	}

	err := saveConfig(tempFile, mockConfig)
	if err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	loaded := loadConfig(tempFile)
	if !loaded.IntervalsAPI.Enabled {
		t.Error("loadConfig IntervalsAPI.Enabled = false, expected true")
	}
	if loaded.IntervalsAPI.AthleteID != "i987654" {
		t.Errorf("loadConfig IntervalsAPI.AthleteID = %q, expected %q", loaded.IntervalsAPI.AthleteID, "i987654")
	}
	if loaded.IntervalsAPI.APIKey != "test_api_key_xyz" {
		t.Errorf("loadConfig IntervalsAPI.APIKey = %q, expected %q", loaded.IntervalsAPI.APIKey, "test_api_key_xyz")
	}
}
