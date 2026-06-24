package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tormoder/fit"
)

// BikeProfile represents a specific bicycle's gear configuration
type BikeProfile struct {
	Name       string `json:"name"`
	FrontGears []int  `json:"front_gears"`
	RearGears  []int  `json:"rear_gears"`
}

// Config represents the gear configuration and data source options
type Config struct {
	FrontGears     []int            `json:"front_gears"`
	RearGears      []int            `json:"rear_gears"`
	Bikes          []BikeProfile    `json:"bikes"`
	LocalDirectory string           `json:"local_directory"`
	HammerheadAPI  HammerheadConfig `json:"hammerhead_api"`
	WahooAPI       WahooConfig      `json:"wahoo_api"`
	IntervalsAPI   IntervalsConfig  `json:"intervals_api"`
	FTP            int              `json:"ftp"`
	MaxHR          int              `json:"max_hr"`
}

// IntervalsConfig represents authentication and connection details for Intervals.icu API integration
type IntervalsConfig struct {
	Enabled     bool   `json:"enabled"`
	AthleteID   string `json:"athlete_id"`
	APIKey      string `json:"api_key"`
	DownloadDir string `json:"download_dir"`
}

// HammerheadConfig represents authentication and caching details for Hammerhead Dashboard API integration
type HammerheadConfig struct {
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthToken    string `json:"auth_token"`
	RefreshToken string `json:"refresh_token"`
	DownloadDir  string `json:"download_dir"`
}

// WahooConfig represents authentication and caching details for Wahoo Fitness Cloud API integration
type WahooConfig struct {
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthToken    string `json:"auth_token"`
	RefreshToken string `json:"refresh_token"`
	DownloadDir  string `json:"download_dir"`
}

// RideSummary contains high-level metrics of the ride
type RideSummary struct {
	StartTime                time.Time      `json:"start_time"`
	DurationSeconds          float64        `json:"duration_seconds"`
	DistanceMeters           float64        `json:"distance_meters"`
	AveragePower             float64        `json:"average_power"`
	MaxPower                 int            `json:"max_power"`
	NormalizedPower          int            `json:"normalized_power"`
	AverageHeartRate         float64        `json:"average_heart_rate"`
	MaxHeartRate             int            `json:"max_heart_rate"`
	AverageCadence           float64        `json:"average_cadence"`
	MaxCadence               int            `json:"max_cadence"`
	TotalElevationGainMeters float64        `json:"total_elevation_gain_meters"`
	MaxAltitudeMeters        float64        `json:"max_altitude_meters"`
	MinAltitudeMeters        float64        `json:"min_altitude_meters"`
	TotalFrontShifts         int            `json:"total_front_shifts"`
	TotalRearShifts          int            `json:"total_rear_shifts"`
	TotalShifts              int            `json:"total_shifts"`
	ThemeName                string         `json:"theme_name"`
	PowerCurve               map[string]int `json:"power_curve"`
	IsZwift                  bool           `json:"is_zwift"`
}

// TelemetryRecord represents a single data point in time
type TelemetryRecord struct {
	Timestamp          time.Time `json:"timestamp"`
	ElapsedTimeSeconds float64   `json:"elapsed_time_seconds"`
	Power              int       `json:"power"`
	Power30s           int       `json:"power_30s"`
	HeartRate          int       `json:"heart_rate"`
	Cadence            int       `json:"cadence"`
	AltitudeMeters     float64   `json:"altitude_meters"`
	SpeedKmh           float64   `json:"speed_kmh"`
	DistanceMeters     float64   `json:"distance_meters"`
	LatitudeDeg        float64   `json:"latitude_deg"`
	LongitudeDeg       float64   `json:"longitude_deg"`
	FrontGearNum       int       `json:"front_gear_num"`
	FrontGearTeeth     int       `json:"front_gear_teeth"`
	RearGearNum        int       `json:"rear_gear_num"`
	RearGearTeeth      int       `json:"rear_gear_teeth"`
	GearRatio          float64   `json:"gear_ratio"`
}

// GearStats details gear usage breakdown
type GearStats struct {
	Combination string  `json:"combination"`
	Seconds     int     `json:"seconds"`
	Percentage  float64 `json:"percentage"`
}

// RideAnalysis is the full JSON output model
type RideAnalysis struct {
	Schema     string            `json:"$schema"`
	SourceFile string            `json:"source_file,omitempty"`
	Summary    RideSummary       `json:"summary"`
	GearUsage  []GearStats       `json:"gear_usage"`
	Records    []TelemetryRecord `json:"records"`
	Source     string            `json:"source,omitempty"`
	Param      string            `json:"param,omitempty"`
	Param2     string            `json:"param2,omitempty"`
}

// GearState tracks shifting status
type GearState struct {
	FrontNum   uint8
	FrontTeeth uint8
	RearNum    uint8
	RearTeeth  uint8
}

func analyzeFITFile(filePath string, config Config) (RideAnalysis, error) {
	var analysis RideAnalysis
	f, err := os.Open(filePath)
	if err != nil {
		return analysis, fmt.Errorf("error opening FIT file: %w", err)
	}
	defer f.Close()

	fitFile, err := fit.Decode(f)
	if err != nil {
		return analysis, fmt.Errorf("error decoding FIT file: %w", err)
	}

	activity, err := fitFile.Activity()
	if err != nil {
		return analysis, fmt.Errorf("error parsing activity from FIT file: %w", err)
	}

	if len(activity.Records) == 0 {
		return analysis, fmt.Errorf("no records found in the FIT file")
	}

	// Process Gears and Shifting Timeline
	sortEvents(activity.Events)
	gearTimeline := buildGearTimeline(activity.Records, activity.Events, config)

	// Process Telemetry Records
	analysis = processRecords(activity.Records, gearTimeline)

	isZwift := false
	if fitFile.FileId.Manufacturer.String() == "Zwift" ||
		strings.Contains(strings.ToLower(fitFile.FileId.ProductName), "zwift") ||
		strings.Contains(strings.ToLower(filepath.Base(filePath)), "zwift") {
		isZwift = true
	}
	analysis.Summary.IsZwift = isZwift

	// Select Theme Name based on ride start month
	analysis.Summary.ThemeName = selectThemeName(analysis.Summary.StartTime)
	analysis.Schema = "https://raw.githubusercontent.com/robshakir/directeur/main/schema.json"
	analysis.SourceFile = filepath.Base(filePath)

	return analysis, nil
}

func resolveConfigPath(flagVal string, configPassed bool) string {
	if configPassed {
		return flagVal
	}
	if dataDir := os.Getenv("DIRECTEUR_DATA_DIR"); dataDir != "" {
		return filepath.Join(dataDir, "config.json")
	}
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		homeDirConfig := filepath.Join(homeDir, ".directeur", "config.json")
		homeConfig := filepath.Join(homeDir, ".directeur.config.json")
		if _, err := os.Stat(homeDirConfig); err == nil {
			return homeDirConfig
		} else if _, err := os.Stat(homeConfig); err == nil {
			return homeConfig
		}
	}
	return flagVal
}

func main() {
	inputFile := flag.String("input", "example.fit", "Path to input .FIT file")
	configFile := flag.String("config", "config.json", "Path to gear configuration file")
	outputJSON := flag.String("output-json", "ride_analysis.json", "Path to output JSON file")
	outputHTML := flag.String("output-html", "ride_dashboard.html", "Path to output HTML dashboard file")
	serveMode := flag.Bool("serve", false, "Start a local web server to display the dashboard")
	defaultPort := 8080
	if envPort := os.Getenv("PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			defaultPort = p
		}
	}
	bikeName := flag.String("bike", "", "Name of the bike profile from the config to use for analysis")
	port := flag.Int("port", defaultPort, "Port for the local web server")

	flag.Parse()

	// 1. Load config
	configPassed := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			configPassed = true
		}
	})
	resolvedConfigPath := resolveConfigPath(*configFile, configPassed)

	config := loadConfig(resolvedConfigPath)

	if *bikeName != "" {
		found := false
		for _, b := range config.Bikes {
			if b.Name == *bikeName {
				config.FrontGears = b.FrontGears
				config.RearGears = b.RearGears
				found = true
				break
			}
		}
		if !found {
			fmt.Printf("Error: Bike profile '%s' not found in configuration\n", *bikeName)
			os.Exit(1)
		}
		fmt.Printf("Selected bike profile: %s. Front rings: %v, Rear cogs: %v\n", *bikeName, config.FrontGears, config.RearGears)
	} else {
		fmt.Printf("Loaded gear configuration: Front rings: %v, Rear cogs: %v\n", config.FrontGears, config.RearGears)
	}

	var resolvedInputFile string
	var resolvedAnalysis RideAnalysis
	var resolveErr error
	var hasData bool
	var startupSource string = "local"
	var startupParam string = ""
	var startupParam2 string = ""

	inputPassed := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "input" {
			inputPassed = true
		}
	})

	if inputPassed {
		resolvedInputFile = *inputFile
		hasData = true
		startupSource = "local"
		startupParam = filepath.Base(*inputFile)
	} else {
		// Try Hammerhead if enabled
		if config.HammerheadAPI.Enabled && (config.HammerheadAPI.AuthToken != "" || config.HammerheadAPI.RefreshToken != "") {
			fmt.Println("Hammerhead API enabled, fetching activities...")
			activities, _, _, err := fetchHammerheadActivities(config.HammerheadAPI, resolvedConfigPath, 1)
			if err == nil && len(activities) > 0 {
				fmt.Printf("Downloading newest Hammerhead activity: %s (%s)...\n", activities[0].Name, activities[0].ID)
				filePath, err := downloadHammerheadFITFile(config.HammerheadAPI, resolvedConfigPath, activities[0].ID)
				if err == nil {
					resolvedInputFile = filePath
					hasData = true
					startupSource = "hammerhead"
					startupParam = activities[0].ID
				} else {
					fmt.Printf("Error downloading Hammerhead activity: %v\n", err)
				}
			} else if err != nil {
				fmt.Printf("Error fetching Hammerhead activities: %v\n", err)
			}
		}

		// Try Wahoo if enabled and not resolved yet
		if !hasData && config.WahooAPI.Enabled && (config.WahooAPI.AuthToken != "" || config.WahooAPI.RefreshToken != "") {
			fmt.Println("Wahoo API enabled, fetching workouts...")
			workouts, _, _, err := fetchWahooWorkouts(config.WahooAPI, resolvedConfigPath, 1)
			if err == nil && len(workouts) > 0 {
				fmt.Printf("Downloading newest Wahoo activity: %s (%d)...\n", workouts[0].Name, workouts[0].ID)
				filePath, err := downloadWahooFITFile(config.WahooAPI, workouts[0].File.URL, workouts[0].ID)
				if err == nil {
					resolvedInputFile = filePath
					hasData = true
					startupSource = "wahoo"
					startupParam = strconv.FormatInt(workouts[0].ID, 10)
					startupParam2 = workouts[0].File.URL
				} else {
					fmt.Printf("Error downloading Wahoo activity: %v\n", err)
				}
			} else if err != nil {
				fmt.Printf("Error fetching Wahoo workouts: %v\n", err)
			}
		}

		// Try Local Directory if not resolved yet
		if !hasData && config.LocalDirectory != "" {
			fmt.Printf("Scanning local directory: %s...\n", config.LocalDirectory)
			localRides, err := listLocalRides(config.LocalDirectory)
			if err == nil && len(localRides) > 0 {
				resolvedInputFile = filepath.Join(config.LocalDirectory, localRides[0].Filename)
				hasData = true
				startupSource = "local"
				startupParam = localRides[0].Filename
			} else if err != nil {
				fmt.Printf("Error listing local directory: %v\n", err)
			}
		}

		// Fallback to default inputFile if it exists and no other source was resolved/configured
		if !hasData {
			if _, err := os.Stat(*inputFile); err == nil {
				resolvedInputFile = *inputFile
				hasData = true
				startupSource = "local"
				startupParam = filepath.Base(*inputFile)
			}
		}
	}

	if !hasData {
		fmt.Println("No data found")
		if !*serveMode {
			os.Exit(0)
		}
	} else {
		fmt.Printf("Parsing FIT file: %s...\n", resolvedInputFile)
		resolvedAnalysis, resolveErr = analyzeFITFile(resolvedInputFile, config)
		if resolveErr != nil {
			fmt.Printf("Error analyzing FIT file: %v\n", resolveErr)
			if !*serveMode {
				os.Exit(1)
			}
		}
	}

	// Write JSON Output if parsed successfully
	if hasData && resolveErr == nil {
		resolvedAnalysis.Source = startupSource
		resolvedAnalysis.Param = startupParam
		resolvedAnalysis.Param2 = startupParam2

		fmt.Printf("Writing JSON analysis to %s...\n", *outputJSON)
		writeJSON(*outputJSON, resolvedAnalysis)

		// Generate HTML Dashboard
		fmt.Printf("Generating HTML dashboard to %s...\n", *outputHTML)
		writeHTML(*outputHTML, resolvedAnalysis, config, startupSource, startupParam, startupParam2, resolvedConfigPath)
		fmt.Println("Analysis completed successfully!")
	} else {
		// Generate blank dashboard to serve as base if in serveMode but no initial data found
		writeHTML(*outputHTML, RideAnalysis{}, config, "", "", "", resolvedConfigPath)
	}

	// Serve Mode if requested
	if *serveMode {
		serveDashboard(*outputHTML, *port, config, resolvedConfigPath)
	}
}

func loadConfig(path string) Config {
	// Default configuration (SRAM AXS 46/33 front, 10-36 12s rear)
	defaultConfig := Config{
		FrontGears: []int{33, 46},
		RearGears:  []int{36, 32, 28, 24, 21, 19, 17, 15, 13, 12, 11, 10},
		Bikes: []BikeProfile{
			{
				Name:       "Cervélo Soloist",
				FrontGears: []int{33, 46},
				RearGears:  []int{36, 32, 28, 24, 21, 19, 17, 15, 13, 12, 11, 10},
			},
			{
				Name:       "Cervélo Aspero",
				FrontGears: []int{40},
				RearGears:  []int{46, 38, 32, 28, 24, 21, 19, 17, 15, 13, 12, 11, 10},
			},
		},
		FTP:   250,
		MaxHR: 190,
	}

	f, err := os.Open(path)
	if err != nil {
		fmt.Printf("Warning: Config file %s not found, using default SRAM AXS 46/33, 10-36T config.\n", path)
		return defaultConfig
	}
	defer f.Close()

	var config Config
	if err := json.NewDecoder(f).Decode(&config); err != nil {
		fmt.Printf("Warning: Error parsing config %s (%v), using default config.\n", path, err)
		return defaultConfig
	}
	if config.FTP == 0 {
		config.FTP = 250
	}
	if config.MaxHR == 0 {
		config.MaxHR = 190
	}
	if len(config.Bikes) == 0 {
		config.Bikes = defaultConfig.Bikes
	}
	dataDir := os.Getenv("DIRECTEUR_DATA_DIR")
	if config.LocalDirectory == "" && dataDir != "" {
		config.LocalDirectory = filepath.Join(dataDir, "rides")
	}
	if config.HammerheadAPI.DownloadDir == "" {
		if dataDir != "" {
			config.HammerheadAPI.DownloadDir = filepath.Join(dataDir, "fit_downloads")
		} else {
			config.HammerheadAPI.DownloadDir = "./fit_downloads"
		}
	}
	if config.WahooAPI.DownloadDir == "" {
		if dataDir != "" {
			config.WahooAPI.DownloadDir = filepath.Join(dataDir, "wahoo_downloads")
		} else {
			config.WahooAPI.DownloadDir = "./wahoo_downloads"
		}
	}
	if config.IntervalsAPI.DownloadDir == "" {
		if dataDir != "" {
			config.IntervalsAPI.DownloadDir = filepath.Join(dataDir, "intervals_downloads")
		} else {
			config.IntervalsAPI.DownloadDir = "./intervals_downloads"
		}
	}
	return config
}

func saveConfig(path string, config Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// RideFile represents a local FIT file details for list API
type RideFile struct {
	Filename        string    `json:"filename"`
	ModTime         time.Time `json:"mod_time"`
	SizeBytes       int64     `json:"size_bytes"`
	DistanceMeters  float64   `json:"distance_meters,omitempty"`
	DurationSeconds float64   `json:"duration_seconds,omitempty"`
}

// HammerheadActivity represents a ride event fetched from the Hammerhead Dashboard API
type HammerheadActivity struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	StartTimeString string    `json:"startTime"`
	StartTime       time.Time `json:"-"`
	DistanceMeters  float64   `json:"distance"`
	DurationSeconds float64   `json:"duration"`
}

// UnmarshalJSON custom unmarshaler to handle flexible API schemas (camelCase / snake_case)
func (ha *HammerheadActivity) UnmarshalJSON(data []byte) error {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// ID mapping
	if val, ok := raw["id"].(string); ok {
		ha.ID = val
	} else if val, ok := raw["_id"].(string); ok {
		ha.ID = val
	} else if val, ok := raw["activityId"].(string); ok {
		ha.ID = val
	}
	// Name mapping
	if val, ok := raw["name"].(string); ok {
		ha.Name = val
	} else if val, ok := raw["title"].(string); ok {
		ha.Name = val
	}
	// Distance mapping
	if val, ok := raw["distance"].(float64); ok {
		ha.DistanceMeters = val
	} else if val, ok := raw["distance_meters"].(float64); ok {
		ha.DistanceMeters = val
	}
	// Duration mapping
	if val, ok := raw["duration"].(float64); ok {
		ha.DurationSeconds = val / 1000.0
	} else if val, ok := raw["elapsedTime"].(float64); ok {
		ha.DurationSeconds = val / 1000.0
	} else if val, ok := raw["duration_seconds"].(float64); ok {
		ha.DurationSeconds = val
	}
	// StartTime mapping
	var timeStr string
	if val, ok := raw["startTime"].(string); ok {
		timeStr = val
	} else if val, ok := raw["start_time"].(string); ok {
		timeStr = val
	} else if val, ok := raw["createdAt"].(string); ok {
		timeStr = val
	} else if val, ok := raw["created_at"].(string); ok {
		timeStr = val
	} else if val, ok := raw["timestamp"].(string); ok {
		timeStr = val
	}
	if timeStr != "" {
		ha.StartTimeString = timeStr
		if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
			ha.StartTime = t
		} else if t, err := time.Parse("2006-01-02T15:04:05.000Z", timeStr); err == nil {
			ha.StartTime = t
		} else if t, err := time.Parse("2006-01-02 15:04:05", timeStr); err == nil {
			ha.StartTime = t
		}
	}
	return nil
}

func getFitFileSummary(filePath string) (distanceMeters float64, durationSeconds float64, err error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	// Handle potential panics in external fit decoding library
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic during FIT decode: %v", r)
		}
	}()

	fitFile, err := fit.Decode(f)
	if err != nil {
		return 0, 0, err
	}

	activity, err := fitFile.Activity()
	if err != nil {
		return 0, 0, err
	}

	if len(activity.Records) == 0 {
		return 0, 0, fmt.Errorf("no records in FIT file")
	}

	startTime := activity.Records[0].Timestamp
	endTime := activity.Records[len(activity.Records)-1].Timestamp
	durationSeconds = endTime.Sub(startTime).Seconds()

	for i := len(activity.Records) - 1; i >= 0; i-- {
		distVal := activity.Records[i].GetDistanceScaled()
		if !math.IsNaN(distVal) && distVal > 0 {
			distanceMeters = distVal
			break
		}
	}

	return distanceMeters, durationSeconds, nil
}

// listLocalRides scans the configured local directory for FIT files and returns them sorted by modification time
func listLocalRides(dir string) ([]RideFile, error) {
	var rides []RideFile
	if dir == "" {
		return rides, nil
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name()))
		if ext == ".fit" {
			info, err := f.Info()
			if err != nil {
				continue
			}
			filePath := filepath.Join(dir, f.Name())
			dist, dur, _ := getFitFileSummary(filePath)
			rides = append(rides, RideFile{
				Filename:        f.Name(),
				ModTime:         info.ModTime(),
				SizeBytes:       info.Size(),
				DistanceMeters:  dist,
				DurationSeconds: dur,
			})
		}
	}
	// Sort newest first
	sort.Slice(rides, func(i, j int) bool {
		return rides[i].ModTime.After(rides[j].ModTime)
	})
	return rides, nil
}

type HammerheadTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func exchangeHammerheadCode(clientID, clientSecret, code, redirectURI string) (*HammerheadTokenResponse, error) {
	u := "https://api.hammerhead.io/v1/auth/oauth/token"
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)

	resp, err := http.PostForm(u, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp HammerheadTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}
	return &tokenResp, nil
}

func refreshHammerheadToken(clientID, clientSecret, refreshToken string) (*HammerheadTokenResponse, error) {
	u := "https://api.hammerhead.io/v1/auth/oauth/token"
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("refresh_token", refreshToken)

	resp, err := http.PostForm(u, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token refresh failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp HammerheadTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}
	return &tokenResp, nil
}

// extractUserIDFromJWT decodes the JWT token payload and returns the "sub" field
func extractUserIDFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid token format")
	}

	payloadSegment := parts[1]
	// Add base64 padding if needed
	switch len(payloadSegment) % 4 {
	case 2:
		payloadSegment += "=="
	case 3:
		payloadSegment += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payloadSegment)
	if err != nil {
		// Try standard base64 decoding if URLEncoding fails
		decoded, err = base64.StdEncoding.DecodeString(payloadSegment)
		if err != nil {
			return "", fmt.Errorf("failed to decode payload: %w", err)
		}
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return "", fmt.Errorf("failed to unmarshal claims: %w", err)
	}

	sub, ok := claims["sub"].(string)
	if !ok || sub == "" {
		return "", fmt.Errorf("sub claim not found or empty")
	}

	return sub, nil
}

// fetchHammerheadActivities gets the list of recent rides from Hammerhead API
func fetchHammerheadActivities(cfg HammerheadConfig, configPath string, page int) ([]HammerheadActivity, int, int, error) {
	if !cfg.Enabled {
		return nil, 0, 0, nil
	}

	makeRequest := func(token string) ([]HammerheadActivity, int, int, int, error) {
		client := &http.Client{Timeout: 10 * time.Second}
		url := fmt.Sprintf("https://api.hammerhead.io/v1/api/activities?page=%d&perPage=50", page)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, 0, 0, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, 0, 0, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return nil, 0, 0, resp.StatusCode, fmt.Errorf("hammerhead API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		}

		var envelope struct {
			Data        []HammerheadActivity `json:"data"`
			CurrentPage int                  `json:"currentPage"`
			TotalPages  int                  `json:"totalPages"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return nil, 0, 0, resp.StatusCode, err
		}
		return envelope.Data, envelope.CurrentPage, envelope.TotalPages, resp.StatusCode, nil
	}

	var tokenToUse = cfg.AuthToken
	var err error
	var statusCode int
	var activities []HammerheadActivity
	var currentPage, totalPages int

	if tokenToUse != "" {
		activities, currentPage, totalPages, statusCode, err = makeRequest(tokenToUse)
		if err == nil {
			return activities, currentPage, totalPages, nil
		}
	}

	// Try refresh if token was missing, or if we got a 401 Unauthorized
	if (tokenToUse == "" || statusCode == http.StatusUnauthorized) && cfg.RefreshToken != "" && cfg.ClientID != "" && cfg.ClientSecret != "" {
		fmt.Println("Hammerhead access token expired or missing, attempting token refresh...")
		tokenResp, refreshErr := refreshHammerheadToken(cfg.ClientID, cfg.ClientSecret, cfg.RefreshToken)
		if refreshErr == nil {
			// Save updated config
			currentConfig := loadConfig(configPath)
			currentConfig.HammerheadAPI.AuthToken = tokenResp.AccessToken
			if tokenResp.RefreshToken != "" {
				currentConfig.HammerheadAPI.RefreshToken = tokenResp.RefreshToken
			}
			if saveErr := saveConfig(configPath, currentConfig); saveErr == nil {
				// Retry the request with new token
				activities, currentPage, totalPages, _, err = makeRequest(tokenResp.AccessToken)
				if err == nil {
					return activities, currentPage, totalPages, nil
				}
			} else {
				fmt.Printf("Error saving config after refresh: %v\n", saveErr)
			}
		} else {
			return nil, 0, 0, fmt.Errorf("token refresh failed: %w (original request error: %v)", refreshErr, err)
		}
	}

	return nil, 0, 0, err
}

// uploadHammerheadRoute uploads a route GPX payload to Hammerhead
func uploadHammerheadRoute(cfg HammerheadConfig, configPath string, name, gpxContent string) error {
	makeRequest := func(token string) (int, error) {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)

		part, err := writer.CreateFormFile("file", name+".gpx")
		if err != nil {
			return 0, err
		}
		_, err = io.Copy(part, strings.NewReader(gpxContent))
		if err != nil {
			return 0, err
		}

		err = writer.Close()
		if err != nil {
			return 0, err
		}

		client := &http.Client{Timeout: 15 * time.Second}
		req, err := http.NewRequest("POST", "https://api.hammerhead.io/v1/api/routes/file", body)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", writer.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, fmt.Errorf("hammerhead API error (%d): %s", resp.StatusCode, string(bodyBytes))
		}
		return resp.StatusCode, nil
	}

	var tokenToUse = cfg.AuthToken
	var err error
	var statusCode int

	if tokenToUse != "" {
		statusCode, err = makeRequest(tokenToUse)
		if err == nil {
			return nil
		}
	}

	if (tokenToUse == "" || statusCode == http.StatusUnauthorized) && cfg.RefreshToken != "" && cfg.ClientID != "" && cfg.ClientSecret != "" {
		tokenResp, refreshErr := refreshHammerheadToken(cfg.ClientID, cfg.ClientSecret, cfg.RefreshToken)
		if refreshErr == nil {
			currentConfig := loadConfig(configPath)
			currentConfig.HammerheadAPI.AuthToken = tokenResp.AccessToken
			if tokenResp.RefreshToken != "" {
				currentConfig.HammerheadAPI.RefreshToken = tokenResp.RefreshToken
			}
			if saveErr := saveConfig(configPath, currentConfig); saveErr == nil {
				_, err = makeRequest(tokenResp.AccessToken)
				if err == nil {
					return nil
				}
			}
		}
	}
	return err
}

// downloadHammerheadFITFile retrieves and caches a FIT file from the Hammerhead activities API
func downloadHammerheadFITFile(cfg HammerheadConfig, configPath string, activityID string) (string, error) {
	if activityID == "" {
		return "", fmt.Errorf("empty activity ID")
	}

	if err := os.MkdirAll(cfg.DownloadDir, 0755); err != nil {
		return "", err
	}

	filePath := filepath.Join(cfg.DownloadDir, activityID+".fit")
	if _, err := os.Stat(filePath); err == nil {
		return filePath, nil
	}

	makeRequest := func(token string) (int, error) {
		client := &http.Client{Timeout: 30 * time.Second}
		url := fmt.Sprintf("https://api.hammerhead.io/v1/api/activities/%s/file", activityID)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, fmt.Errorf("failed to download FIT (status %d): %s", resp.StatusCode, string(bodyBytes))
		}

		out, err := os.Create(filePath)
		if err != nil {
			return resp.StatusCode, err
		}
		defer out.Close()

		_, err = io.Copy(out, resp.Body)
		if err != nil {
			return resp.StatusCode, err
		}

		return resp.StatusCode, nil
	}

	var tokenToUse = cfg.AuthToken
	var err error
	var statusCode int

	if tokenToUse != "" {
		statusCode, err = makeRequest(tokenToUse)
		if err == nil {
			return filePath, nil
		}
	}

	// Try refresh if token was missing, or if we got a 401 Unauthorized
	if (tokenToUse == "" || statusCode == http.StatusUnauthorized) && cfg.RefreshToken != "" && cfg.ClientID != "" && cfg.ClientSecret != "" {
		fmt.Println("Hammerhead access token expired or missing, attempting token refresh...")
		tokenResp, refreshErr := refreshHammerheadToken(cfg.ClientID, cfg.ClientSecret, cfg.RefreshToken)
		if refreshErr == nil {
			// Save updated config
			currentConfig := loadConfig(configPath)
			currentConfig.HammerheadAPI.AuthToken = tokenResp.AccessToken
			if tokenResp.RefreshToken != "" {
				currentConfig.HammerheadAPI.RefreshToken = tokenResp.RefreshToken
			}
			if saveErr := saveConfig(configPath, currentConfig); saveErr == nil {
				// Retry the request with new token
				_, err = makeRequest(tokenResp.AccessToken)
				if err == nil {
					return filePath, nil
				}
			} else {
				fmt.Printf("Error saving config after refresh: %v\n", saveErr)
			}
		} else {
			return "", fmt.Errorf("token refresh failed: %w (original download error: %v)", refreshErr, err)
		}
	}

	return "", err
}

type WahooTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type WahooWorkout struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Starts         string    `json:"starts"`
	Distance       float64   `json:"distance"`
	DurationActive float64   `json:"duration_active"`
	File           WahooFile `json:"file"`
}

type WahooFile struct {
	URL string `json:"url"`
}

type WahooWorkoutsEnvelope struct {
	Workouts    []WahooWorkout `json:"workouts"`
	CurrentPage int            `json:"current_page"`
	TotalPages  int            `json:"total_pages"`
}

func exchangeWahooCode(clientID, clientSecret, code, redirectURI string) (*WahooTokenResponse, error) {
	u := "https://api.wahooligan.com/oauth/token"
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)

	resp, err := http.PostForm(u, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Wahoo token exchange failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp WahooTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}
	return &tokenResp, nil
}

func refreshWahooToken(clientID, clientSecret, refreshToken string) (*WahooTokenResponse, error) {
	u := "https://api.wahooligan.com/oauth/token"
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("refresh_token", refreshToken)

	resp, err := http.PostForm(u, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Wahoo token refresh failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp WahooTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}
	return &tokenResp, nil
}

func fetchWahooWorkouts(cfg WahooConfig, configPath string, page int) ([]WahooWorkout, int, int, error) {
	if !cfg.Enabled {
		return nil, 0, 0, nil
	}

	makeRequest := func(token string) ([]WahooWorkout, int, int, int, error) {
		client := &http.Client{Timeout: 10 * time.Second}
		u := fmt.Sprintf("https://api.wahooligan.com/v1/workouts?page=%d&per_page=50", page)
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, 0, 0, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, 0, 0, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return nil, 0, 0, resp.StatusCode, fmt.Errorf("Wahoo API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		}

		var envelope WahooWorkoutsEnvelope
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return nil, 0, 0, resp.StatusCode, err
		}
		return envelope.Workouts, envelope.CurrentPage, envelope.TotalPages, resp.StatusCode, nil
	}

	var tokenToUse = cfg.AuthToken
	var err error
	var statusCode int
	var workouts []WahooWorkout
	var currentPage, totalPages int

	if tokenToUse != "" {
		workouts, currentPage, totalPages, statusCode, err = makeRequest(tokenToUse)
		if err == nil {
			return workouts, currentPage, totalPages, nil
		}
	}

	if (tokenToUse == "" || statusCode == http.StatusUnauthorized) && cfg.RefreshToken != "" && cfg.ClientID != "" && cfg.ClientSecret != "" {
		fmt.Println("Wahoo access token expired or missing, attempting token refresh...")
		tokenResp, refreshErr := refreshWahooToken(cfg.ClientID, cfg.ClientSecret, cfg.RefreshToken)
		if refreshErr == nil {
			currentConfig := loadConfig(configPath)
			currentConfig.WahooAPI.AuthToken = tokenResp.AccessToken
			if tokenResp.RefreshToken != "" {
				currentConfig.WahooAPI.RefreshToken = tokenResp.RefreshToken
			}
			if saveErr := saveConfig(configPath, currentConfig); saveErr == nil {
				workouts, currentPage, totalPages, _, err = makeRequest(tokenResp.AccessToken)
				if err == nil {
					return workouts, currentPage, totalPages, nil
				}
			} else {
				fmt.Printf("Error saving config after Wahoo refresh: %v\n", saveErr)
			}
		} else {
			return nil, 0, 0, fmt.Errorf("Wahoo token refresh failed: %w (original request error: %v)", refreshErr, err)
		}
	}

	return nil, 0, 0, err
}

func downloadWahooFITFile(cfg WahooConfig, cdnURL string, workoutID int64) (string, error) {
	if cdnURL == "" {
		return "", fmt.Errorf("empty CDN URL")
	}

	if err := os.MkdirAll(cfg.DownloadDir, 0755); err != nil {
		return "", err
	}

	filePath := filepath.Join(cfg.DownloadDir, fmt.Sprintf("%d.fit", workoutID))
	if _, err := os.Stat(filePath); err == nil {
		return filePath, nil
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(cdnURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to download Wahoo FIT from CDN (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	out, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return "", err
	}

	return filePath, nil
}

type IntervalsActivity struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	StartDateLocal string    `json:"start_date_local"`
	StartTime      time.Time `json:"start_time"`
	DistanceKM     float64   `json:"distance_km"`
	Duration       string    `json:"duration"`
}

func fetchIntervalsActivities(cfg IntervalsConfig) ([]IntervalsActivity, error) {
	if !cfg.Enabled || cfg.APIKey == "" {
		return nil, nil
	}

	athleteID := cfg.AthleteID
	if athleteID == "" {
		athleteID = "0"
	}

	// Fetch last 50 activities in the last 60 days
	oldest := time.Now().AddDate(0, 0, -60).Format("2006-01-02")
	newest := time.Now().AddDate(0, 0, 1).Format("2006-01-02")

	reqURL := fmt.Sprintf("https://intervals.icu/api/v1/athlete/%s/activities?oldest=%s&newest=%s&limit=50", athleteID, oldest, newest)
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("API_KEY", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var rawActivities []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rawActivities); err != nil {
		return nil, err
	}

	var list []IntervalsActivity
	for _, act := range rawActivities {
		actType, _ := act["type"].(string)
		if !strings.Contains(strings.ToLower(actType), "ride") {
			continue
		}

		idVal := ""
		if idNum, ok := act["id"].(float64); ok {
			idVal = fmt.Sprintf("%.0f", idNum)
		} else if idStr, ok := act["id"].(string); ok {
			idVal = idStr
		}
		if idVal == "" {
			continue
		}

		name, _ := act["name"].(string)
		if name == "" {
			name = "Intervals.icu Activity"
		}
		startDateStr, _ := act["start_date"].(string)
		if startDateStr == "" {
			startDateStr, _ = act["start_date_local"].(string)
		}

		var startTime time.Time
		if startDateStr != "" {
			t, parseErr := time.Parse(time.RFC3339, startDateStr)
			if parseErr == nil {
				startTime = t
			} else {
				t, parseErr = time.Parse("2006-01-02T15:04:05", startDateStr)
				if parseErr == nil {
					startTime = t
				}
			}
		}

		distanceM := 0.0
		if distVal, ok := act["distance"].(float64); ok {
			distanceM = distVal
		}
		distanceKM := distanceM / 1000.0

		movingSecs := 0.0
		if movVal, ok := act["moving_time"].(float64); ok {
			movingSecs = movVal
		} else if movValInt, ok := act["moving_time"].(int); ok {
			movingSecs = float64(movValInt)
		}

		durStr := ""
		if movingSecs > 0 {
			h := int(movingSecs) / 3600
			m := (int(movingSecs) % 3600) / 60
			s := int(movingSecs) % 60
			if h > 0 {
				durStr = fmt.Sprintf("%dh %dm", h, m)
			} else {
				durStr = fmt.Sprintf("%dm %ds", m, s)
			}
		}

		list = append(list, IntervalsActivity{
			ID:             idVal,
			Name:           name,
			Type:           actType,
			StartDateLocal: startDateStr,
			StartTime:      startTime,
			DistanceKM:     math.Round(distanceKM*10.0) / 10.0,
			Duration:       durStr,
		})
	}

	return list, nil
}

func downloadIntervalsFITFile(cfg IntervalsConfig, activityID string) (string, error) {
	if activityID == "" {
		return "", fmt.Errorf("empty activity ID")
	}

	if err := os.MkdirAll(cfg.DownloadDir, 0755); err != nil {
		return "", err
	}

	filePath := filepath.Join(cfg.DownloadDir, activityID+".fit")
	if _, err := os.Stat(filePath); err == nil {
		return filePath, nil
	}

	url := fmt.Sprintf("https://intervals.icu/api/v1/activity/%s/file", activityID)
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth("API_KEY", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Intervals.icu returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var fitBytes []byte
	if len(bodyBytes) > 2 && bodyBytes[0] == 0x1f && bodyBytes[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(bodyBytes))
		if err != nil {
			return "", fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer gr.Close()
		decompressed, err := io.ReadAll(gr)
		if err != nil {
			return "", fmt.Errorf("failed to decompress gzip content: %w", err)
		}
		fitBytes = decompressed
	} else {
		fitBytes = bodyBytes
	}

	err = os.WriteFile(filePath, fitBytes, 0644)
	if err != nil {
		return "", err
	}

	return filePath, nil
}

func sortEvents(events []*fit.EventMsg) {
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
}

func buildGearTimeline(records []*fit.RecordMsg, events []*fit.EventMsg, config Config) []GearState {
	// Find initial gear state
	var initialFrontNum, initialFrontTeeth uint8
	var initialRearNum, initialRearTeeth uint8

	for _, e := range events {
		if (e.Event == fit.EventFrontGearChange || e.Event == fit.EventRearGearChange) && (e.FrontGear > 0 || e.RearGear > 0) {
			initialFrontNum = e.FrontGearNum
			initialFrontTeeth = e.FrontGear
			initialRearNum = e.RearGearNum
			initialRearTeeth = e.RearGear
			break
		}
	}

	// Fallback to defaults if no events contain gear data
	if initialFrontNum == 0 && len(config.FrontGears) > 0 {
		initialFrontNum = 2 // Outer ring
		initialFrontTeeth = uint8(config.FrontGears[1])
	}
	if initialRearNum == 0 && len(config.RearGears) > 0 {
		initialRearNum = 6 // Middle cog
		initialRearTeeth = uint8(config.RearGears[5])
	}

	frontNum := initialFrontNum
	frontTeeth := initialFrontTeeth
	rearNum := initialRearNum
	rearTeeth := initialRearTeeth

	timeline := make([]GearState, len(records))
	eventIdx := 0

	for i, r := range records {
		// Update gear state with all events occurring at or before this record's timestamp
		for eventIdx < len(events) && !events[eventIdx].Timestamp.After(r.Timestamp) {
			e := events[eventIdx]
			if e.Event == fit.EventFrontGearChange || e.Event == fit.EventRearGearChange {
				if e.FrontGearNum > 0 {
					frontNum = e.FrontGearNum
					if int(e.FrontGearNum) <= len(config.FrontGears) {
						frontTeeth = uint8(config.FrontGears[e.FrontGearNum-1])
					} else {
						frontTeeth = e.FrontGear
					}
				}
				if e.RearGearNum > 0 {
					rearNum = e.RearGearNum
					if int(e.RearGearNum) <= len(config.RearGears) {
						rearTeeth = uint8(config.RearGears[e.RearGearNum-1])
					} else {
						rearTeeth = e.RearGear
					}
				}
			}
			eventIdx++
		}

		timeline[i] = GearState{
			FrontNum:   frontNum,
			FrontTeeth: frontTeeth,
			RearNum:    rearNum,
			RearTeeth:  rearTeeth,
		}
	}

	return timeline
}

func processRecords(records []*fit.RecordMsg, gearTimeline []GearState) RideAnalysis {
	n := len(records)
	telemetry := make([]TelemetryRecord, n)
	startTime := records[0].Timestamp

	// Accumulators for averages and summaries
	var totalPower, totalHR, totalCadence float64
	var powerCount, hrCount, cadenceCount float64
	var maxPower, maxHR, maxCadence int
	var maxAlt, minAlt float64
	minAlt = 99999.0

	var lastValidAlt, lastValidSpeed, lastValidDist float64
	var elevationGain float64

	// Power array for 30s rolling and Normalized Power
	rawPowers := make([]int, n)

	// Shift counters
	frontShifts := 0
	rearShifts := 0
	var lastFrontNum, lastRearNum int

	// Gear combination durations
	gearDurations := make(map[string]int) // e.g. "46x19" -> seconds

	for i, r := range records {
		elapsed := r.Timestamp.Sub(startTime).Seconds()

		// 1. Process Power
		pVal := 0
		if r.Power != 65535 {
			pVal = int(r.Power)
			totalPower += float64(pVal)
			powerCount++
			if pVal > maxPower {
				maxPower = pVal
			}
		}
		rawPowers[i] = pVal

		// 2. Process Heart Rate
		hrVal := 0
		if r.HeartRate != 255 {
			hrVal = int(r.HeartRate)
			totalHR += float64(hrVal)
			hrCount++
			if hrVal > maxHR {
				maxHR = hrVal
			}
		}

		// 3. Process Cadence
		cadVal := 0
		if r.Cadence != 255 {
			cadVal = int(r.Cadence)
			totalCadence += float64(cadVal)
			cadenceCount++
			if cadVal > maxCadence {
				maxCadence = cadVal
			}
		}

		// 4. Process Altitude
		altVal := r.GetAltitudeScaled()
		if math.IsNaN(altVal) {
			altVal = lastValidAlt
		} else {
			// Calculate elevation gain
			if i > 0 && altVal > lastValidAlt {
				elevationGain += (altVal - lastValidAlt)
			}
			lastValidAlt = altVal
		}
		if altVal > maxAlt {
			maxAlt = altVal
		}
		if altVal < minAlt {
			minAlt = altVal
		}

		// 5. Process Speed
		speedVal := r.GetSpeedScaled() // m/s
		if math.IsNaN(speedVal) {
			speedVal = lastValidSpeed
		} else {
			lastValidSpeed = speedVal
		}
		speedKmh := speedVal * 3.6

		// 6. Process Distance
		distVal := r.GetDistanceScaled() // meters
		if math.IsNaN(distVal) {
			distVal = lastValidDist
		} else {
			lastValidDist = distVal
		}

		// 7. Process GPS coordinates
		latDeg := 0.0
		lonDeg := 0.0
		if !r.PositionLat.Invalid() && !r.PositionLong.Invalid() {
			latDeg = r.PositionLat.Degrees()
			lonDeg = r.PositionLong.Degrees()
		}

		// 8. Get gears from timeline
		g := gearTimeline[i]
		fNum := int(g.FrontNum)
		fTeeth := int(g.FrontTeeth)
		rNum := int(g.RearNum)
		rTeeth := int(g.RearTeeth)

		ratio := 0.0
		if rTeeth > 0 {
			ratio = float64(fTeeth) / float64(rTeeth)
		}

		// Count shifts
		if i > 0 {
			if fNum != lastFrontNum && lastFrontNum != 0 {
				frontShifts++
			}
			if rNum != lastRearNum && lastRearNum != 0 {
				rearShifts++
			}
		}
		lastFrontNum = fNum
		lastRearNum = rNum

		// Track gear combination duration
		if fTeeth > 0 && rTeeth > 0 {
			comboKey := fmt.Sprintf("%dx%d", fTeeth, rTeeth)
			gearDurations[comboKey]++
		}

		telemetry[i] = TelemetryRecord{
			Timestamp:          r.Timestamp,
			ElapsedTimeSeconds: elapsed,
			Power:              pVal,
			HeartRate:          hrVal,
			Cadence:            cadVal,
			AltitudeMeters:     altVal,
			SpeedKmh:           speedKmh,
			DistanceMeters:     distVal,
			LatitudeDeg:        latDeg,
			LongitudeDeg:       lonDeg,
			FrontGearNum:       fNum,
			FrontGearTeeth:     fTeeth,
			RearGearNum:        rNum,
			RearGearTeeth:      rTeeth,
			GearRatio:          ratio,
		}
	}

	// 30s rolling power & Normalized Power calculation
	power30sList := make([]int, n)
	for i := 0; i < n; i++ {
		p30s := compute30sPower(records, rawPowers, i)
		telemetry[i].Power30s = p30s
		power30sList[i] = p30s
	}

	normPower := calculateNormalizedPower(power30sList)

	// Calculate peak power curve
	powerCurve := calculatePowerCurve(records, rawPowers)

	// Summary creation
	duration := records[n-1].Timestamp.Sub(startTime).Seconds()
	avgPower := 0.0
	if powerCount > 0 {
		avgPower = totalPower / powerCount
	}
	avgHR := 0.0
	if hrCount > 0 {
		avgHR = totalHR / hrCount
	}
	avgCadence := 0.0
	if cadenceCount > 0 {
		avgCadence = totalCadence / cadenceCount
	}

	if minAlt == 99999.0 {
		minAlt = 0
	}

	summary := RideSummary{
		StartTime:                startTime,
		DurationSeconds:          duration,
		DistanceMeters:           lastValidDist,
		AveragePower:             avgPower,
		MaxPower:                 maxPower,
		NormalizedPower:          normPower,
		AverageHeartRate:         avgHR,
		MaxHeartRate:             maxHR,
		AverageCadence:           avgCadence,
		MaxCadence:               maxCadence,
		TotalElevationGainMeters: elevationGain,
		MaxAltitudeMeters:        maxAlt,
		MinAltitudeMeters:        minAlt,
		TotalFrontShifts:         frontShifts,
		TotalRearShifts:          rearShifts,
		TotalShifts:              frontShifts + rearShifts,
		PowerCurve:               powerCurve,
	}

	// Sort gear durations to percentages
	var usage []GearStats
	totalRideSeconds := int(duration)
	if totalRideSeconds == 0 {
		totalRideSeconds = 1
	}
	for combo, secs := range gearDurations {
		pct := (float64(secs) / float64(totalRideSeconds)) * 100.0
		usage = append(usage, GearStats{
			Combination: combo,
			Seconds:     secs,
			Percentage:  pct,
		})
	}
	sort.Slice(usage, func(i, j int) bool {
		return usage[i].Seconds > usage[j].Seconds
	})

	return RideAnalysis{
		Summary:   summary,
		GearUsage: usage,
		Records:   telemetry,
	}
}

func compute30sPower(records []*fit.RecordMsg, rawPowers []int, index int) int {
	targetTime := records[index].Timestamp
	startTime := targetTime.Add(-29 * time.Second)

	sum := 0
	count := 0
	for j := index; j >= 0; j-- {
		r := records[j]
		if r.Timestamp.Before(startTime) {
			break
		}
		if records[j].Power != 65535 {
			sum += rawPowers[j]
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / count
}

func calculateNormalizedPower(power30s []int) int {
	if len(power30s) == 0 {
		return 0
	}
	var sum float64
	count := 0
	for _, p := range power30s {
		sum += math.Pow(float64(p), 4)
		count++
	}
	if count == 0 {
		return 0
	}
	avg := sum / float64(count)
	return int(math.Round(math.Pow(avg, 0.25)))
}

func calculatePowerCurve(records []*fit.RecordMsg, rawPowers []int) map[string]int {
	durations := map[string]int{
		"1s":  1,
		"3s":  3,
		"5s":  5,
		"30s": 30,
		"1m":  60,
		"3m":  180,
		"5m":  300,
		"20m": 1200,
		"1h":  3600,
	}

	curve := make(map[string]int)
	n := len(records)
	if n == 0 {
		return curve
	}

	for label, secs := range durations {
		if n < secs {
			curve[label] = 0
			continue
		}

		maxAvg := 0
		for i := 0; i < n; i++ {
			startTime := records[i].Timestamp
			endTime := startTime.Add(time.Duration(secs) * time.Second)
			sum := 0
			for j := i; j < n; j++ {
				if !records[j].Timestamp.Before(endTime) {
					break
				}
				sum += rawPowers[j]
			}
			avg := sum / secs
			if avg > maxAvg {
				maxAvg = avg
			}
		}
		curve[label] = maxAvg
	}

	return curve
}

func selectThemeName(startTime time.Time) string {
	switch startTime.Month() {
	case time.March:
		return "Classics Flandrian"
	case time.April, time.May:
		return "Giro Pink"
	case time.June, time.July:
		return "Tour Yellow"
	case time.August, time.September:
		return "Vuelta Red"
	default:
		return "Carbon Dark"
	}
}

func writeJSON(path string, analysis RideAnalysis) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Printf("Error creating JSON file: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(analysis); err != nil {
		fmt.Printf("Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

func writeHTML(path string, analysis RideAnalysis, config Config, source string, param string, param2 string, configPath string) {
	tmplSrc := getDashboardTemplate()
	tmpl, err := template.New("dashboard").Parse(tmplSrc)
	if err != nil {
		fmt.Printf("Error parsing HTML template: %v\n", err)
		os.Exit(1)
	}

	// Prepare data for JSON embedding in HTML template
	jsonData, err := json.Marshal(analysis)
	if err != nil {
		fmt.Printf("Error encoding embedded JSON: %v\n", err)
		os.Exit(1)
	}

	// Prepare bikes configuration for embedding
	bikesData, err := json.Marshal(config.Bikes)
	if err != nil {
		bikesData = []byte("[]")
	}

	// Read schema
	schemaPath := "schema.json"
	if dataDir := os.Getenv("DIRECTEUR_DATA_DIR"); dataDir != "" {
		dataSchema := filepath.Join(dataDir, "schema.json")
		if _, err := os.Stat(dataSchema); err == nil {
			schemaPath = dataSchema
		}
	}
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		schemaBytes = []byte(`{"error": "schema.json not found"}`)
	}

	f, err := os.Create(path)
	if err != nil {
		fmt.Printf("Error creating HTML file: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	type TmplData struct {
		JSONStr    template.JS
		SchemaStr  template.JS
		BikesStr   template.JS
		Summary    RideSummary
		GearUsage  []GearStats
		FTP        int
		MaxHR      int
		Source     string
		Param      string
		Param2     string
		ConfigPath string
	}

	data := TmplData{
		JSONStr:    template.JS(jsonData),
		SchemaStr:  template.JS(schemaBytes),
		BikesStr:   template.JS(bikesData),
		Summary:    analysis.Summary,
		GearUsage:  analysis.GearUsage,
		FTP:        config.FTP,
		MaxHR:      config.MaxHR,
		Source:     source,
		Param:      param,
		Param2:     param2,
		ConfigPath: configPath,
	}

	if err := tmpl.Execute(f, data); err != nil {
		fmt.Printf("Error executing HTML template: %v\n", err)
		os.Exit(1)
	}
}

func serveDashboard(path string, port int, config Config, configPath string) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	fmt.Printf("\nStarting local server on http://localhost:%d/\n", port)
	fmt.Printf("Serving file: %s\n", absPath)
	fmt.Println("Press Ctrl+C to stop.")

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		http.ServeFile(w, r, absPath)
	})

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			return
		}

		// Load fresh config
		cfg := loadConfig(configPath)
		if cfg.HammerheadAPI.ClientID == "" || cfg.HammerheadAPI.ClientSecret == "" {
			http.Error(w, fmt.Sprintf("Client credentials not configured in config file at %s", configPath), http.StatusInternalServerError)
			return
		}

		scheme := "http"
		if r.Header.Get("X-Forwarded-Proto") != "" {
			scheme = r.Header.Get("X-Forwarded-Proto")
		} else if r.TLS != nil {
			scheme = "https"
		}
		redirectURI := fmt.Sprintf("%s://%s/callback", scheme, r.Host)

		tokenResp, err := exchangeHammerheadCode(
			cfg.HammerheadAPI.ClientID,
			cfg.HammerheadAPI.ClientSecret,
			code,
			redirectURI,
		)
		if err != nil {
			http.Error(w, fmt.Sprintf("Token exchange failed: %v", err), http.StatusInternalServerError)
			return
		}

		// Update and save config
		cfg.HammerheadAPI.AuthToken = tokenResp.AccessToken
		cfg.HammerheadAPI.RefreshToken = tokenResp.RefreshToken
		cfg.HammerheadAPI.Enabled = true
		if err := saveConfig(configPath, cfg); err != nil {
			http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
			return
		}

		// Redirect user back to the main dashboard page
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
    <title>Link Successful</title>
    <style>
        body {
            background-color: #0a0a0c;
            color: #ffffff;
            font-family: 'Outfit', sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            height: 100vh;
            margin: 0;
            text-align: center;
        }
        .container {
            background: rgba(255, 255, 255, 0.02);
            border: 1px solid #27273a;
            padding: 3rem;
            border-radius: 20px;
            box-shadow: 0 10px 25px rgba(0,0,0,0.5);
            max-width: 400px;
        }
        h2 { color: #E45C86; margin-bottom: 1rem; }
        p { color: #94a3b8; font-size: 0.95rem; margin-bottom: 2rem; }
        .btn {
            background: #E45C86;
            color: white;
            border: none;
            padding: 0.75rem 2rem;
            border-radius: 10px;
            font-weight: 600;
            text-decoration: none;
            cursor: pointer;
        }
    </style>
</head>
<body>
    <div class="container">
        <h2>Connection Successful!</h2>
        <p>Your Hammerhead Account has been successfully linked to directeurAI. You can close this window now or return to the dashboard.</p>
        <a class="btn" href="/">Return to Dashboard</a>
    </div>
    <script>
        // Auto-redirect back to dashboard after 3 seconds
        setTimeout(function() {
            window.location.href = "/";
        }, 3000);
    </script>
</body>
</html>`))
	})

	http.HandleFunc("/wahoo-callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			return
		}

		// Load fresh config
		cfg := loadConfig(configPath)
		if cfg.WahooAPI.ClientID == "" || cfg.WahooAPI.ClientSecret == "" {
			http.Error(w, fmt.Sprintf("Wahoo client credentials not configured in config file at %s", configPath), http.StatusInternalServerError)
			return
		}

		scheme := "http"
		if r.Header.Get("X-Forwarded-Proto") != "" {
			scheme = r.Header.Get("X-Forwarded-Proto")
		} else if r.TLS != nil {
			scheme = "https"
		}
		redirectURI := fmt.Sprintf("%s://%s/wahoo-callback", scheme, r.Host)

		tokenResp, err := exchangeWahooCode(
			cfg.WahooAPI.ClientID,
			cfg.WahooAPI.ClientSecret,
			code,
			redirectURI,
		)
		if err != nil {
			http.Error(w, fmt.Sprintf("Wahoo token exchange failed: %v", err), http.StatusInternalServerError)
			return
		}

		// Update and save config
		cfg.WahooAPI.AuthToken = tokenResp.AccessToken
		cfg.WahooAPI.RefreshToken = tokenResp.RefreshToken
		cfg.WahooAPI.Enabled = true
		if err := saveConfig(configPath, cfg); err != nil {
			http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
			return
		}

		// Redirect user back to the main dashboard page
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
    <title>Link Successful</title>
    <style>
        body {
            background-color: #0a0a0c;
            color: #ffffff;
            font-family: 'Outfit', sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            height: 100vh;
            margin: 0;
            text-align: center;
        }
        .container {
            background: rgba(255, 255, 255, 0.02);
            border: 1px solid #27273a;
            padding: 3rem;
            border-radius: 20px;
            box-shadow: 0 10px 25px rgba(0,0,0,0.5);
            max-width: 400px;
        }
        h2 { color: #9b59b6; margin-bottom: 1rem; }
        p { color: #94a3b8; font-size: 0.95rem; margin-bottom: 2rem; }
        .btn {
            background: #9b59b6;
            color: white;
            border: none;
            padding: 0.75rem 2rem;
            border-radius: 10px;
            font-weight: 600;
            text-decoration: none;
            cursor: pointer;
        }
    </style>
</head>
<body>
    <div class="container">
        <h2>Connection Successful!</h2>
        <p>Your Wahoo Account has been successfully linked to directeurAI. You can close this window now or return to the dashboard.</p>
        <a class="btn" href="/">Return to Dashboard</a>
    </div>
    <script>
        // Auto-redirect back to dashboard after 3 seconds
        setTimeout(function() {
            window.location.href = "/";
        }, 3000);
    </script>
</body>
</html>`))
	})

	http.HandleFunc("/api/rides", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cfg := loadConfig(configPath)
		localRides, err := listLocalRides(cfg.LocalDirectory)
		if err != nil {
			fmt.Printf("Error listing local rides: %v\n", err)
		}

		// Hammerhead Page parsing
		hhPageStr := r.URL.Query().Get("hh_page")
		hhPage := 1
		if hhPageStr != "" {
			if p, err := strconv.Atoi(hhPageStr); err == nil && p > 0 {
				hhPage = p
			}
		} else {
			// shared fallback
			if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
				hhPage = p
			}
		}

		// Wahoo Page parsing
		wahooPageStr := r.URL.Query().Get("wahoo_page")
		wahooPage := 1
		if wahooPageStr != "" {
			if p, err := strconv.Atoi(wahooPageStr); err == nil && p > 0 {
				wahooPage = p
			}
		} else {
			// shared fallback
			if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
				wahooPage = p
			}
		}

		hhRides, currentPage, totalPages, hhErr := fetchHammerheadActivities(cfg.HammerheadAPI, configPath, hhPage)
		if hhErr != nil {
			fmt.Printf("Error fetching Hammerhead activities: %v\n", hhErr)
		}

		wahooRides, wahooCurrentPage, wahooTotalPages, wahooErr := fetchWahooWorkouts(cfg.WahooAPI, configPath, wahooPage)
		if wahooErr != nil {
			fmt.Printf("Error fetching Wahoo workouts: %v\n", wahooErr)
		}

		intervalsRides, intervalsErr := fetchIntervalsActivities(cfg.IntervalsAPI)
		if intervalsErr != nil {
			fmt.Printf("Error fetching Intervals activities: %v\n", intervalsErr)
		}

		type RidesResponse struct {
			Local                []RideFile           `json:"local"`
			Hammerhead           []HammerheadActivity `json:"hammerhead"`
			HammerheadConfigured bool                 `json:"hammerhead_configured"`
			HammerheadLinked     bool                 `json:"hammerhead_linked"`
			HammerheadError      string               `json:"hammerhead_error,omitempty"`
			ClientID             string               `json:"client_id,omitempty"`
			CurrentPage          int                  `json:"current_page"`
			TotalPages           int                  `json:"total_pages"`

			Wahoo            []WahooWorkout `json:"wahoo"`
			WahooConfigured  bool           `json:"wahoo_configured"`
			WahooLinked      bool           `json:"wahoo_linked"`
			WahooError       string         `json:"wahoo_error,omitempty"`
			WahooClientID    string         `json:"wahoo_client_id,omitempty"`
			WahooCurrentPage int            `json:"wahoo_current_page"`
			WahooTotalPages  int            `json:"wahoo_total_pages"`

			Intervals           []IntervalsActivity `json:"intervals"`
			IntervalsConfigured bool                `json:"intervals_configured"`

			Bikes []BikeProfile `json:"bikes"`
		}

		var hhErrStr string
		if hhErr != nil {
			hhErrStr = hhErr.Error()
		}

		var wahooErrStr string
		if wahooErr != nil {
			wahooErrStr = wahooErr.Error()
		}

		resp := RidesResponse{
			Local:                localRides,
			Hammerhead:           hhRides,
			HammerheadConfigured: cfg.HammerheadAPI.ClientID != "" && cfg.HammerheadAPI.ClientSecret != "",
			HammerheadLinked:     cfg.HammerheadAPI.Enabled && (cfg.HammerheadAPI.AuthToken != "" || cfg.HammerheadAPI.RefreshToken != ""),
			HammerheadError:      hhErrStr,
			ClientID:             cfg.HammerheadAPI.ClientID,
			CurrentPage:          currentPage,
			TotalPages:           totalPages,

			Wahoo:            wahooRides,
			WahooConfigured:  cfg.WahooAPI.ClientID != "" && cfg.WahooAPI.ClientSecret != "",
			WahooLinked:      cfg.WahooAPI.Enabled && (cfg.WahooAPI.AuthToken != "" || cfg.WahooAPI.RefreshToken != ""),
			WahooError:       wahooErrStr,
			WahooClientID:    cfg.WahooAPI.ClientID,
			WahooCurrentPage: wahooCurrentPage,
			WahooTotalPages:  wahooTotalPages,

			Intervals:           intervalsRides,
			IntervalsConfigured: cfg.IntervalsAPI.Enabled && cfg.IntervalsAPI.APIKey != "",

			Bikes: cfg.Bikes,
		}
		json.NewEncoder(w).Encode(resp)
	})

	http.HandleFunc("/api/hammerhead/sync-route", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		cfg := loadConfig(configPath)
		if !cfg.HammerheadAPI.Enabled {
			http.Error(w, `{"error": "Hammerhead integration not enabled"}`, http.StatusBadRequest)
			return
		}

		var payload struct {
			Name string `json:"name"`
			GPX  string `json:"gpx"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "invalid payload: %v"}`, err), http.StatusBadRequest)
			return
		}

		if payload.Name == "" || payload.GPX == "" {
			http.Error(w, `{"error": "name and gpx are required"}`, http.StatusBadRequest)
			return
		}

		err := uploadHammerheadRoute(cfg.HammerheadAPI, configPath, payload.Name, payload.GPX)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Write([]byte(`{"status": "success"}`))
	})

	http.HandleFunc("/api/hammerhead/unlink", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		cfg := loadConfig(configPath)
		cfg.HammerheadAPI.Enabled = false
		cfg.HammerheadAPI.AuthToken = ""
		cfg.HammerheadAPI.RefreshToken = ""
		if err := saveConfig(configPath, cfg); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "failed to save config"})
			return
		}

		w.Write([]byte(`{"status": "success"}`))
	})

	http.HandleFunc("/api/geocode", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("q")
		if q == "" {
			http.Error(w, `{"error": "query parameter 'q' is required"}`, http.StatusBadRequest)
			return
		}

		targetURL := fmt.Sprintf("https://nominatim.openstreetmap.org/search?format=json&q=%s&limit=1", url.QueryEscape(q))
		req, err := http.NewRequest(http.MethodGet, targetURL, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
			return
		}
		req.Header.Set("User-Agent", "directeurAI/1.0")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	http.HandleFunc("/api/reverse-geocode", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		lat := r.URL.Query().Get("lat")
		lon := r.URL.Query().Get("lon")
		if lat == "" || lon == "" {
			http.Error(w, `{"error": "parameters 'lat' and 'lon' are required"}`, http.StatusBadRequest)
			return
		}

		targetURL := fmt.Sprintf("https://nominatim.openstreetmap.org/reverse?format=json&lat=%s&lon=%s", url.QueryEscape(lat), url.QueryEscape(lon))
		req, err := http.NewRequest(http.MethodGet, targetURL, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
			return
		}
		req.Header.Set("User-Agent", "directeurAI/1.0")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	http.HandleFunc("/api/brouter", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		targetURL := "https://brouter.de/brouter?" + r.URL.RawQuery
		req, err := http.NewRequest(http.MethodGet, targetURL, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
			return
		}
		req.Header.Set("User-Agent", "directeurAI/1.0")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	http.HandleFunc("/api/analyze", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cfg := loadConfig(configPath)
		source := r.URL.Query().Get("source")
		force := r.URL.Query().Get("force")
		var filePath string
		var err error

		if source == "local" {
			file := r.URL.Query().Get("file")
			if file == "" {
				http.Error(w, `{"error": "missing file parameter"}`, http.StatusBadRequest)
				return
			}
			cleanFile := filepath.Base(file)
			filePath = filepath.Join(cfg.LocalDirectory, cleanFile)
			if _, err := os.Stat(filePath); os.IsNotExist(err) {
				// Try Hammerhead download cache
				if cfg.HammerheadAPI.DownloadDir != "" {
					hhPath := filepath.Join(cfg.HammerheadAPI.DownloadDir, cleanFile)
					if _, err := os.Stat(hhPath); err == nil {
						filePath = hhPath
					}
				}
			}
			if _, err := os.Stat(filePath); os.IsNotExist(err) {
				// Try Wahoo download cache
				if cfg.WahooAPI.DownloadDir != "" {
					wahooPath := filepath.Join(cfg.WahooAPI.DownloadDir, cleanFile)
					if _, err := os.Stat(wahooPath); err == nil {
						filePath = wahooPath
					}
				}
			}
		} else if source == "hammerhead" {
			id := r.URL.Query().Get("id")
			if id == "" {
				http.Error(w, `{"error": "missing id parameter"}`, http.StatusBadRequest)
				return
			}
			if force == "true" && cfg.HammerheadAPI.DownloadDir != "" {
				cachedFile := filepath.Join(cfg.HammerheadAPI.DownloadDir, id+".fit")
				os.Remove(cachedFile)
			}
			filePath, err = downloadHammerheadFITFile(cfg.HammerheadAPI, configPath, id)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error": "failed to download activity: %s"}`, err.Error()), http.StatusInternalServerError)
				return
			}
		} else if source == "wahoo" {
			idStr := r.URL.Query().Get("id")
			cdnURL := r.URL.Query().Get("url")
			if idStr == "" || cdnURL == "" {
				http.Error(w, `{"error": "missing id or url parameter"}`, http.StatusBadRequest)
				return
			}
			id, parseErr := strconv.ParseInt(idStr, 10, 64)
			if parseErr != nil {
				http.Error(w, `{"error": "invalid id parameter"}`, http.StatusBadRequest)
				return
			}
			if force == "true" && cfg.WahooAPI.DownloadDir != "" {
				cachedFile := filepath.Join(cfg.WahooAPI.DownloadDir, fmt.Sprintf("%d.fit", id))
				os.Remove(cachedFile)
			}
			filePath, err = downloadWahooFITFile(cfg.WahooAPI, cdnURL, id)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error": "failed to download activity from Wahoo: %s"}`, err.Error()), http.StatusInternalServerError)
				return
			}
		} else if source == "intervals" {
			id := r.URL.Query().Get("id")
			if id == "" {
				http.Error(w, `{"error": "missing id parameter"}`, http.StatusBadRequest)
				return
			}
			if force == "true" && cfg.IntervalsAPI.DownloadDir != "" {
				cachedFile := filepath.Join(cfg.IntervalsAPI.DownloadDir, id+".fit")
				os.Remove(cachedFile)
			}
			filePath, err = downloadIntervalsFITFile(cfg.IntervalsAPI, id)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error": "failed to download activity from Intervals.icu: %s"}`, err.Error()), http.StatusInternalServerError)
				return
			}
		} else {
			http.Error(w, `{"error": "invalid source parameter"}`, http.StatusBadRequest)
			return
		}

		bikeName := r.URL.Query().Get("bike")
		if bikeName != "" {
			found := false
			for _, b := range cfg.Bikes {
				if b.Name == bikeName {
					cfg.FrontGears = b.FrontGears
					cfg.RearGears = b.RearGears
					found = true
					break
				}
			}
			if !found {
				http.Error(w, fmt.Sprintf(`{"error": "bike profile '%s' not found"}`, bikeName), http.StatusBadRequest)
				return
			}
		}

		analysis, err := analyzeFITFile(filePath, cfg)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "failed to analyze FIT file: %s"}`, err.Error()), http.StatusInternalServerError)
			return
		}

		analysis.Source = source
		if source == "local" {
			analysis.Param = r.URL.Query().Get("file")
		} else if source == "hammerhead" {
			analysis.Param = r.URL.Query().Get("id")
		} else if source == "wahoo" {
			analysis.Param = r.URL.Query().Get("id")
			analysis.Param2 = r.URL.Query().Get("url")
		}

		json.NewEncoder(w).Encode(analysis)
	})

	http.HandleFunc("/api/intervals/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cfg := loadConfig(configPath)

		if r.Method == http.MethodGet {
			type ConfigResp struct {
				Enabled   bool   `json:"enabled"`
				AthleteID string `json:"athlete_id"`
				HasAPIKey bool   `json:"has_api_key"`
			}
			json.NewEncoder(w).Encode(ConfigResp{
				Enabled:   cfg.IntervalsAPI.Enabled,
				AthleteID: cfg.IntervalsAPI.AthleteID,
				HasAPIKey: cfg.IntervalsAPI.APIKey != "",
			})
			return
		}

		if r.Method == http.MethodPost {
			var req struct {
				Enabled   bool   `json:"enabled"`
				AthleteID string `json:"athlete_id"`
				APIKey    string `json:"api_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error": "invalid request body"}`, http.StatusBadRequest)
				return
			}

			cfg.IntervalsAPI.Enabled = req.Enabled
			cfg.IntervalsAPI.AthleteID = strings.TrimSpace(req.AthleteID)
			if req.APIKey != "" {
				cfg.IntervalsAPI.APIKey = strings.TrimSpace(req.APIKey)
			}

			if err := saveConfig(configPath, cfg); err != nil {
				http.Error(w, fmt.Sprintf(`{"error": "failed to save config: %s"}`, err.Error()), http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status": "success"}`))
			return
		}

		http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
	})

	http.HandleFunc("/api/intervals/test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			AthleteID string `json:"athlete_id"`
			APIKey    string `json:"api_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error": "invalid request body"}`, http.StatusBadRequest)
			return
		}

		athleteID := strings.TrimSpace(req.AthleteID)
		apiKey := strings.TrimSpace(req.APIKey)
		if athleteID == "" {
			athleteID = "0"
		}

		if apiKey == "" {
			cfg := loadConfig(configPath)
			apiKey = cfg.IntervalsAPI.APIKey
			if athleteID == "0" && cfg.IntervalsAPI.AthleteID != "" {
				athleteID = cfg.IntervalsAPI.AthleteID
			}
		}

		if apiKey == "" {
			http.Error(w, `{"error": "API Key is required"}`, http.StatusBadRequest)
			return
		}

		testURL := fmt.Sprintf("https://intervals.icu/api/v1/athlete/%s", athleteID)
		client := &http.Client{Timeout: 10 * time.Second}
		testReq, err := http.NewRequest("GET", testURL, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "failed to build request: %s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		testReq.SetBasicAuth("API_KEY", apiKey)

		resp, err := client.Do(testReq)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "connection failed: %s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var athleteInfo map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&athleteInfo); err == nil {
				if name, ok := athleteInfo["name"].(string); ok {
					json.NewEncoder(w).Encode(map[string]interface{}{
						"status":  "success",
						"message": fmt.Sprintf("Connected successfully as %s!", name),
					})
					return
				}
			}
			w.Write([]byte(`{"status": "success", "message": "Connected successfully!"}`))
			return
		}

		http.Error(w, fmt.Sprintf(`{"error": "intervals.icu returned status %d"}`, resp.StatusCode), http.StatusBadRequest)
	})

	http.HandleFunc("/api/intervals/export", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		cfg := loadConfig(configPath)
		if !cfg.IntervalsAPI.Enabled || cfg.IntervalsAPI.APIKey == "" {
			http.Error(w, `{"error": "Intervals.icu integration is not configured or disabled"}`, http.StatusBadRequest)
			return
		}

		type PlannedWorkout struct {
			Date                  string  `json:"date"`
			DateKey               string  `json:"date_key"`
			Day                   string  `json:"day"`
			WorkoutType           string  `json:"workout_type"`
			Title                 string  `json:"title"`
			Description           string  `json:"description"`
			DurationMins          int     `json:"duration_mins"`
			TargetTSS             int     `json:"target_tss"`
			TargetIF              float64 `json:"target_if"`
			Structure             string  `json:"structure"`
			IntervalsIcuStructure string  `json:"intervals_icu_structure"`
		}

		var req struct {
			StartDate string           `json:"start_date"`
			Workouts  []PlannedWorkout `json:"workouts"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error": "invalid request body"}`, http.StatusBadRequest)
			return
		}

		if len(req.Workouts) == 0 {
			http.Error(w, `{"error": "no workouts provided to export"}`, http.StatusBadRequest)
			return
		}

		parsedStart, err := time.Parse(time.RFC3339, req.StartDate)
		if err != nil {
			parsedStart, err = time.Parse("2006-01-02T15:04:05Z", req.StartDate)
			if err != nil {
				parsedStart, err = time.Parse("2006-01-02", req.StartDate)
				if err != nil {
					http.Error(w, fmt.Sprintf(`{"error": "invalid start_date format: %s"}`, err.Error()), http.StatusBadRequest)
					return
				}
			}
		}

		athleteID := cfg.IntervalsAPI.AthleteID
		if athleteID == "" {
			athleteID = "0"
		}

		// Dynamically compute query range for existing events based on input workouts
		var oldest, newest string
		for _, wkt := range req.Workouts {
			dStr := wkt.Date
			if dStr == "" {
				dStr = wkt.DateKey
			}
			if dStr == "" {
				continue
			}
			if len(dStr) > 10 {
				dStr = dStr[:10]
			}
			if oldest == "" || dStr < oldest {
				oldest = dStr
			}
			if newest == "" || dStr > newest {
				newest = dStr
			}
		}

		if oldest == "" {
			oldest = parsedStart.Format("2006-01-02")
		}
		if newest == "" {
			newest = parsedStart.AddDate(0, 0, 7).Format("2006-01-02")
		} else {
			// Add 1 day to newest to make query range inclusive
			tMax, err := time.Parse("2006-01-02", newest)
			if err == nil {
				newest = tMax.AddDate(0, 0, 1).Format("2006-01-02")
			}
		}

		getURL := fmt.Sprintf("https://intervals.icu/api/v1/athlete/%s/events?oldest=%s&newest=%s", athleteID, oldest, newest)
		client := &http.Client{Timeout: 15 * time.Second}
		getReq, err := http.NewRequest("GET", getURL, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "failed to build GET request: %s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		getReq.SetBasicAuth("API_KEY", cfg.IntervalsAPI.APIKey)

		getResp, err := client.Do(getReq)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "failed to retrieve calendar events: %s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		defer getResp.Body.Close()

		if getResp.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf(`{"error": "failed to query intervals.icu calendar (status %d)"}`, getResp.StatusCode), http.StatusBadRequest)
			return
		}

		type ExistingEvent struct {
			ID             int64  `json:"id"`
			StartDateLocal string `json:"start_date_local"`
			Category       string `json:"category"`
			Name           string `json:"name"`
		}

		var existingEvents []ExistingEvent
		if err := json.NewDecoder(getResp.Body).Decode(&existingEvents); err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "failed to decode existing events: %s"}`, err.Error()), http.StatusInternalServerError)
			return
		}

		calculateDayOffset := func(startDay time.Weekday, targetDayStr string) int {
			daysMap := map[string]int{
				"Sunday": 0, "Monday": 1, "Tuesday": 2, "Wednesday": 3,
				"Thursday": 4, "Friday": 5, "Saturday": 6,
			}
			targetOffset, ok := daysMap[targetDayStr]
			if !ok {
				return 0
			}
			startOffset := int(startDay)
			diff := targetOffset - startOffset
			return diff
		}

		type ExportResult struct {
			Name   string `json:"name"`
			Day    string `json:"day"`
			Status string `json:"status"`
		}
		var results []ExportResult

		for _, wkt := range req.Workouts {
			if wkt.DurationMins <= 0 || strings.ToLower(wkt.WorkoutType) == "rest" || strings.ToLower(wkt.WorkoutType) == "rest day" || strings.ToLower(wkt.WorkoutType) == "no plan" {
				results = append(results, ExportResult{
					Name:   wkt.Title,
					Day:    wkt.Day,
					Status: "skipped",
				})
				continue
			}

			// Get the target date formatted as YYYY-MM-DD
			targetDateStr := wkt.Date
			if targetDateStr == "" {
				targetDateStr = wkt.DateKey
			}
			if targetDateStr == "" {
				offset := calculateDayOffset(parsedStart.Weekday(), wkt.Day)
				targetDate := parsedStart.AddDate(0, 0, offset)
				targetDateStr = targetDate.Format("2006-01-02")
			} else {
				if len(targetDateStr) > 10 {
					targetDateStr = targetDateStr[:10]
				}
			}

			targetWktNameLower := strings.TrimSpace(strings.ToLower(wkt.Title))

			// Delete existing matching workout if found
			for _, ev := range existingEvents {
				if len(ev.StartDateLocal) >= 10 && ev.StartDateLocal[:10] == targetDateStr {
					if strings.TrimSpace(strings.ToLower(ev.Name)) == targetWktNameLower {
						deleteURL := fmt.Sprintf("https://intervals.icu/api/v1/athlete/%s/events/%d", athleteID, ev.ID)
						delReq, err := http.NewRequest("DELETE", deleteURL, nil)
						if err == nil {
							delReq.SetBasicAuth("API_KEY", cfg.IntervalsAPI.APIKey)
							delResp, err := client.Do(delReq)
							if err == nil {
								delResp.Body.Close()
							}
						}
					}
				}
			}

			indoor := false
			wktType := "Ride"

			titleLower := strings.ToLower(wkt.Title)
			descLower := strings.ToLower(wkt.Description)
			structLower := strings.ToLower(wkt.Structure)
			if strings.Contains(titleLower, "zwift") || strings.Contains(descLower, "zwift") || strings.Contains(structLower, "zwift") {
				indoor = true
				wktType = "VirtualRide"
			}

			fullDescription := ""
			if wkt.IntervalsIcuStructure != "" {
				fullDescription += wkt.IntervalsIcuStructure + "\n\n"
			} else if wkt.Structure != "" {
				fullDescription += wkt.Structure + "\n\n"
			}
			fullDescription += wkt.Description
			fullDescription += fmt.Sprintf("\n\nIF=%d%%\nTSS=%d", int(wkt.TargetIF*100), wkt.TargetTSS)

			type IntervalsCreatePayload struct {
				StartDateLocal string `json:"start_date_local"`
				Category       string `json:"category"`
				Type           string `json:"type"`
				Name           string `json:"name"`
				Description    string `json:"description"`
				Indoor         bool   `json:"indoor"`
				MovingTime     int    `json:"moving_time"`
			}

			payload := IntervalsCreatePayload{
				StartDateLocal: fmt.Sprintf("%sT08:00:00", targetDateStr),
				Category:       "WORKOUT",
				Type:           wktType,
				Name:           wkt.Title,
				Description:    fullDescription,
				Indoor:         indoor,
				MovingTime:     wkt.DurationMins * 60,
			}

			payloadBytes, _ := json.Marshal(payload)
			postURL := fmt.Sprintf("https://intervals.icu/api/v1/athlete/%s/events", athleteID)
			postReq, err := http.NewRequest("POST", postURL, bytes.NewBuffer(payloadBytes))
			if err != nil {
				results = append(results, ExportResult{
					Name:   wkt.Title,
					Day:    wkt.Day,
					Status: fmt.Sprintf("failed: %s", err.Error()),
				})
				continue
			}
			postReq.Header.Set("Content-Type", "application/json")
			postReq.SetBasicAuth("API_KEY", cfg.IntervalsAPI.APIKey)

			postResp, err := client.Do(postReq)
			if err != nil {
				results = append(results, ExportResult{
					Name:   wkt.Title,
					Day:    wkt.Day,
					Status: fmt.Sprintf("failed: %s", err.Error()),
				})
				continue
			}
			postResp.Body.Close()

			if postResp.StatusCode == http.StatusOK || postResp.StatusCode == http.StatusCreated {
				results = append(results, ExportResult{
					Name:   wkt.Title,
					Day:    wkt.Day,
					Status: "created",
				})
			} else {
				results = append(results, ExportResult{
					Name:   wkt.Title,
					Day:    wkt.Day,
					Status: fmt.Sprintf("failed: status %d", postResp.StatusCode),
				})
			}
		}

		json.NewEncoder(w).Encode(results)
	})

	getStoragePath := func() string {
		if dataDir := os.Getenv("DIRECTEUR_DATA_DIR"); dataDir != "" {
			os.MkdirAll(dataDir, 0755)
			return filepath.Join(dataDir, "storage.json")
		}
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "directeur_storage.json"
		}
		dir := filepath.Join(homeDir, ".directeur")
		os.MkdirAll(dir, 0755)
		return filepath.Join(dir, "storage.json")
	}

	http.HandleFunc("/api/storage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		storagePath := getStoragePath()

		if r.Method == http.MethodGet {
			data, err := os.ReadFile(storagePath)
			if err != nil || len(data) == 0 {
				w.Write([]byte("{}"))
				return
			}
			w.Write(data)
			return
		}

		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, `{"error": "bad request"}`, http.StatusBadRequest)
				return
			}
			defer r.Body.Close()

			err = os.WriteFile(storagePath, body, 0644)
			if err != nil {
				http.Error(w, `{"error": "failed to write storage"}`, http.StatusInternalServerError)
				return
			}
			w.Write([]byte(`{"success": true}`))
			return
		}

		http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
	})

	http.HandleFunc("/api/hammerhead/upload-route", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		cfg := loadConfig(configPath)
		if !cfg.HammerheadAPI.Enabled || (cfg.HammerheadAPI.AuthToken == "" && cfg.HammerheadAPI.RefreshToken == "") {
			http.Error(w, `{"error": "Hammerhead integration is not configured or disabled"}`, http.StatusBadRequest)
			return
		}

		var req struct {
			GPX  string `json:"gpx"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error": "invalid request body"}`, http.StatusBadRequest)
			return
		}

		if req.GPX == "" {
			http.Error(w, `{"error": "no GPX content provided"}`, http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			req.Name = "Planned Route"
		}

		// Helper function to execute the upload
		uploadFile := func(token string) (int, string, error) {
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)

			part, err := writer.CreateFormFile("file", req.Name+".gpx")
			if err != nil {
				return 0, "", err
			}
			_, err = part.Write([]byte(req.GPX))
			if err != nil {
				return 0, "", err
			}
			err = writer.Close()
			if err != nil {
				return 0, "", err
			}

			postURL := "https://api.hammerhead.io/v1/api/routes/file"
			reqUpload, err := http.NewRequest("POST", postURL, body)
			if err != nil {
				return 0, "", err
			}
			reqUpload.Header.Set("Content-Type", writer.FormDataContentType())
			reqUpload.Header.Set("Authorization", "Bearer "+token)

			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Do(reqUpload)
			if err != nil {
				return 0, "", err
			}
			defer resp.Body.Close()

			respBody, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(respBody), nil
		}

		status, bodyStr, err := uploadFile(cfg.HammerheadAPI.AuthToken)
		if err == nil && status == http.StatusUnauthorized && cfg.HammerheadAPI.RefreshToken != "" {
			// Token expired, refresh it
			tokenResp, refreshErr := refreshHammerheadToken(cfg.HammerheadAPI.ClientID, cfg.HammerheadAPI.ClientSecret, cfg.HammerheadAPI.RefreshToken)
			if refreshErr == nil {
				cfg.HammerheadAPI.AuthToken = tokenResp.AccessToken
				if tokenResp.RefreshToken != "" {
					cfg.HammerheadAPI.RefreshToken = tokenResp.RefreshToken
				}
				// Save config
				if saveErr := saveConfig(configPath, cfg); saveErr == nil {
					// Retry upload
					status, bodyStr, err = uploadFile(cfg.HammerheadAPI.AuthToken)
				}
			}
		}

		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "failed to upload route: %s"}`, err.Error()), http.StatusInternalServerError)
			return
		}

		if status != http.StatusOK && status != http.StatusCreated {
			http.Error(w, fmt.Sprintf(`{"error": "Hammerhead API error (status %d): %s"}`, status, bodyStr), http.StatusBadRequest)
			return
		}

		w.Write([]byte(`{"status": "success", "message": "Route uploaded to Hammerhead successfully!"}`))
	})

	err = http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
	if err != nil {
		fmt.Printf("Error starting HTTP server: %v\n", err)
	}
}

func getDashboardTemplate() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>directeurAI - dsAI Cycling Dashboard</title>
    <!-- Outfit Font -->
    <link rel="preconnect" href="https://fonts.googleapis.com">
    <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
    <link href="https://fonts.googleapis.com/css2?family=Outfit:wght@300;400;500;600;700;800&display=swap" rel="stylesheet">
    <!-- Chart.js -->
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
    <!-- Leaflet.js for Maps -->
    <link rel="stylesheet" href="https://unpkg.com/leaflet@1.9.4/dist/leaflet.css" integrity="sha256-p4NxAoJBhIIN+hmNHrzRCf9tD/miZyoHS5obTRR9BMY=" crossorigin="" />
    <script src="https://unpkg.com/leaflet@1.9.4/dist/leaflet.js" integrity="sha256-20nQCchB9co0qIjJZRGuk2/Z9VM+kNiyxNV1lvTlZBo=" crossorigin=""></script>
    <!-- Marked.js for Markdown parsing -->
    <script src="https://cdn.jsdelivr.net/npm/marked/marked.min.js"></script>
    <!-- KaTeX for formula rendering -->
    <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/katex@0.16.9/dist/katex.min.css">
    <script defer src="https://cdn.jsdelivr.net/npm/katex@0.16.9/dist/katex.min.js"></script>
    <script defer src="https://cdn.jsdelivr.net/npm/katex@0.16.9/dist/contrib/auto-render.min.js"></script>
    <style>
        :root {
            /* Fallback defaults: Giro Pink */
            --bg-primary: #0a0a0c;
            --bg-secondary: #13131a;
            --bg-tertiary: #1b1b26;
            --accent: #E45C86;
            --accent-glow: rgba(228, 92, 134, 0.15);
            --text-primary: #ffffff;
            --text-secondary: #94a3b8;
            --border-color: #27273a;
            --font-family: 'Outfit', -apple-system, BlinkMacSystemFont, sans-serif;
            --grid-color: rgba(255, 255, 255, 0.03);
        }

        /* Classics Flandrian (March) Theme */
        .theme-flandrian {
            --bg-primary: #0b0b0a;
            --bg-secondary: #141412;
            --bg-tertiary: #1d1d1a;
            --accent: #F5C400;
            --accent-glow: rgba(245, 196, 0, 0.15);
            --border-color: #2a2a22;
            --btn-primary-text: #0a0a0c;
        }

        /* Giro Pink (April, May) Theme */
        .theme-giro {
            --bg-primary: #0a0a0c;
            --bg-secondary: #141215;
            --bg-tertiary: #1e1b21;
            --accent: #FF8BB4;
            --accent-glow: rgba(255, 139, 180, 0.15);
            --border-color: #2b252f;
        }

        /* Tour Yellow (June, July) Theme */
        .theme-tour {
            --bg-primary: #080a0f;
            --bg-secondary: #101420;
            --bg-tertiary: #181d2f;
            --accent: #FDE100;
            --accent-glow: rgba(253, 225, 0, 0.15);
            --border-color: #222940;
            --btn-primary-text: #0a0a0c;
        }

        /* Vuelta Red (August, September) Theme */
        .theme-vuelta {
            --bg-primary: #0b0808;
            --bg-secondary: #161010;
            --bg-tertiary: #221818;
            --accent: #E30613;
            --accent-glow: rgba(227, 6, 19, 0.15);
            --border-color: #331f1f;
        }

        /* Carbon Dark (default) Theme */
        .theme-carbon {
            --bg-primary: #0c0d0e;
            --bg-secondary: #16181b;
            --bg-tertiary: #202329;
            --accent: #00d2ff;
            --accent-glow: rgba(0, 210, 255, 0.15);
            --border-color: #2b2f37;
        }

        /* Custom scrollbar for textareas */
        textarea::-webkit-scrollbar {
            width: 6px;
            height: 6px;
        }
        textarea::-webkit-scrollbar-track {
            background: var(--bg-secondary);
            border-radius: 4px;
        }
        textarea::-webkit-scrollbar-thumb {
            background: var(--border-color);
            border-radius: 4px;
        }
        textarea::-webkit-scrollbar-thumb:hover {
            background: var(--accent);
        }

        * {
            box-sizing: border-box;
            margin: 0;
            padding: 0;
        }

        body {
            background-color: var(--bg-primary);
            color: var(--text-primary);
            font-family: var(--font-family);
            padding: 2rem;
            min-height: 100vh;
            transition: all 0.3s ease;
        }

        /* Global Header and Navigation Tabs Styling */
        #global-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            border-bottom: 1px solid var(--border-color);
            padding-bottom: 1rem;
            max-width: 1400px;
            margin: 0 auto 1.5rem auto;
            width: 100%;
            box-sizing: border-box;
        }

        .header-logo-container {
            display: flex;
            align-items: center;
            gap: 0.5rem;
            cursor: pointer;
            user-select: none;
        }

        .header-logo-container h1 {
            font-size: 1.6rem;
            font-weight: 800;
            letter-spacing: -0.03em;
            margin: 0;
            background: linear-gradient(135deg, #ffffff 30%, var(--accent) 100%);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }

        .header-nav-tabs {
            display: flex;
            align-items: center;
            gap: 0.25rem;
            background: rgba(255, 255, 255, 0.03);
            border: 1px solid var(--border-color);
            padding: 0.25rem;
            border-radius: 9999px;
        }

        .nav-tab-btn {
            background: transparent;
            border: none;
            color: var(--text-secondary);
            font-family: inherit;
            font-size: 0.82rem;
            font-weight: 600;
            padding: 0.4rem 1.1rem;
            border-radius: 9999px;
            cursor: pointer;
            transition: all 0.2s ease;
            display: flex;
            align-items: center;
            gap: 0.3rem;
            outline: none;
        }

        .nav-tab-btn:hover {
            color: #ffffff;
            background: rgba(255, 255, 255, 0.05);
        }

        .nav-tab-btn.active {
            color: #ffffff;
            background: var(--accent);
            box-shadow: 0 2px 8px var(--accent-glow);
        }

        .theme-select-compact {
            cursor: pointer;
            appearance: none;
            -webkit-appearance: none;
            font-family: inherit;
            font-size: 0.75rem;
            font-weight: 600;
            text-align: center;
            border-radius: 6px;
            text-transform: uppercase;
            background: var(--bg-tertiary);
            border: 1px solid var(--border-color);
            color: var(--text-primary);
            padding: 0.3rem 0.5rem;
            outline: none;
            transition: border-color 0.2s;
        }

        .theme-select-compact:hover {
            border-color: var(--accent);
        }

        header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 2rem;
            border-bottom: 1px solid var(--border-color);
            padding-bottom: 1.5rem;
        }

        .logo-area h1 {
            font-size: 2.2rem;
            font-weight: 800;
            letter-spacing: -0.05em;
            background: linear-gradient(135deg, #ffffff 30%, var(--accent) 100%);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }

        .logo-area p {
            color: var(--text-secondary);
            font-size: 0.95rem;
            margin-top: 0.25rem;
        }

        .badge {
            background: var(--accent-glow);
            border: 1px solid var(--accent);
            color: var(--accent);
            padding: 0.5rem 1rem;
            border-radius: 9999px;
            font-size: 0.85rem;
            font-weight: 600;
            text-transform: uppercase;
            letter-spacing: 0.05em;
            box-shadow: 0 0 15px var(--accent-glow);
        }

        .btn-action {
            background: var(--bg-secondary);
            border: 1px solid var(--border-color);
            color: var(--text-primary);
            padding: 0.5rem 1rem;
            border-radius: 9999px;
            font-size: 0.85rem;
            font-weight: 600;
            cursor: pointer;
            transition: all 0.2s ease;
            font-family: var(--font-family);
            display: flex;
            align-items: center;
            gap: 0.25rem;
        }

        .btn-action:hover {
            border-color: var(--accent);
            background: var(--accent-glow);
            color: var(--accent);
            box-shadow: 0 0 10px var(--accent-glow);
        }

        select.btn-action {
            appearance: none;
            -webkit-appearance: none;
            padding-right: 2rem;
            background-image: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='10' height='10' fill='white'><polygon points='0,3 10,3 5,8'/></svg>");
            background-repeat: no-repeat;
            background-position: right 0.75rem center;
        }
        
        select.btn-action option {
            background-color: var(--bg-secondary);
            color: var(--text-primary);
        }

        /* Dropdown menu container */
        .dropdown {
            position: relative;
            display: inline-block;
        }

        .dropdown-menu {
            display: none;
            position: absolute;
            right: 0;
            top: calc(100% + 0.5rem);
            background: rgba(19, 19, 26, 0.95);
            backdrop-filter: blur(10px);
            -webkit-backdrop-filter: blur(10px);
            border: 1px solid var(--border-color);
            border-radius: 12px;
            box-shadow: 0 10px 25px rgba(0, 0, 0, 0.5), 0 0 15px var(--accent-glow);
            z-index: 1000;
            min-width: 200px;
            overflow: hidden;
            flex-direction: column;
            padding: 0.5rem 0;
        }

        .dropdown.active .dropdown-menu {
            display: flex;
        }

        .dropdown-item {
            background: none;
            border: none;
            color: var(--text-primary);
            padding: 0.6rem 1.2rem;
            text-align: left;
            font-size: 0.85rem;
            font-family: var(--font-family);
            font-weight: 500;
            cursor: pointer;
            transition: all 0.2s ease;
            width: 100%;
            display: flex;
            align-items: center;
            gap: 0.5rem;
            white-space: nowrap;
        }

        .dropdown-item:hover {
            background: var(--accent-glow);
            color: var(--accent);
        }

        /* Stats Grid */
        .stats-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 1.5rem;
            margin-bottom: 2.5rem;
        }

        .stat-card {
            background: var(--bg-secondary);
            border: 1px solid var(--border-color);
            border-radius: 16px;
            padding: 1.5rem;
            position: relative;
            overflow: hidden;
            transition: transform 0.2s ease, border-color 0.2s ease;
        }

        .stat-card:hover {
            transform: translateY(-2px);
            border-color: var(--accent);
        }

        .stat-card::after {
            content: '';
            position: absolute;
            top: 0;
            left: 0;
            width: 4px;
            height: 100%;
            background: var(--accent);
            opacity: 0.7;
        }

        .stat-label {
            color: var(--text-secondary);
            font-size: 0.85rem;
            font-weight: 500;
            text-transform: uppercase;
            letter-spacing: 0.05em;
            margin-bottom: 0.5rem;
        }

        .stat-value {
            font-size: 1.8rem;
            font-weight: 700;
            display: flex;
            align-items: baseline;
            gap: 0.25rem;
        }

        .stat-unit {
            font-size: 0.9rem;
            color: var(--text-secondary);
            font-weight: 400;
        }

        .stat-subtext {
            margin-top: 0.5rem;
            font-size: 0.8rem;
            color: var(--text-secondary);
        }

        /* Layout Main */
        .layout-main {
            display: grid;
            grid-template-columns: 3fr 1fr;
            gap: 2rem;
        }

        @media (max-width: 1200px) {
            .layout-main {
                grid-template-columns: 1fr;
            }
        }

        .charts-container {
            display: flex;
            flex-direction: column;
            gap: 2rem;
        }

        .card {
            background: var(--bg-secondary);
            border: 1px solid var(--border-color);
            border-radius: 20px;
            padding: 1.5rem;
        }

        .card-header {
            margin-bottom: 1.5rem;
            display: flex;
            justify-content: space-between;
            align-items: center;
        }

        .card-title {
            font-size: 1.2rem;
            font-weight: 600;
            letter-spacing: -0.02em;
        }

        .chart-wrapper {
            position: relative;
            height: 280px;
            width: 100%;
        }

        /* Leaflet custom styling */
        .leaflet-container {
            background-color: var(--bg-primary) !important;
        }

        /* Gear Sidebar */
        .gear-sidebar {
            display: flex;
            flex-direction: column;
            gap: 2rem;
        }

        .gear-list {
            list-style: none;
            display: flex;
            flex-direction: column;
            gap: 0.75rem;
        }

        .gear-item {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding: 0.75rem 1rem;
            background: var(--bg-tertiary);
            border-radius: 12px;
            border: 1px solid var(--border-color);
        }

        .gear-combo {
            font-weight: 600;
            font-size: 1rem;
        }

        .gear-bar-container {
            flex: 1;
            margin: 0 1.5rem;
            background: rgba(255, 255, 255, 0.05);
            height: 8px;
            border-radius: 9999px;
            overflow: hidden;
            position: relative;
        }

        .gear-bar {
            height: 100%;
            background: var(--accent);
            border-radius: 9999px;
        }

        .gear-duration {
            font-size: 0.9rem;
            font-weight: 500;
            color: var(--text-secondary);
            text-align: right;
            min-width: 70px;
        }

        .shifting-stats {
            display: grid;
            grid-template-columns: 1fr 1fr;
            gap: 1rem;
            margin-top: 1rem;
        }

        .shift-stat-box {
            background: var(--bg-tertiary);
            padding: 1rem;
            border-radius: 12px;
            text-align: center;
            border: 1px solid var(--border-color);
        }

        .shift-stat-value {
            font-size: 1.5rem;
            font-weight: 700;
            color: var(--accent);
        }

        .shift-stat-label {
            font-size: 0.75rem;
            color: var(--text-secondary);
            text-transform: uppercase;
            letter-spacing: 0.05em;
            margin-top: 0.25rem;
        }
        @keyframes spin {
            0% { transform: rotate(0deg); }
            100% { transform: rotate(360deg); }
        }
        @keyframes pulse {
            0% { transform: scale(1); opacity: 0.8; }
            50% { transform: scale(1.08); opacity: 1; }
            100% { transform: scale(1); opacity: 0.8; }
        }

        /* Ride selector and list items styling */
        .ride-list-item {
            background: var(--bg-tertiary);
            border: 1px solid var(--border-color);
            border-radius: 12px;
            padding: 1rem;
            display: flex;
            justify-content: space-between;
            align-items: center;
            cursor: pointer;
            transition: all 0.2s ease;
        }
        .ride-list-item:hover {
            border-color: var(--accent) !important;
            background: var(--accent-glow) !important;
            transform: translateY(-1px);
            box-shadow: 0 4px 12px rgba(0, 0, 0, 0.2);
        }

        /* Markdown rendering styles for AI Coach Report */
        #coach-report-content table {
            width: 100%;
            border-collapse: collapse;
            margin-top: 1rem;
            margin-bottom: 1.5rem;
            font-size: 0.88rem;
            border: 1px solid var(--border-color);
            border-radius: 8px;
            overflow: hidden;
            box-shadow: 0 4px 15px rgba(0, 0, 0, 0.15);
        }
        #coach-report-content th {
            background: rgba(255, 255, 255, 0.05);
            color: #ffffff;
            font-weight: 600;
            text-align: left;
            padding: 0.75rem 1rem;
            border-bottom: 1px solid var(--border-color);
            font-family: 'Outfit';
        }
        #coach-report-content td {
            padding: 0.75rem 1rem;
            border-bottom: 1px solid rgba(255, 255, 255, 0.05);
            color: #e2e8f0;
            line-height: 1.5;
        }
        #coach-report-content tr:last-child td {
            border-bottom: none;
        }
        #coach-report-content tr:nth-child(even) {
            background: rgba(255, 255, 255, 0.015);
        }
        #coach-report-content ul, #coach-report-content ol {
            margin-bottom: 1rem;
            padding-left: 1.5rem;
        }
        #coach-report-content li {
            margin-bottom: 0.35rem;
            line-height: 1.5;
        }
        #coach-report-content p {
            margin-bottom: 1rem;
            line-height: 1.6;
        }
        #coach-report-content hr {
            border: none;
            border-top: 1px solid rgba(255, 255, 255, 0.1);
            margin: 1.5rem 0;
        }
        #coach-report-content blockquote {
            border-left: 4px solid var(--accent);
            background: rgba(255, 255, 255, 0.02);
            padding: 0.75rem 1rem;
            margin: 0 0 1rem 0;
            border-radius: 0 8px 8px 0;
            color: #ffffff;
            font-style: italic;
        }

        /* Landing Page Styling */
        #landing-view {
            display: flex;
            flex-direction: column;
            align-items: center;
            justify-content: center;
            min-height: 100vh;
            background: radial-gradient(circle at center, var(--bg-secondary) 0%, var(--bg-primary) 100%);
            padding: 2.5rem 1.5rem;
            box-sizing: border-box;
            font-family: var(--font-family);
            color: var(--text-primary);
            position: relative;
            overflow: hidden;
        }

        .landing-glow {
            position: absolute;
            width: 400px;
            height: 400px;
            background: radial-gradient(circle, var(--accent-glow) 0%, transparent 70%);
            top: 50%;
            left: 50%;
            transform: translate(-50%, -50%);
            pointer-events: none;
            z-index: 0;
            animation: pulse-glow 8s ease-in-out infinite alternate;
        }

        @keyframes pulse-glow {
            0% { opacity: 0.5; width: 350px; height: 350px; }
            100% { opacity: 0.9; width: 480px; height: 480px; }
        }

        .landing-container {
            max-width: 600px;
            width: 100%;
            text-align: center;
            z-index: 10;
            background: rgba(19, 19, 26, 0.45);
            backdrop-filter: blur(16px);
            -webkit-backdrop-filter: blur(16px);
            border: 1px solid var(--border-color);
            border-radius: 24px;
            padding: 3rem 2rem;
            box-shadow: 0 15px 35px rgba(0, 0, 0, 0.4), 0 0 25px var(--accent-glow);
            transition: transform 0.3s ease;
        }

        .landing-logo-container {
            margin-bottom: 2rem;
            display: flex;
            flex-direction: column;
            align-items: center;
        }

        .landing-logo-icon {
            color: var(--accent);
            filter: drop-shadow(0 0 15px var(--accent));
            animation: float-logo 4s ease-in-out infinite alternate;
        }

        @keyframes float-logo {
            0% { transform: translateY(0) rotate(0deg); }
            50% { transform: translateY(-8px) rotate(1.5deg); }
            100% { transform: translateY(0) rotate(0deg); }
        }

        .landing-title {
            font-size: 3rem;
            font-weight: 800;
            margin: 1.2rem 0 0.5rem 0;
            font-family: 'Outfit', sans-serif;
            letter-spacing: -0.02em;
            background: linear-gradient(135deg, #ffffff 40%, var(--accent) 100%);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }

        .landing-subtitle {
            font-size: 1rem;
            color: var(--text-secondary);
            margin-bottom: 2.5rem;
            font-weight: 400;
            letter-spacing: 0.02em;
            max-width: 440px;
            margin-left: auto;
            margin-right: auto;
            line-height: 1.4;
        }

        .landing-menu {
            display: flex;
            flex-direction: column;
            gap: 1rem;
            max-width: 320px;
            margin: 0 auto;
        }

        .landing-btn {
            background: rgba(255, 255, 255, 0.02);
            border: 1px solid var(--border-color);
            border-radius: 14px;
            padding: 1rem 1.5rem;
            color: #ffffff;
            font-family: inherit;
            font-size: 0.95rem;
            font-weight: 600;
            cursor: pointer;
            text-decoration: none;
            display: flex;
            align-items: center;
            justify-content: center;
            gap: 0.75rem;
            transition: all 0.25s ease;
            box-shadow: 0 4px 6px rgba(0,0,0,0.15);
        }

        .landing-btn:hover {
            background: rgba(255, 255, 255, 0.05);
            border-color: var(--accent);
            transform: translateY(-2px);
        }

        .landing-btn-primary {
            background: var(--accent);
            border-color: var(--accent);
            color: var(--btn-primary-text, #ffffff);
            box-shadow: 0 4px 12px var(--accent-glow);
        }

        .landing-btn-primary:hover {
            background: var(--accent);
            filter: brightness(1.1);
            box-shadow: 0 6px 16px var(--accent-glow);
            transform: translateY(-2px);
        }

        .landing-footer {
            margin-top: 3.5rem;
            font-size: 0.72rem;
            color: var(--text-secondary);
            opacity: 0.7;
            font-family: var(--font-family);
        }

        /* Training Programme Calendar Sizing & Stacking */
        .calendar-day-row {
            transition: all 0.2s ease;
        }
        .calendar-day-row:hover {
            border-color: var(--accent) !important;
            box-shadow: 0 0 12px var(--accent-glow);
            transform: scale(1.005);
        }

        @media (max-width: 768px) {
            .calendar-day-row {
                flex-direction: column;
                gap: 1rem !important;
            }
            .calendar-day-details {
                border-left: none !important;
                padding-left: 0 !important;
                width: 100% !important;
            }
        }

        #global-loading-overlay {
            position: fixed;
            top: 0;
            left: 0;
            width: 100vw;
            height: 100vh;
            background: rgba(10, 10, 12, 0.65);
            backdrop-filter: blur(10px);
            -webkit-backdrop-filter: blur(10px);
            z-index: 9999;
            display: flex;
            flex-direction: column;
            justify-content: center;
            align-items: center;
            color: var(--text-color);
            transition: opacity 0.5s ease-out;
        }
        #global-loading-overlay .spinner {
            width: 50px;
            height: 50px;
            border: 4px solid var(--border);
            border-top: 4px solid var(--accent);
            border-radius: 50%;
            animation: spin 1s linear infinite;
            margin-bottom: 1.5rem;
        }
        @keyframes spin { 0% { transform: rotate(0deg); } 100% { transform: rotate(360deg); } }
    </style>
</head>
<body>
    <!-- Global Loading Overlay -->
    <div id="global-loading-overlay">
        <div class="spinner"></div>
        <h2 style="font-weight: 600; letter-spacing: 1px;">Loading directeur<span style="color: var(--accent); font-weight: 800;">AI</span>...</h2>
        <p style="color: var(--text-muted); font-size: 0.9rem; margin-top: 0.5rem;">Reading local storage & syncing rides...</p>
    </div>

    <!-- Global Header -->
    <header id="global-header">
        <div class="header-logo-container" onclick="switchToView('landing')" title="Go to Home">
            <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="color: var(--accent); filter: drop-shadow(0 0 6px var(--accent-glow)); vertical-align: middle;">
                <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41" stroke-width="2"/>
                <circle cx="12" cy="12" r="7" stroke-width="1.5"/>
                <path d="M12 7c0 2-1 3-3 3 2 0 3 1 3 3 0-2 1-3 3-3-2 0-3-1-3-3z" fill="currentColor" stroke="none"/>
            </svg>
            <h1>directeur<span style="color: var(--accent); font-weight: 800;">AI</span></h1>
        </div>
        
        <div class="header-nav-tabs">
            <button class="nav-tab-btn active" id="nav-tab-landing" onclick="switchToView('landing')">🏠 Home</button>
            <button class="nav-tab-btn" id="nav-tab-dashboard" onclick="switchToView('dashboard')">⚡ Rides</button>
            <button class="nav-tab-btn" id="nav-tab-calendar" onclick="switchToView('calendar')">🗓️ Training Planner</button>
            <button class="nav-tab-btn" id="nav-tab-settings" onclick="switchToView('settings')">⚙️ Settings</button>
            <button class="nav-tab-btn" id="nav-tab-data" onclick="switchToView('data')">📦 Data & Backup</button>
        </div>
        
        <div class="header-actions">
            <select id="theme-selector" class="theme-select-compact">
                <option value="theme-giro">🎨 Giro</option>
                <option value="theme-flandrian">🎨 Flandrian</option>
                <option value="theme-tour">🎨 Tour</option>
                <option value="theme-vuelta">🎨 Vuelta</option>
                <option value="theme-carbon">🎨 Carbon</option>
            </select>
        </div>
    </header>
    <!-- Landing / Home View -->
    <div id="landing-view" style="display: flex; flex-direction: column; padding: 2rem; max-width: 1400px; margin: 0 auto; box-sizing: border-box; width: 100%;">
        <div style="margin-bottom: 1.5rem; display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 1rem; width: 100%;">
            <div>
                <h2 style="font-size: 1.6rem; font-weight: 700; color: #ffffff; font-family: 'Outfit'; margin: 0;">Welcome back to directeur<span style="color: var(--accent);">AI</span></h2>
                <p style="color: var(--text-secondary); font-size: 0.9rem; margin-top: 0.25rem;">Here is your unified activity & training schedule for the week.</p>
            </div>
            <!-- Quick Actions -->
            <div style="display: flex; gap: 0.75rem;">
                <button class="btn-action" onclick="switchToView('dashboard')" style="font-weight: 600;">⚡ View Rides Dashboard</button>
                <button class="btn-action" onclick="switchToView('calendar')" style="font-weight: 600;">🗓️ Plan Training Week</button>
            </div>
        </div>

        <!-- Weekly Unified Calendar Container -->
        <div class="card" style="margin-bottom: 2rem; padding: 1.25rem; width: 100%; box-sizing: border-box;">
            <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 0.75rem; margin-bottom: 1rem;">
                <div style="font-family: 'Outfit'; font-weight: 700; font-size: 1.1rem; display: flex; align-items: center; gap: 0.5rem;">
                    <span>📅</span> Unified Training & Activity Calendar
                    <span id="landing-calendar-week-label" style="font-size: 0.85rem; color: var(--text-secondary); font-weight: normal; margin-left: 0.5rem;"></span>
                </div>
                <!-- Week Toggles for Landing -->
                <div style="display: flex; align-items: center; gap: 0.35rem; background: rgba(255, 255, 255, 0.03); border: 1px solid var(--border-color); border-radius: 6px; padding: 0.2rem 0.4rem;">
                    <button class="btn-action" id="btn-landing-prev-week" style="padding: 0.15rem 0.3rem; border: none; background: transparent; font-size: 0.8rem;" title="Previous Week">⬅️</button>
                    <button class="btn-action" id="btn-landing-next-week" style="padding: 0.15rem 0.3rem; border: none; background: transparent; font-size: 0.8rem;" title="Next Week">➡️</button>
                </div>
            </div>
            
            <div id="landing-calendar-grid" style="display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 0.75rem;">
                <!-- Day cards rendered dynamically -->
            </div>
        </div>
        
        <!-- Summary Dashboard Widgets Row -->
        <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 1.5rem; width: 100%;">
            <!-- Widget 1: Training Plan Summary -->
            <div class="card" id="landing-plan-summary-card" style="display: flex; flex-direction: column; gap: 0.75rem;">
                <div class="card-header"><div class="card-title">Weekly Plan Focus</div></div>
                <div id="landing-plan-summary-content" style="font-size: 0.85rem; line-height: 1.5; color: var(--text-secondary);">
                    No active training plan. Go to the <a href="#" onclick="event.preventDefault(); switchToView('calendar');" style="color: var(--accent); font-weight: 600; text-decoration: none;">Training Planner</a> to generate one!
                </div>
            </div>
            
            <!-- Widget 2: Recent Activity Quick Selector -->
            <div class="card" style="display: flex; flex-direction: column; gap: 0.75rem;">
                <div class="card-header"><div class="card-title">Recent Ride Files</div></div>
                <div id="landing-recent-activity-list" style="max-height: 250px; overflow-y: auto; display: flex; flex-direction: column; gap: 0.5rem;">
                    <!-- Lists recent rides -->
                </div>
            </div>
        </div>
    </div>

    <!-- Calendar View Container -->
    <div id="calendar-view" style="display: none; padding: 2rem; max-width: 1400px; margin: 0 auto; min-height: 100vh; box-sizing: border-box; font-family: var(--font-family); color: var(--text-primary);">
        <div class="toolbar" style="display: flex; justify-content: space-between; align-items: center; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 12px; padding: 0.75rem 1rem; margin-bottom: 1.5rem;">
            <div style="display: flex; align-items: center; gap: 1rem;">
                <div style="font-family: 'Outfit'; font-weight: 700; font-size: 1.1rem; color: #ffffff;">🗓️ Training Programme Planner</div>
            </div>
            <div id="calendar-week-nav" style="display: flex; align-items: center; gap: 0.5rem; background: rgba(255, 255, 255, 0.03); border: 1px solid var(--border-color); border-radius: 8px; padding: 0.35rem 0.5rem;">
                <button class="btn-action" onclick="navigateWeek('prev')" style="padding: 0.15rem 0.35rem; border: none; background: transparent; font-size: 0.9rem;" title="Previous Week">⬅️</button>
                <select id="calendar-week-select" class="btn-action" style="background: transparent; border: none; color: #ffffff; font-size: 0.85rem; font-weight: 600; padding: 0 1.5rem 0 0.5rem; outline: none; cursor: pointer; text-align-last: center;" onchange="loadWeekFromSelect(this.value)">
                        <option value="" disabled selected>Select Week</option>
                    </select>
                    <button class="btn-action" onclick="navigateWeek('next')" style="padding: 0.15rem 0.35rem; border: none; background: transparent; font-size: 0.9rem;" title="Next Week">➡️</button>
                </div>
            </div>

        <div style="display: grid; grid-template-columns: 1fr; gap: 2rem; align-items: start; margin-top: 1rem;">
            <!-- Grid columns split for desktop -->
            <div style="display: flex; gap: 2rem; flex-wrap: wrap;">
                <!-- Left Column Container -->
                <div style="flex: 1 1 320px; max-width: 340px; display: flex; flex-direction: column; gap: 1.5rem;">
                    <!-- Planner Configuration Card -->
                    <div class="card" style="display: flex; flex-direction: column; gap: 1.25rem; width: 100%; box-sizing: border-box;">
                        <div class="card-header">
                            <div class="card-title">Planner Configuration</div>
                        </div>
                        
                        <div style="display: flex; flex-direction: column; gap: 0.5rem;">
                            <label style="font-size: 0.8rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Training Goals</label>
                            <textarea id="calendar-goals-input" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; resize: vertical; min-height: 70px; outline: none; transition: border-color 0.2s;" placeholder="e.g. Build FTP and climbing endurance for hilly grand fondo">Build FTP and climbing endurance</textarea>
                        </div>
                        
                        <div style="display: flex; flex-direction: column; gap: 0.5rem;">
                            <label style="font-size: 0.8rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Plan Constraints</label>
                            <textarea id="calendar-constraints-input" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; resize: vertical; min-height: 180px; outline: none; transition: border-color 0.2s;" placeholder="e.g. Tuesday/Thursday trainer sessions capped at 1h. Long ride on Saturday. Monday/Friday Rest.">Monday and Friday are rest days. Long endurance ride on Saturday. Tuesday/Thursday trainer sessions capped at 1 hour.</textarea>
                        </div>

                        <div style="display: flex; flex-direction: column; gap: 0.5rem;">
                            <label style="font-size: 0.8rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Gemini AI Model</label>
                            <select id="calendar-model-select" class="btn-action" style="font-size: 0.85rem; padding: 0.5rem; background: var(--bg-tertiary); border: 1px solid var(--border-color); color: #ffffff; border-radius: 8px; outline: none; width: 100%;">
                                <option value="gemini-3.5-flash" selected>Gemini 3.5 Flash (Default)</option>
                                <option value="gemini-3.5-pro">Gemini 3.5 Pro</option>
                                <option value="gemini-3.1-pro">Gemini 3.1 Pro</option>
                                <option value="gemini-2.5-pro">Gemini 2.5 Pro</option>
                                <option value="gemini-2.5-flash">Gemini 2.5 Flash</option>
                                <option value="gemini-1.5-pro">Gemini 1.5 Pro (Legacy)</option>
                            </select>
                        </div>

                        <div style="display: flex; flex-direction: column; gap: 0.5rem;">
                            <label style="font-size: 0.8rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Duration (Weeks)</label>
                            <select id="calendar-weeks-select" class="btn-action" style="font-size: 0.85rem; padding: 0.5rem; background: var(--bg-tertiary); border: 1px solid var(--border-color); color: #ffffff; border-radius: 8px; outline: none; width: 100%;">
                                <option value="1" selected>1 Week</option>
                                <option value="2">2 Weeks</option>
                                <option value="3">3 Weeks</option>
                                <option value="4">4 Weeks</option>
                                <option value="6">6 Weeks</option>
                                <option value="8">8 Weeks</option>
                                <option value="12">12 Weeks</option>
                            </select>
                        </div>



                        <button id="btn-generate-calendar" onclick="generateTrainingCalendar()" class="landing-btn landing-btn-primary" style="width: 100%; justify-content: center; font-size: 0.9rem; padding: 0.75rem;">
                            🗓️ Generate Programme
                        </button>
                    </div>

                    <!-- Past Programmes Card -->
                    <div class="card" id="calendar-history-card" style="display: flex; flex-direction: column; gap: 1rem; width: 100%; box-sizing: border-box;">
                        <div class="card-header" style="margin-bottom: 0.25rem;">
                            <div class="card-title">Past Programmes</div>
                        </div>
                        <div id="calendar-history-list" style="display: flex; flex-direction: column; gap: 0.75rem; max-height: 320px; overflow-y: auto; padding-right: 0.25rem;">
                            <!-- Dynamically populated -->
                        </div>
                    </div>
                </div>

                <!-- Right Column: Outputs -->
                <div style="flex: 3 1 600px; min-width: 0; display: flex; flex-direction: column; gap: 1.5rem;">
                    <!-- Weekly Focus Box -->
                    <div class="card" id="calendar-summary-box" style="display: none; background: linear-gradient(135deg, rgba(255,255,255,0.01) 0%, rgba(255,255,255,0.03) 100%); border-left: 4px solid var(--accent); flex-direction: column; gap: 0.75rem;">
                        <div style="display: flex; justify-content: space-between; align-items: start; gap: 1rem; flex-wrap: wrap;">
                            <div style="flex: 1 1 300px;">
                                <h4 style="color: var(--accent); margin: 0 0 0.5rem 0; font-family: 'Outfit'; font-weight: 600; font-size: 0.95rem; text-transform: uppercase; letter-spacing: 0.05em;">Weekly Physiological Focus</h4>
                                <p id="calendar-summary-text" style="color: var(--text-secondary); font-size: 0.9rem; line-height: 1.5; margin: 0;"></p>
                            </div>
                            <div id="intervals-export-container" style="flex: 0 0 auto; display: flex; flex-direction: column; gap: 0.5rem; min-width: 210px; padding: 0.6rem 0.8rem; background: rgba(255, 255, 255, 0.02); border: 1px solid var(--border-color); border-radius: 8px;">
                                <div style="display: flex; align-items: center; justify-content: space-between; gap: 0.5rem;">
                                    <span style="font-size: 0.7rem; font-weight: 700; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Intervals.icu Sync</span>
                                    <span id="intervals-status-badge" style="background: rgba(255,255,255,0.08); color: var(--text-secondary); font-size: 0.65rem; border-radius: 4px; padding: 0.1rem 0.3rem; font-weight: bold; border: 1px solid var(--border-color); text-transform: uppercase;">Not Connected</span>
                                </div>
                                <button id="btn-intervals-export" onclick="exportCalendarToIntervals()" class="landing-btn landing-btn-primary" style="justify-content: center; font-size: 0.8rem; padding: 0.45rem 0.6rem; display: flex; align-items: center; gap: 0.35rem; width: 100%; font-weight: 600;" disabled>
                                    📅 Add workouts to calendar
                                </button>
                                <a href="javascript:void(0)" onclick="showIntervalsConfigModal()" style="font-size: 0.7rem; color: var(--accent); text-align: center; text-decoration: none; font-weight: 600; margin-top: 0.1rem;">Configure Connection</a>
                            </div>
                        </div>
                    </div>

                    <!-- Calendar Overview Box -->
                    <div class="card" id="calendar-overview-box" style="display: none; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 16px; padding: 1.25rem;">
                        <h4 style="color: var(--text-secondary); margin: 0 0 1rem 0; font-family: 'Outfit'; font-weight: 600; font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.05em; display: flex; align-items: center; gap: 0.4rem;">
                            <span>📅</span> Weekly Schedule Overview (Click to jump)
                        </h4>
                        <div id="calendar-overview-grid" style="display: grid; grid-template-columns: repeat(7, minmax(80px, 1fr)); gap: 0.75rem; overflow-x: auto; padding-bottom: 0.25rem;">
                            <!-- Will be populated dynamically -->
                        </div>
                    </div>



                    <!-- Calendar Loading Indicator -->
                    <div id="calendar-loading" style="display: none; flex-direction: column; align-items: center; justify-content: center; gap: 1.5rem; padding: 4rem 2rem; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 20px;">
                        <div style="width: 48px; height: 48px; border: 4px solid var(--border-color); border-top: 4px solid var(--accent); border-radius: 50%; animation: spin 1s linear infinite;"></div>
                        <div style="color: var(--text-primary); font-weight: 600; font-size: 1.05rem; font-family: 'Outfit';">directeurAI is planning your week...</div>
                        <div style="color: var(--text-secondary); font-size: 0.85rem; max-width: 320px; text-align: center;">Retrieving recent ride metrics and structuring optimal workout intervals.</div>
                    </div>

                    <!-- Empty State -->
                    <div id="calendar-empty-state" style="display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 1.5rem; padding: 4rem 2rem; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 20px; text-align: center;">
                        <div style="font-size: 3rem; filter: drop-shadow(0 0 10px var(--accent-glow));">🗓️</div>
                        <div style="color: var(--text-primary); font-weight: 600; font-size: 1.1rem; font-family: 'Outfit';">No Training Programme Generated Yet</div>
                        <div style="color: var(--text-secondary); font-size: 0.85rem; max-width: 380px;">Enter your goals and constraints, then click "Generate Programme" to build your custom, AI-coach suggested training week.</div>
                    </div>

                    <!-- Calendar Card Grid -->
                    <div id="calendar-grid" style="display: none; flex-direction: column; gap: 1rem;">
                        <!-- Will be populated dynamically by JS -->
                    </div>
                </div>
            </div>
        </div>
    </div>

    <!-- Dashboard View Container -->
    <div id="dashboard-view" style="display: none;">
        <div id="connection-error-banner" style="display: none; background: rgba(231, 76, 60, 0.15); border-bottom: 1px solid #e74c3c; padding: 0.75rem 1.5rem; text-align: center; font-size: 0.9rem; color: #ffffff; align-items: center; justify-content: center; gap: 1rem; z-index: 1000; position: relative; box-shadow: 0 4px 15px rgba(0,0,0,0.3);">
            <div style="display: flex; align-items: center; gap: 0.5rem; justify-content: center; flex-wrap: wrap;">
                <span style="font-weight: 600; color: #ff6b6b; display: flex; align-items: center; gap: 0.3rem;">
                    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" style="flex-shrink: 0;"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>
                    Hammerhead Account Connection Expired:
                </span>
                <span id="connection-error-message" style="color: #e2e8f0; font-family: monospace; font-size: 0.8rem;">Token refresh failed.</span>
                <a id="btn-reauth-banner" href="#" class="btn-action" style="padding: 0.4rem 1.2rem; font-size: 0.8rem; border-color: #e74c3c; color: #ffffff; background: rgba(231, 76, 60, 0.25); text-decoration: none; border-radius: 8px; font-weight: 600; box-shadow: 0 0 10px rgba(231, 76, 60, 0.2); transition: all 0.2s;">
                    🔗 Re-authorize Account
                </a>
            </div>
        </div>

        <div class="toolbar" style="display: flex; justify-content: space-between; align-items: center; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 12px; padding: 0.75rem 1rem; margin-bottom: 1.5rem; width: 100%; box-sizing: border-box;">
            <div style="display: flex; align-items: center; gap: 1rem; flex-wrap: wrap;">
                <button id="btn-select-ride" class="btn-action" style="font-weight: 600; display: flex; align-items: center; gap: 0.3rem;">📂 Select Ride</button>
                <button id="btn-reparse-ride" class="btn-action" style="font-weight: 600; display: none; align-items: center; gap: 0.3rem;" onclick="reparseCurrentRide()">🔄 Reparse Ride</button>
                <select id="bike-selector" class="btn-action" style="display: none;">
                    <option value="">⚙️ Default Gears</option>
                </select>
                <div id="ride-date-sub" style="font-size: 0.85rem; color: var(--text-secondary);">Cycling Analysis Dashboard</div>
            </div>
            <div style="display: flex; align-items: center; gap: 0.75rem;">
                <button id="btn-gemini-coach" class="btn-action" style="background: linear-gradient(135deg, rgba(155, 89, 182, 0.2), rgba(52, 152, 219, 0.2)); border-color: #9b59b6; color: #e0aaff; font-weight: 600; display: flex; align-items: center; gap: 0.3rem;">🤖 Ask directeurAI Coach</button>
            </div>
        </div>

    <!-- Collapsible Rides Calendar -->
    <div id="rides-calendar-container" style="margin-bottom: 1.5rem;">
        <div style="background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 16px; padding: 0.75rem 1rem; box-shadow: 0 4px 25px rgba(0,0,0,0.3); display: flex; flex-direction: column; gap: 0.5rem; transition: all 0.3s ease;">
            <div style="display: flex; justify-content: space-between; align-items: center; cursor: pointer; user-select: none;" id="rides-calendar-header">
                <div style="display: flex; align-items: center; gap: 0.8rem;">
                    <div style="display: flex; align-items: center; gap: 0.5rem; font-family: 'Outfit'; font-weight: 700; font-size: 0.95rem; color: #ffffff;" id="rides-calendar-title">
                        <span>📅</span> Recent Ride Activity (Last 7 Days)
                    </div>
                    <div style="display: flex; align-items: center; gap: 0.35rem;" id="rides-calendar-nav">
                        <button id="btn-prev-week" class="btn-action" style="padding: 0.15rem 0.4rem; font-size: 0.75rem; font-weight: bold; border-radius: 4px;" title="Previous Week">◀</button>
                        <button id="btn-next-week" class="btn-action" style="padding: 0.15rem 0.4rem; font-size: 0.75rem; font-weight: bold; border-radius: 4px;" title="Next Week" disabled>▶</button>
                    </div>
                </div>
                <div style="display: flex; align-items: center; gap: 0.75rem;">
                    <span id="rides-calendar-stats" style="font-size: 0.78rem; color: var(--text-secondary); font-weight: 500;"></span>
                    <button id="btn-toggle-rides-calendar" style="background: none; border: none; color: var(--accent); cursor: pointer; font-size: 0.8rem; font-weight: 600; display: flex; align-items: center; gap: 0.25rem; font-family: inherit;">
                        <span id="rides-calendar-arrow" style="display: inline-block; transition: transform 0.2s ease;">▲</span> Collapse
                    </button>
                </div>
            </div>
            
            <div id="rides-calendar-content" style="transition: max-height 0.3s ease-in-out, opacity 0.2s ease-in-out; overflow: hidden; max-height: 500px; opacity: 1;">
                <div id="rides-calendar-grid" style="display: grid; grid-template-columns: repeat(7, minmax(130px, 1fr)); gap: 0.5rem; margin-top: 0.25rem; padding-bottom: 0.25rem; overflow-x: auto;">
                    <!-- Populated dynamically -->
                </div>
            </div>
        </div>
    </div>

    <!-- Stats Grid -->
    <div class="stats-grid">
        <div class="stat-card" id="card-np">
            <div class="stat-label">Power (NP)</div>
            <div class="stat-value" id="val-np">- <span class="stat-unit">W</span></div>
            <div class="stat-subtext" id="val-avg-power">Avg: - W</div>
        </div>
        <div class="stat-card" id="card-max-power">
            <div class="stat-label">Max Power</div>
            <div class="stat-value" id="val-max-power">- <span class="stat-unit">W</span></div>
            <div class="stat-subtext" id="val-max-power-sub">-</div>
        </div>
        <div class="stat-card" id="card-tss">
            <div class="stat-label">Training Stress (TSS)</div>
            <div class="stat-value" id="val-tss">-</div>
            <div class="stat-subtext" id="val-tss-details">IF: - (FTP -W)</div>
        </div>
        <div class="stat-card">
            <div class="stat-label">Heart Rate</div>
            <div class="stat-value" id="val-avg-hr">- <span class="stat-unit">bpm</span></div>
            <div class="stat-subtext" id="val-max-hr">Max: - bpm</div>
        </div>
        <div class="stat-card">
            <div class="stat-label">Cadence</div>
            <div class="stat-value" id="val-avg-cadence">- <span class="stat-unit">rpm</span></div>
            <div class="stat-subtext" id="val-max-cadence">Max: - rpm</div>
        </div>
        <div class="stat-card">
            <div class="stat-label">Elevation Gain</div>
            <div class="stat-value" id="val-elevation">- <span class="stat-unit">m</span></div>
            <div class="stat-subtext" id="val-alt-range">Range: - to - m</div>
        </div>
        <div class="stat-card">
            <div class="stat-label">Distance & Time</div>
            <div class="stat-value" id="val-distance">- <span class="stat-unit">km</span></div>
            <div class="stat-subtext" id="val-duration">Duration: -</div>
        </div>
    </div>

    <!-- Main Layout -->
    <div class="layout-main">
        
        <!-- Left Column: Map & Charts -->
        <div class="charts-container">
            
            <!-- Leaflet Map Card -->
            <div class="card">
                <div class="card-header">
                    <div class="card-title">GPS Route Profile</div>
                </div>
                <div id="map" style="height: 380px; width: 100%; border-radius: 12px; border: 1px solid var(--border-color); overflow: hidden;"></div>
            </div>

            <!-- Power & 30s Power Chart -->
            <div class="card" id="card-power-timeline">
                <div class="card-header">
                    <div class="card-title">Power Timeline</div>
                </div>
                <div class="chart-wrapper">
                    <canvas id="chart-power"></canvas>
                </div>
            </div>

            <!-- Speed & Altitude Chart -->
            <div class="card">
                <div class="card-header">
                    <div class="card-title">Speed & Altitude</div>
                </div>
                <div class="chart-wrapper">
                    <canvas id="chart-speed-alt"></canvas>
                </div>
            </div>

            <!-- Heart Rate & Cadence Chart -->
            <div class="card">
                <div class="card-header">
                    <div class="card-title">Heart Rate & Cadence</div>
                </div>
                <div class="chart-wrapper">
                    <canvas id="chart-hr-cadence"></canvas>
                </div>
                <!-- Cadence Stats Grid -->
                <div id="cadence-stats-container" style="margin-top: 1rem; border-top: 1px solid var(--border-color); padding-top: 1rem;">
                    <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); gap: 1rem; text-align: center;">
                        <div>
                            <div style="font-size: 0.72rem; color: var(--text-secondary); text-transform: uppercase; font-weight: 600; letter-spacing: 0.03em;">Normalised Cadence</div>
                            <div id="stats-norm-cadence" style="font-size: 1.2rem; font-weight: 700; color: #2ecc71; margin-top: 0.2rem;">- <span style="font-size: 0.75rem; font-weight: 500; color: var(--text-secondary);">rpm</span></div>
                        </div>
                        <div>
                            <div style="font-size: 0.72rem; color: var(--text-secondary); text-transform: uppercase; font-weight: 600; letter-spacing: 0.03em;">Pedalling Time</div>
                            <div id="stats-pedalling-percent" style="font-size: 1.2rem; font-weight: 700; color: var(--text-primary); margin-top: 0.2rem;">- <span style="font-size: 0.75rem; font-weight: 500; color: var(--text-secondary);">%</span></div>
                        </div>
                        <div>
                            <div style="font-size: 0.72rem; color: var(--text-secondary); text-transform: uppercase; font-weight: 600; letter-spacing: 0.03em;">Pedalling Range</div>
                            <div id="stats-pedalling-range" style="font-size: 1.2rem; font-weight: 700; color: var(--text-primary); margin-top: 0.2rem;">- <span style="font-size: 0.75rem; font-weight: 500; color: var(--text-secondary);">rpm</span></div>
                        </div>
                        <div>
                            <div style="font-size: 0.72rem; color: var(--text-secondary); text-transform: uppercase; font-weight: 600; letter-spacing: 0.03em;">Pedalling StDev</div>
                            <div id="stats-cadence-variance" style="font-size: 1.2rem; font-weight: 700; color: var(--text-primary); margin-top: 0.2rem;">- <span style="font-size: 0.75rem; font-weight: 500; color: var(--text-secondary);">rpm</span></div>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Advanced Shifting Analytics (Gear Choice vs. Terrain) -->
            <div class="card">
                <div class="card-header">
                    <div class="card-title">Advanced Shifting Analytics (Gear Choice vs. Terrain)</div>
                </div>
                <div style="font-size: 0.82rem; color: var(--text-secondary); line-height: 1.5; margin: 0.5rem 0.25rem; border-left: 3px solid var(--accent); padding-left: 0.75rem;">
                    Analyzes drivetrain efficiency by mapping your gear selection (ratios) against the terrain profile. Highlights <strong>Cross-Chaining</strong> (Big front ring to largest rear cogs, or small front ring to smallest rear cogs) which increases mechanical friction, chain noise, and drivetrain wear.
                </div>
                <div style="display: flex; gap: 1.5rem; flex-wrap: wrap; padding: 1rem 0;">
                    <div style="flex: 1.5; min-width: 320px; height: 380px;">
                        <canvas id="chart-alt-gears"></canvas>
                    </div>
                    <div style="flex: 1; min-width: 280px; display: flex; flex-direction: column; justify-content: center; gap: 1rem; padding: 0 0.5rem;">
                        <h4 style="color: var(--accent); margin: 0 0 0.5rem 0; font-family: 'Outfit'; font-weight: 600; border-bottom: 1px solid var(--border-color); padding-bottom: 0.25rem;">Drivetrain Statistics</h4>
                        <table style="width: 100%; border-collapse: collapse; font-size: 0.85rem; color: var(--text-secondary);">
                            <tbody>
                                <tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
                                    <td style="padding: 0.5rem 0; font-weight: 600; color: #ffffff;">Total Shifts</td>
                                    <td id="shift-total" style="padding: 0.5rem 0; text-align: right; color: var(--text-primary); font-weight: bold;">-</td>
                                </tr>
                                <tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
                                    <td style="padding: 0.5rem 0; font-weight: 600; color: #ffffff;">Front / Rear Shifts</td>
                                    <td id="shift-front-rear" style="padding: 0.5rem 0; text-align: right; color: var(--text-primary); font-weight: bold;">-</td>
                                </tr>
                                <tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
                                    <td style="padding: 0.5rem 0; font-weight: 600; color: #ffffff;">Big-Big Cross-Chaining</td>
                                    <td id="shift-cross-big" style="padding: 0.5rem 0; text-align: right; color: var(--text-primary); font-weight: bold;">-</td>
                                </tr>
                                <tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
                                    <td style="padding: 0.5rem 0; font-weight: 600; color: #ffffff;">Small-Small Cross-Chaining</td>
                                    <td id="shift-cross-small" style="padding: 0.5rem 0; text-align: right; color: var(--text-primary); font-weight: bold;">-</td>
                                </tr>
                            </tbody>
                        </table>
                        <div style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; padding: 0.75rem; font-size: 0.8rem; line-height: 1.4;">
                            <div style="font-weight: 600; color: var(--accent); margin-bottom: 0.25rem; font-family: 'Outfit';">Drivetrain Diagnostics</div>
                            <div id="drivetrain-diagnostics-text" style="color: var(--text-secondary);">Calculating stats...</div>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Training Zones Distribution -->
            <div class="card">
                <div class="card-header">
                    <div class="card-title">Training Zones Distribution</div>
                </div>
                <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 1.5rem; padding: 1rem 0;">
                    <div id="container-chart-power-zones" style="height: 260px;">
                        <canvas id="chart-power-zones"></canvas>
                    </div>
                    <div style="height: 260px;">
                        <canvas id="chart-hr-zones"></canvas>
                    </div>
                </div>
                <div style="margin-top: 1rem; border-top: 1px solid var(--border-color); padding-top: 1rem;">
                    <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 1.5rem;">
                        <div id="container-power-zones-table">
                            <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; flex-wrap: wrap; gap: 0.5rem;">
                                <h4 style="color: var(--accent); margin: 0; font-family: 'Outfit'; font-weight: 600;">Power Zones</h4>
                                <div style="font-size: 0.85rem; color: var(--text-secondary); display: flex; align-items: center; gap: 0.25rem;">
                                    FTP: 
                                    <input type="number" id="ftp-input" value="250" style="width: 55px; background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 4px; color: #ffffff; text-align: center; font-family: inherit; font-size: 0.82rem; padding: 0.1rem 0.2rem; outline: none; border-color: rgba(255,255,255,0.15);" />
                                    <span>W</span>
                                </div>
                            </div>
                            <table style="width: 100%; border-collapse: collapse; font-size: 0.85rem; color: var(--text-secondary);">
                                <thead>
                                    <tr style="border-bottom: 1px solid var(--border-color); text-align: left;">
                                        <th style="padding: 0.4rem 0;">Zone</th>
                                        <th style="padding: 0.4rem 0;">Range</th>
                                        <th style="padding: 0.4rem 0; text-align: right;">Time</th>
                                        <th style="padding: 0.4rem 0; text-align: right;">%</th>
                                    </tr>
                                </thead>
                                <tbody id="power-zones-tbody">
                                </tbody>
                            </table>
                        </div>
                        <div>
                            <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; flex-wrap: wrap; gap: 0.5rem;">
                                <h4 style="color: var(--accent); margin: 0; font-family: 'Outfit'; font-weight: 600;">Heart Rate Zones <span id="zones-max-hr" style="display: none;">-</span></h4>
                                <div style="font-size: 0.85rem; color: var(--text-secondary); display: flex; align-items: center; gap: 0.25rem;">
                                    Max HR: 
                                    <input type="number" id="max-hr-input" value="190" style="width: 50px; background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 4px; color: #ffffff; text-align: center; font-family: inherit; font-size: 0.82rem; padding: 0.1rem 0.2rem; outline: none; border-color: rgba(255,255,255,0.15);" />
                                    <span>bpm</span>
                                </div>
                            </div>
                            <table style="width: 100%; border-collapse: collapse; font-size: 0.85rem; color: var(--text-secondary);">
                                <thead>
                                    <tr style="border-bottom: 1px solid var(--border-color); text-align: left;">
                                        <th style="padding: 0.4rem 0;">Zone</th>
                                        <th style="padding: 0.4rem 0;">Range</th>
                                        <th style="padding: 0.4rem 0; text-align: right;">Time</th>
                                        <th style="padding: 0.4rem 0; text-align: right;">%</th>
                                    </tr>
                                </thead>
                                <tbody id="hr-zones-tbody">
                                </tbody>
                            </table>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Neuromuscular vs. Aerobic Load (Quadrant Analysis) -->
            <div class="card" id="card-quadrant">
                <div class="card-header">
                    <div class="card-title">Neuromuscular vs. Aerobic Load (Quadrant Analysis)</div>
                </div>
                <div style="font-size: 0.82rem; color: var(--text-secondary); line-height: 1.5; margin: 0.5rem 0.25rem; border-left: 3px solid var(--accent); padding-left: 0.75rem;">
                    This chart plots Average Effective Pedal Force (AEPF) in Newtons against Circumferential Pedal Velocity (CPV) in m/s. The dashed crosshairs indicate your mean force and velocity, dividing the plot into four quadrants: <strong>QI (High Force / High Velocity - Sprinting)</strong>, <strong>QII (High Force / Low Velocity - Climbing/Mashing)</strong>, <strong>QIII (Low Force / Low Velocity - Recovery)</strong>, and <strong>QIV (Low Force / High Velocity - Spinning)</strong>, showing how your muscle recruitment and aerobic contribution were balanced during active pedaling.
                </div>
                <div style="display: flex; gap: 1.5rem; flex-wrap: wrap; padding: 1rem 0;">
                    <div style="flex: 1.5; min-width: 320px; height: 380px;">
                        <canvas id="chart-quadrant-analysis"></canvas>
                    </div>
                    <div style="flex: 1; min-width: 250px; display: flex; flex-direction: column; justify-content: center; gap: 1rem;">
                        <h4 style="color: var(--accent); margin: 0 0 0.5rem 0; font-family: 'Outfit'; font-weight: 600; border-bottom: 1px solid var(--border-color); padding-bottom: 0.25rem;">Quadrant Statistics</h4>
                        <table style="width: 100%; border-collapse: collapse; font-size: 0.85rem; color: var(--text-secondary);">
                            <tbody>
                                <tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
                                    <td style="padding: 0.5rem 0; font-weight: 600; color: #ffffff;">Total Active Pedaling Time</td>
                                    <td id="quad-active-time" style="padding: 0.5rem 0; text-align: right; color: var(--text-primary); font-weight: bold;">-</td>
                                </tr>
                                <tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
                                    <td style="padding: 0.5rem 0; font-weight: 600; color: #ffffff;">Mean Active Power</td>
                                    <td id="quad-mean-power" style="padding: 0.5rem 0; text-align: right; color: var(--text-primary); font-weight: bold;">-</td>
                                </tr>
                                <tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
                                    <td style="padding: 0.5rem 0; font-weight: 600; color: #ffffff;">Mean Active Cadence</td>
                                    <td id="quad-mean-cadence" style="padding: 0.5rem 0; text-align: right; color: var(--text-primary); font-weight: bold;">-</td>
                                </tr>
                                <tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
                                    <td style="padding: 0.5rem 0; font-weight: 600; color: #ffffff;">Mean Pedal Velocity (CPV)</td>
                                    <td id="quad-mean-cpv" style="padding: 0.5rem 0; text-align: right; color: var(--text-primary); font-weight: bold;">-</td>
                                </tr>
                                <tr style="border-bottom: 1px solid var(--border-color);">
                                    <td style="padding: 0.5rem 0; font-weight: 600; color: #ffffff;">Mean Pedal Force (AEPF)</td>
                                    <td id="quad-mean-aepf" style="padding: 0.5rem 0; text-align: right; color: var(--text-primary); font-weight: bold;">-</td>
                                </tr>
                            </tbody>
                        </table>
                        
                        <h4 style="color: var(--accent); margin: 0.5rem 0 0.5rem 0; font-family: 'Outfit'; font-weight: 600; border-bottom: 1px solid var(--border-color); padding-bottom: 0.25rem;">Time Distribution</h4>
                        <table style="width: 100%; border-collapse: collapse; font-size: 0.85rem; color: var(--text-secondary);">
                            <tbody>
                                <tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
                                    <td style="padding: 0.4rem 0; font-weight: 600; color: #ff8b6b;"><span style="display:inline-block; width:8px; height:8px; border-radius:50%; background:#ff8b6b; margin-right:6px;"></span>QI (High Force / High Vel)</td>
                                    <td id="quad-pct-1" style="padding: 0.4rem 0; text-align: right; color: #ffffff; font-weight: bold;">-</td>
                                </tr>
                                <tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
                                    <td style="padding: 0.4rem 0; font-weight: 600; color: #f1c40f;"><span style="display:inline-block; width:8px; height:8px; border-radius:50%; background:#f1c40f; margin-right:6px;"></span>QII (High Force / Low Vel)</td>
                                    <td id="quad-pct-2" style="padding: 0.4rem 0; text-align: right; color: #ffffff; font-weight: bold;">-</td>
                                </tr>
                                <tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
                                    <td style="padding: 0.4rem 0; font-weight: 600; color: #3498db;"><span style="display:inline-block; width:8px; height:8px; border-radius:50%; background:#3498db; margin-right:6px;"></span>QIII (Low Force / Low Vel)</td>
                                    <td id="quad-pct-3" style="padding: 0.4rem 0; text-align: right; color: #ffffff; font-weight: bold;">-</td>
                                </tr>
                                <tr style="border-bottom: 1px solid var(--border-color);">
                                    <td style="padding: 0.4rem 0; font-weight: 600; color: #2ecc71;"><span style="display:inline-block; width:8px; height:8px; border-radius:50%; background:#2ecc71; margin-right:6px;"></span>QIV (Low Force / High Vel)</td>
                                    <td id="quad-pct-4" style="padding: 0.4rem 0; text-align: right; color: #ffffff; font-weight: bold;">-</td>
                                </tr>
                            </tbody>
                        </table>
                    </div>
                </div>
            </div>

            <!-- FTP History & Estimation Card -->
            <div class="card" id="card-ftp-history">
                <div class="card-header">
                    <div class="card-title">FTP History & Estimation</div>
                </div>
                <div style="padding: 0.5rem 0; display: flex; flex-direction: column; gap: 1rem;">
                    <div style="font-size: 0.85rem; color: var(--text-secondary); line-height: 1.5;">
                        FTP (Functional Threshold Power) is the highest average power output you can sustain for approximately one hour. 
                        It is standardly estimated as <strong>95% of your peak 20-minute power</strong>. Below are the estimated FTP calculations from your analyzed rides:
                    </div>
                    
                    <div style="display: flex; gap: 1.5rem; flex-wrap: wrap;">
                        <div style="flex: 1; min-width: 280px; background: rgba(255,255,255,0.02); border: 1px solid rgba(255,255,255,0.05); border-radius: 8px; padding: 1rem; display: flex; flex-direction: column; justify-content: space-between;">
                            <div>
                                <h4 style="color: var(--accent); margin: 0 0 0.5rem 0; font-family: 'Outfit'; font-weight: 600;">Current Ride Estimate</h4>
                                <div id="ftp-current-estimate-val" style="font-size: 1.8rem; font-weight: bold; color: #ffffff; margin-bottom: 0.25rem;">-</div>
                                <div id="ftp-current-estimate-method" style="font-size: 0.75rem; color: var(--text-secondary);">Calculating...</div>
                            </div>
                            <div style="margin-top: 1rem; border-top: 1px solid rgba(255,255,255,0.05); padding-top: 0.75rem; display: flex; justify-content: space-between; align-items: center;">
                                <span style="font-size: 0.75rem; color: var(--text-secondary);">Apply to dashboard:</span>
                                <button id="btn-apply-current-ftp" class="btn-action" style="font-size: 0.7rem; padding: 0.2rem 0.5rem; border-color: var(--accent); color: var(--accent); cursor: pointer;">Apply FTP</button>
                            </div>
                        </div>
                        
                        <div style="flex: 1.5; min-width: 320px; display: flex; flex-direction: column; gap: 0.5rem;">
                            <h4 style="color: var(--accent); margin: 0 0 0.25rem 0; font-family: 'Outfit'; font-weight: 600;">Historical FTP Estimates</h4>
                            <div id="ftp-history-list" style="max-height: 180px; overflow-y: auto; display: flex; flex-direction: column; gap: 0.4rem; padding-right: 0.25rem;">
                                <!-- Will be populated by JS -->
                            </div>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Dynamic Custom Cards Container -->
            <div id="dynamic-cards-container" style="display: flex; flex-direction: column; gap: 1.5rem; margin-top: 1.5rem;">
                <!-- Dynamically generated cards will append here -->
            </div>

        </div>

        <!-- Right Column: Sidebar Stats -->
        <div class="gear-sidebar">
            
            <!-- Shifting Card -->
            <div class="card">
                <div class="card-header">
                    <div class="card-title">Shifting Summary</div>
                </div>
                
                <div class="shifting-stats">
                    <div class="shift-stat-box">
                        <div class="shift-stat-value" id="val-shifts-front">-</div>
                        <div class="shift-stat-label">Front Shifts</div>
                    </div>
                    <div class="shift-stat-box">
                        <div class="shift-stat-value" id="val-shifts-rear">-</div>
                        <div class="shift-stat-label">Rear Shifts</div>
                    </div>
                </div>
                <div class="shifting-stats" style="margin-top: 0.5rem;">
                    <div class="shift-stat-box" style="grid-column: span 2;">
                        <div class="shift-stat-value" id="val-shifts-total">-</div>
                        <div class="shift-stat-label">Total Shifts</div>
                    </div>
                </div>
            </div>

            <!-- Peak Power Profile (MMP) Card -->
            <div class="card" id="card-power-curve">
                <div class="card-header">
                    <div class="card-title">Peak Power Profile (MMP)</div>
                </div>
                <div class="chart-wrapper" style="height: 250px;">
                    <canvas id="chart-power-curve"></canvas>
                </div>
            </div>

            <!-- Gear Combo Usage Card -->
            <div class="card">
                <div class="card-header">
                    <div class="card-title">Gear Combination Usage</div>
                </div>
                <ul class="gear-list" id="gear-usage-list">
                    <!-- Dynamic List Items -->
                    {{range .GearUsage}}
                    <li class="gear-item">
                        <div>
                            <span class="gear-combo">{{.Combination}}</span>
                        </div>
                        <div class="gear-bar-container">
                            <div class="gear-bar" style="width: {{.Percentage}}%;"></div>
                        </div>
                        <div class="gear-duration">{{printf "%.1f" .Percentage}}%</div>
                    </li>
                    {{else}}
                    <li class="gear-item">No gear data found</li>
                    {{end}}
                </ul>
            </div>

        </div>

    </div>


    <!-- Evolve Control Panel -->
    <div id="evolve-control-panel" class="card" style="margin-top: 1.5rem; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 16px; padding: 1.25rem; box-shadow: 0 4px 25px rgba(0,0,0,0.3); transition: all 0.3s ease;">
        <div style="display: flex; align-items: center; gap: 0.5rem; font-family: 'Outfit'; font-weight: 700; font-size: 1.1rem; color: #ffffff; margin-bottom: 0.75rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">
            <span>💡</span> Evolve directeurAI Dashboard
        </div>
        <div style="font-size: 0.82rem; color: var(--text-secondary); line-height: 1.5; margin-bottom: 1rem;">
            Describe a custom graph, data table, or statistical analysis you want to see. directeurAI will query Gemini to generate sandboxed JavaScript charting code, execute it immediately on your current ride data, and add the new card to your dashboard. Custom cards are saved locally in your browser.
        </div>
        <div style="display: flex; gap: 1rem; flex-wrap: wrap; align-items: flex-start;">
            <div style="flex: 1; min-width: 300px; display: flex; flex-direction: column; gap: 0.5rem;">
                <textarea id="evolve-prompt" placeholder="e.g. Draw a scatter plot of speed (X-axis) vs heart rate (Y-axis) color-coded by altitude, or show a summary of time spent in different cadence ranges." style="width: 100%; height: 90px; background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.75rem; font-family: inherit; font-size: 0.88rem; outline: none; resize: vertical; line-height: 1.4; transition: border-color 0.2s;"></textarea>
            </div>
            <div style="display: flex; flex-direction: column; gap: 0.75rem; min-width: 200px;">
                <div style="display: flex; flex-direction: column; gap: 0.3rem;">
                    <label style="font-size: 0.72rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Gemini AI Model</label>
                    <select id="evolve-model-select" class="badge" style="cursor: pointer; font-family: inherit; font-size: 0.85rem; font-weight: 600; padding: 0.4rem 1.5rem 0.4rem 0.75rem; text-align: left; border-radius: 8px; border: 1px solid var(--border-color); background: var(--bg-tertiary); width: 100%;">
                        <option value="gemini-3.5-flash" selected>Gemini 3.5 Flash (Default)</option>
                        <option value="gemini-3.5-pro">Gemini 3.5 Pro</option>
                        <option value="gemini-3.1-pro">Gemini 3.1 Pro</option>
                    </select>
                </div>
                <button id="btn-evolve-dashboard" class="btn-action" style="background: linear-gradient(135deg, rgba(46, 204, 113, 0.2), rgba(39, 174, 96, 0.2)); border-color: #2ecc71; color: #2ecc71; font-weight: 700; width: 100%; padding: 0.6rem 1rem; display: flex; align-items: center; justify-content: center; gap: 0.5rem; cursor: pointer; border-radius: 8px;">
                    ⚡ Evolve Dashboard
                </button>
            </div>
        </div>
        
        <!-- Loading State Indicator inside card -->
        <div id="evolve-loading" style="display: none; align-items: center; gap: 1rem; margin-top: 1rem; padding-top: 1rem; border-top: 1px solid rgba(255,255,255,0.05);">
            <div style="width: 24px; height: 24px; border: 3px solid var(--border-color); border-top: 3px solid var(--accent); border-radius: 50%; animation: spin 1s linear infinite;"></div>
            <div style="font-size: 0.85rem; color: var(--text-secondary);">
                Querying Gemini for card design & code... (<span id="evolve-status-text">preparing prompt</span>)
            </div>
        </div>
    </div>
    </div> <!-- End dashboard-view -->

    <!-- Settings View Container -->
    <div id="settings-view" style="display: none; padding: 2rem; max-width: 1400px; margin: 0 auto; min-height: 100vh; box-sizing: border-box; font-family: var(--font-family); color: var(--text-primary); width: 100%;">
        <div class="toolbar" style="display: flex; align-items: center; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 12px; padding: 0.75rem 1rem; margin-bottom: 1.5rem; width: 100%; box-sizing: border-box;">
            <div style="font-family: 'Outfit'; font-weight: 700; font-size: 1.1rem; color: #ffffff;">⚙️ Unified Application Settings</div>
        </div>

        <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 1.5rem; align-items: start; width: 100%;">
            <!-- Card 1: FTP Configuration -->
            <div class="card" style="display: flex; flex-direction: column; gap: 1rem;">
                <h4 style="font-family: 'Outfit'; font-weight: 700; font-size: 1rem; color: #ffffff; margin: 0;">Functional Threshold Power (FTP)</h4>
                <p style="color: var(--text-secondary); font-size: 0.8rem; margin: 0; line-height: 1.4;">Configure your Functional Threshold Power (FTP) in Watts. This value is critical for scaling power training zones and calculating Normalized Power (NP).</p>
                <div style="display: flex; flex-direction: column; gap: 0.4rem; margin-top: 0.5rem;">
                    <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase;">Athlete FTP (Watts)</label>
                    <input type="number" id="settings-ftp-input" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none;" value="250">
                </div>
                <button onclick="saveFTPFromSettings()" class="landing-btn landing-btn-primary" style="justify-content: center; font-size: 0.85rem; padding: 0.6rem 0; font-weight: 600; margin-top: 0.5rem; width: 100%;">💾 Save FTP</button>
            </div>

            <!-- Card 1b: Max HR Configuration -->
            <div class="card" style="display: flex; flex-direction: column; gap: 1rem;">
                <h4 style="font-family: 'Outfit'; font-weight: 700; font-size: 1rem; color: #ffffff; margin: 0;">Maximum Heart Rate (Max HR)</h4>
                <p style="color: var(--text-secondary); font-size: 0.8rem; margin: 0; line-height: 1.4;">Configure your maximum heart rate (Max HR) in bpm. This value is used to dynamically construct your cardiovascular heart rate zones.</p>
                <div style="display: flex; flex-direction: column; gap: 0.4rem; margin-top: 0.5rem;">
                    <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase;">Athlete Max HR (bpm)</label>
                    <input type="number" id="settings-max-hr-input" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none;" value="190">
                </div>
                <button onclick="saveMaxHRFromSettings()" class="landing-btn landing-btn-primary" style="justify-content: center; font-size: 0.85rem; padding: 0.6rem 0; font-weight: 600; margin-top: 0.5rem; width: 100%;">💾 Save Max HR</button>
            </div>

            <!-- Card 2: Gemini API Key -->
            <div class="card" style="display: flex; flex-direction: column; gap: 1rem;">
                <h4 style="font-family: 'Outfit'; font-weight: 700; font-size: 1rem; color: #ffffff; margin: 0;">Gemini API Credentials</h4>
                <p style="color: var(--text-secondary); font-size: 0.8rem; margin: 0; line-height: 1.4;">Enter your Google Gemini API Key. This enables advanced AI coaching analysis, ride feedback, and dynamic training plan generation.</p>
                <div style="display: flex; flex-direction: column; gap: 0.4rem; margin-top: 0.5rem;">
                    <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase;">Gemini API Key</label>
                    <input type="password" id="settings-api-key-input" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none;" placeholder="Paste API Key here">
                </div>
                <div style="display: flex; gap: 0.5rem; margin-top: 0.5rem; width: 100%;">
                    <button onclick="saveAPIKeyFromSettings()" class="landing-btn landing-btn-primary" style="flex: 1; justify-content: center; font-size: 0.85rem; padding: 0.6rem 0; font-weight: 600;">💾 Save Key</button>
                    <button onclick="clearAPIKeyFromSettings()" class="btn-action" style="flex: 1; justify-content: center; font-size: 0.85rem; padding: 0.6rem 0;">Clear Key</button>
                </div>
            </div>

            <!-- Card 3: Default Bike/Gears Selection -->
            <div class="card" style="display: flex; flex-direction: column; gap: 1rem;">
                <h4 style="font-family: 'Outfit'; font-weight: 700; font-size: 1rem; color: #ffffff; margin: 0;">Drivetrain & Bike Selector</h4>
                <p style="color: var(--text-secondary); font-size: 0.8rem; margin: 0; line-height: 1.4;">Select your active bike configuration to analyze gear ratios and map mechanical drivetrain efficiency telemetry.</p>
                <div style="display: flex; flex-direction: column; gap: 0.4rem; margin-top: 0.5rem;">
                    <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase;">Active Drivetrain</label>
                    <select id="settings-bike-selector" class="btn-action" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none; width: 100%; box-sizing: border-box;" onchange="saveBikeFromSettings(this.value)">
                        <option value="">⚙️ Default Gears</option>
                    </select>
                </div>
            </div>

            <!-- Card 4: Intervals.icu Account Connection -->
            <div class="card" style="display: flex; flex-direction: column; gap: 1rem;">
                <h4 style="font-family: 'Outfit'; font-weight: 700; font-size: 1rem; color: #ffffff; margin: 0;">Intervals.icu Connection</h4>
                <p style="color: var(--text-secondary); font-size: 0.8rem; margin: 0; line-height: 1.4;">Sync workouts and training plans directly to your Intervals.icu calendar. Configure athlete credentials below.</p>
                <div style="display: flex; flex-direction: column; gap: 0.75rem; margin-top: 0.25rem;">
                    <div style="display: flex; flex-direction: column; gap: 0.3rem;">
                        <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase;">Athlete ID</label>
                        <input type="text" id="settings-intervals-athlete-id" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.5rem; font-family: inherit; font-size: 0.8rem; outline: none;" placeholder="Athlete ID (or 0)" value="0">
                    </div>
                    <div style="display: flex; flex-direction: column; gap: 0.3rem;">
                        <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase;">API Key</label>
                        <input type="password" id="settings-intervals-api-key" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.5rem; font-family: inherit; font-size: 0.8rem; outline: none;" placeholder="Paste API Key">
                    </div>
                    <div style="display: flex; align-items: center; gap: 0.5rem; margin: 0.1rem 0;">
                        <input type="checkbox" id="settings-intervals-enabled" style="cursor: pointer; width: 15px; height: 15px; accent-color: var(--accent);">
                        <label for="settings-intervals-enabled" style="font-size: 0.8rem; color: #ffffff; cursor: pointer; font-weight: 500;">Enable Calendar Export</label>
                    </div>
                </div>
                <div id="settings-intervals-test-status" style="display: none; padding: 0.5rem; border-radius: 6px; font-size: 0.75rem; font-weight: 500;"></div>
                <div style="display: flex; gap: 0.5rem; margin-top: 0.25rem; width: 100%;">
                    <button onclick="testIntervalsFromSettings()" id="btn-settings-intervals-test" class="btn-action" style="flex: 1; justify-content: center; font-size: 0.8rem; padding: 0.5rem 0;">🔍 Test Link</button>
                    <button onclick="saveIntervalsFromSettings()" class="landing-btn landing-btn-primary" style="flex: 1; justify-content: center; font-size: 0.8rem; padding: 0.5rem 0;">💾 Save Link</button>
                </div>
            </div>

            <!-- Card 5: Device Imports / Accounts Connection -->
            <div class="card" style="display: flex; flex-direction: column; gap: 1rem;">
                <h4 style="font-family: 'Outfit'; font-weight: 700; font-size: 1rem; color: #ffffff; margin: 0;">Device Linking & Imports</h4>
                <p style="color: var(--text-secondary); font-size: 0.8rem; margin: 0; line-height: 1.4;">Link your Hammerhead account or local cycling computer devices to download raw ride activity files instantly.</p>
                <div style="display: flex; flex-direction: column; gap: 0.75rem; margin-top: 0.5rem; width: 100%;">
                    <button class="landing-btn landing-btn-primary" onclick="triggerDeviceLinking()" style="justify-content: center; font-size: 0.85rem; padding: 0.6rem 0; font-weight: 600; width: 100%;">🔗 Link Hammerhead Account</button>
                </div>
            </div>
        </div>
    </div>

    <!-- Data & Backup View Container -->
    <div id="data-view" style="display: none; padding: 2rem; max-width: 1400px; margin: 0 auto; min-height: 100vh; box-sizing: border-box; font-family: var(--font-family); color: var(--text-primary); width: 100%;">
        <div class="toolbar" style="display: flex; align-items: center; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 12px; padding: 0.75rem 1rem; margin-bottom: 1.5rem; width: 100%; box-sizing: border-box;">
            <div style="font-family: 'Outfit'; font-weight: 700; font-size: 1.1rem; color: #ffffff;">📦 Database & Local Cache Options</div>
        </div>

        <div style="display: grid; grid-template-columns: 1fr; gap: 1.5rem; align-items: start; width: 100%;">
            <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 1.5rem; width: 100%;">
                <!-- Backup and Recovery Card -->
                <div class="card" style="display: flex; flex-direction: column; gap: 1rem;">
                    <h4 style="font-family: 'Outfit'; font-weight: 700; font-size: 1rem; color: #ffffff; margin: 0;">Database Backup & Migration</h4>
                    <p style="color: var(--text-secondary); font-size: 0.8rem; margin: 0; line-height: 1.4;">Export all stored telemetry files, custom chart layouts, training history, and application configuration to a JSON backup file, or restore from a previous backup.</p>
                    <div style="display: flex; gap: 0.75rem; margin-top: 0.75rem; width: 100%;">
                        <button onclick="window.exportAllLocalStorage()" class="landing-btn landing-btn-primary" style="flex: 1; justify-content: center; font-size: 0.85rem; padding: 0.6rem 0; font-weight: 600;">📤 Export Backup</button>
                        <button onclick="window.triggerImportBackup()" class="btn-action" style="flex: 1; justify-content: center; font-size: 0.85rem; padding: 0.6rem 0; font-weight: 600;">📥 Import Backup</button>
                    </div>
                </div>

                <!-- Database Wipe Card -->
                <div class="card" style="display: flex; flex-direction: column; gap: 1rem;">
                    <h4 style="font-family: 'Outfit'; font-weight: 700; font-size: 1rem; color: #ffffff; margin: 0;">Cache Management</h4>
                    <p style="color: var(--text-secondary); font-size: 0.8rem; margin: 0; line-height: 1.4;">Wipe all stored ride history and API settings from this browser origin. This action is irreversible.</p>
                    <button id="data-clear-all-btn" class="landing-btn" style="border-color: rgba(231, 76, 60, 0.5); color: #fc8181; font-weight: 600; justify-content: center; margin-top: 1rem; font-size: 0.85rem; padding: 0.6rem 0; width: 100%;">⚠️ Wipe All Local Cache</button>
                </div>
            </div>

            <!-- Active Ride Telemetry JSON Card -->
            <div class="card" style="display: flex; flex-direction: column; gap: 1rem; width: 100%; box-sizing: border-box;">
                <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 0.75rem; flex-wrap: wrap; gap: 0.5rem;">
                    <h4 style="font-family: 'Outfit'; font-weight: 700; font-size: 1rem; color: #ffffff; margin: 0;">Active Ride Telemetry (JSON Preview)</h4>
                    <div style="display: flex; gap: 0.5rem; flex-wrap: wrap;">
                        <button id="data-copy-json-btn" class="btn-action" style="font-size: 0.75rem; padding: 0.3rem 0.6rem;">📋 Copy JSON</button>
                        <button id="data-download-json-btn" class="btn-action" style="font-size: 0.75rem; padding: 0.3rem 0.6rem;">📥 Download JSON File</button>
                        <button id="data-view-schema-btn" class="btn-action" style="font-size: 0.75rem; padding: 0.3rem 0.6rem;">📋 View Schema</button>
                    </div>
                </div>
                <textarea id="data-json-preview" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #a0aec0; padding: 0.75rem; font-family: monospace; font-size: 0.75rem; min-height: 300px; max-height: 450px; resize: vertical; outline: none; width: 100%; box-sizing: border-box;" readonly></textarea>
            </div>
        </div>
    </div>

    <!-- Modal for Intervals.icu Configuration -->
    <div id="intervals-config-modal" style="display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.7); backdrop-filter: blur(8px); z-index: 9999; justify-content: center; align-items: center; padding: 2rem;">
        <div style="width: 100%; max-width: 480px; display: flex; flex-direction: column; gap: 1.25rem; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 20px; padding: 1.75rem; position: relative; box-shadow: 0 10px 25px rgba(0,0,0,0.5), 0 0 15px var(--accent-glow);">
            <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 1rem;">
                <div style="font-size: 1.3rem; font-weight: 700; background: linear-gradient(135deg, #ffffff, var(--accent)); -webkit-background-clip: text; -webkit-text-fill-color: transparent; font-family: 'Outfit';">🌐 Connect Intervals.icu</div>
                <button onclick="hideIntervalsConfigModal()" class="btn-action" style="padding: 0.25rem 0.5rem; font-size: 0.85rem;">Close</button>
            </div>
            
            <p style="color: var(--text-secondary); font-size: 0.85rem; line-height: 1.4; margin: 0;">
                Export your structured training plan workouts directly to Intervals.icu. Enter your Athlete ID (or leave as 0 for main athlete) and API Key from developer settings.
            </p>

            <div style="display: flex; flex-direction: column; gap: 1rem; margin-top: 0.25rem;">
                <div style="display: flex; flex-direction: column; gap: 0.4rem;">
                    <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Athlete ID</label>
                    <input type="text" id="intervals-athlete-id" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none; transition: border-color 0.2s;" placeholder="e.g. i123456 or 0" value="0">
                </div>
                
                <div style="display: flex; flex-direction: column; gap: 0.4rem;">
                    <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">API Key</label>
                    <input type="password" id="intervals-api-key" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none; transition: border-color 0.2s;" placeholder="Paste Intervals.icu API Key">
                </div>

                <div style="display: flex; align-items: center; gap: 0.5rem; margin: 0.25rem 0;">
                    <input type="checkbox" id="intervals-enabled" style="cursor: pointer; width: 16px; height: 16px; accent-color: var(--accent);">
                    <label for="intervals-enabled" style="font-size: 0.85rem; color: #ffffff; cursor: pointer; font-weight: 500;">Enable Intervals.icu Export</label>
                </div>
            </div>

            <!-- Connection Status -->
            <div id="intervals-test-status" style="display: none; padding: 0.6rem 0.75rem; border-radius: 8px; font-size: 0.8rem; line-height: 1.4; font-weight: 500;">
                <!-- Filled dynamically -->
            </div>

            <div style="display: flex; gap: 0.75rem; width: 100%; margin-top: 0.5rem;">
                <button onclick="testIntervalsConnection()" id="btn-intervals-test" class="landing-btn" style="flex: 1; justify-content: center; font-size: 0.85rem; padding: 0.6rem 0;">
                    🔍 Test Connection
                </button>
                <button onclick="saveIntervalsConfig()" id="btn-intervals-save" class="landing-btn landing-btn-primary" style="flex: 1; justify-content: center; font-size: 0.85rem; padding: 0.6rem 0;">
                    💾 Save Settings
                </button>
            </div>
        </div>
    </div>

    <!-- Modal for Workout Route Planner & Scheduler -->
    <div id="route-planner-modal" style="display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.7); backdrop-filter: blur(8px); z-index: 9999; justify-content: center; align-items: center; padding: 2rem;">
        <div style="width: 100%; max-width: 900px; display: flex; flex-direction: column; gap: 1.25rem; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 20px; padding: 1.75rem; position: relative; box-shadow: 0 10px 25px rgba(0,0,0,0.5), 0 0 15px var(--accent-glow); max-height: 90vh; overflow-y: auto;">
            <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 1rem;">
                <div id="route-planner-title" style="font-size: 1.3rem; font-weight: 700; background: linear-gradient(135deg, #ffffff, var(--accent)); -webkit-background-clip: text; -webkit-text-fill-color: transparent; font-family: 'Outfit';">🗺️ Workout Route Planner & Scheduler</div>
                <button onclick="hideRoutePlannerModal()" class="btn-action" style="padding: 0.25rem 0.5rem; font-size: 0.85rem;">Close</button>
            </div>
            
            <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 1.5rem; align-items: start;">
                <!-- Left Column: Controls -->
                <div style="display: flex; flex-direction: column; gap: 1rem;">
                    <div style="display: flex; flex-direction: column; gap: 0.4rem;">
                        <div style="display: flex; justify-content: space-between; align-items: center;">
                            <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Start Location</label>
                            <label style="font-size: 0.7rem; color: var(--accent); cursor: pointer; display: inline-flex; align-items: center; gap: 3px;">
                                <input type="radio" name="map-click-target" value="start" checked style="accent-color: var(--accent); margin: 0; cursor: pointer;"> Click map to set
                            </label>
                        </div>
                        <input type="text" id="route-start-location" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none; transition: border-color 0.2s;" placeholder="Type address or click on the map">
                    </div>
                    
                    <div style="display: flex; flex-direction: column; gap: 0.4rem;">
                        <div style="display: flex; justify-content: space-between; align-items: center;">
                            <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">End Location (Optional)</label>
                            <label style="font-size: 0.7rem; color: var(--accent); cursor: pointer; display: inline-flex; align-items: center; gap: 3px;">
                                <input type="radio" name="map-click-target" value="end" style="accent-color: var(--accent); margin: 0; cursor: pointer;"> Click map to set
                            </label>
                        </div>
                        <input type="text" id="route-end-location" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none; transition: border-color 0.2s;" placeholder="Type address or click on the map to set end">
                    </div>
                    
                    <div style="display: flex; flex-direction: column; gap: 0.4rem;">
                        <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Direction Bias ("Towards") (Loop Only)</label>
                        <input type="text" id="route-towards" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none;" placeholder="e.g., Marin, Oakland, Pacifica (optional)">
                    </div>

                    <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 0.75rem;">
                        <div style="display: flex; flex-direction: column; gap: 0.4rem;">
                            <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Avg Speed (km/h)</label>
                            <input type="number" id="route-avg-speed" step="0.1" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none;" oninput="updateTargetDistance()">
                        </div>
                        <div style="display: flex; flex-direction: column; gap: 0.4rem;">
                            <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Target Dist (km)</label>
                            <input type="number" id="route-target-dist" step="0.1" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none;">
                        </div>
                    </div>

                    <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 0.75rem;">
                        <div style="display: flex; flex-direction: column; gap: 0.4rem;">
                            <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Start Time</label>
                            <input type="time" id="route-start-time" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none;" oninput="updateFinishTime()">
                        </div>
                        <div style="display: flex; flex-direction: column; gap: 0.4rem;">
                            <label style="font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Finish Time</label>
                            <input type="time" id="route-finish-time" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; outline: none;">
                        </div>
                    </div>

                    <div id="route-planner-status" style="display: none; padding: 0.6rem 0.75rem; border-radius: 8px; font-size: 0.8rem; line-height: 1.4; font-weight: 500;">
                    </div>

                    <div style="display: flex; flex-direction: column; gap: 0.5rem;">
                        <button onclick="suggestRouteWithGemini()" id="btn-suggest-route" class="landing-btn" style="justify-content: center; font-size: 0.85rem; padding: 0.65rem 0; width: 100%; background: linear-gradient(135deg, rgba(155, 89, 182, 0.2), rgba(52, 152, 219, 0.2)); border-color: #9b59b6; color: #e0aaff; font-weight: 600; display: flex; align-items: center; gap: 0.4rem;">
                            🤖 Suggest Route with Gemini AI
                        </button>
                    </div>

                    <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 0.75rem; margin-top: 0.5rem;">
                        <button onclick="exportRouteGPX()" id="btn-export-route" class="landing-btn" style="justify-content: center; font-size: 0.85rem; padding: 0.65rem 0; width: 100%; border-color: #2ecc71; color: #2ecc71;">
                            ⬇️ Export GPX
                        </button>
                        <button onclick="syncRouteToHammerhead()" id="btn-route-sync" class="landing-btn" style="justify-content: center; font-size: 0.85rem; padding: 0.65rem 0; width: 100%; border-color: #3498db; color: #3498db;">
                            🔄 Sync to Karoo
                        </button>
                    </div>

                    <button onclick="saveRouteSchedule()" id="btn-route-save" class="landing-btn landing-btn-primary" style="justify-content: center; font-size: 0.85rem; padding: 0.65rem 0; margin-top: 0.5rem; width: 100%;" disabled>
                        🗓️ Save Schedule & Route
                    </button>
                </div>

                <!-- Right Column: Leaflet Map -->
                <div style="display: flex; flex-direction: column; gap: 0.75rem;">
                    <div id="route-map" style="width: 100%; height: 350px; background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 12px; position: relative; z-index: 1;">
                    </div>
                    <div id="route-summary-info" style="font-size: 0.85rem; color: var(--text-secondary); line-height: 1.4;">
                        No route generated yet.
                    </div>
                </div>
            </div>
        </div>
    </div>

    <!-- Modal for Select Ride -->
    <div id="select-ride-modal" style="display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.7); backdrop-filter: blur(8px); z-index: 9999; justify-content: center; align-items: center; padding: 2rem;">
        <div style="width: 100%; max-width: 700px; height: 75%; display: flex; flex-direction: column; gap: 1rem; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 20px; padding: 1.5rem; position: relative; box-shadow: 0 10px 25px rgba(0,0,0,0.5), 0 0 15px var(--accent-glow);">
            <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 1rem;">
                <div style="font-size: 1.3rem; font-weight: 700; background: linear-gradient(135deg, #ffffff, var(--accent)); -webkit-background-clip: text; -webkit-text-fill-color: transparent; font-family: 'Outfit';">📂 Select Ride Activity</div>
                <button id="select-ride-close-btn" class="btn-action">Close</button>
            </div>
            
            <!-- Modal Tabs Header -->
            <div style="display: flex; border-bottom: 1px solid var(--border-color); margin-bottom: 0.5rem;">
                <button id="tab-local" class="btn-action" style="border-radius: 0; border: none; border-bottom: 2px solid var(--accent); background: none; font-size: 0.95rem; font-weight: 600; padding: 0.75rem 1.5rem; color: var(--accent); cursor: pointer;">📂 Local Files</button>
                <button id="tab-hammerhead" class="btn-action" style="border-radius: 0; border: none; border-bottom: 2px solid transparent; background: none; font-size: 0.95rem; font-weight: 500; padding: 0.75rem 1.5rem; color: var(--text-secondary); cursor: pointer;">🚲 Hammerhead Karoo</button>
                <button id="tab-wahoo" class="btn-action" style="border-radius: 0; border: none; border-bottom: 2px solid transparent; background: none; font-size: 0.95rem; font-weight: 500; padding: 0.75rem 1.5rem; color: var(--text-secondary); cursor: pointer;">⚡ Wahoo Fitness</button>
                <button id="tab-intervals" class="btn-action" style="border-radius: 0; border: none; border-bottom: 2px solid transparent; background: none; font-size: 0.95rem; font-weight: 500; padding: 0.75rem 1.5rem; color: var(--text-secondary); cursor: pointer;">🌐 Intervals.icu</button>
            </div>

            <!-- Modal Content Lists Container -->
            <div style="flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 0.75rem; padding-right: 0.25rem;">
                
                <!-- Loading State -->
                <div id="select-ride-loading" style="display: none; flex-direction: column; justify-content: center; align-items: center; gap: 1rem; padding: 3rem 0;">
                    <div style="width: 40px; height: 40px; border: 4px solid var(--border-color); border-top: 4px solid var(--accent); border-radius: 50%; animation: spin 1s linear infinite;"></div>
                    <div style="color: var(--text-secondary); font-size: 0.9rem;">Fetching ride list...</div>
                </div>

                <!-- Empty State -->
                <div id="select-ride-empty" style="display: none; text-align: center; color: var(--text-secondary); padding: 3rem 0; font-style: italic;">
                    No activities found.
                </div>

                <!-- Local Files List -->
                <div id="list-local-container" style="display: flex; flex-direction: column; gap: 0.75rem;">
                    <!-- Filled dynamically -->
                </div>

                <!-- Hammerhead Activities List -->
                <div id="list-hammerhead-container" style="display: none; flex-direction: column; gap: 0.75rem;">
                    <!-- Filled dynamically -->
                </div>

                <!-- Wahoo Activities List -->
                <div id="list-wahoo-container" style="display: none; flex-direction: column; gap: 0.75rem;">
                    <!-- Filled dynamically -->
                </div>

                <!-- Intervals.icu Activities List -->
                <div id="list-intervals-container" style="display: none; flex-direction: column; gap: 0.75rem;">
                    <!-- Filled dynamically -->
                </div>

            </div>
        </div>
    </div>

    <!-- Global Loading Overlay for Ride Analysis -->
    <div id="analysis-loading-overlay" style="display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(10, 10, 12, 0.85); backdrop-filter: blur(10px); z-index: 10000; justify-content: center; align-items: center; flex-direction: column; gap: 1.5rem;">
        <div style="width: 60px; height: 60px; border: 5px solid var(--border-color); border-top: 5px solid var(--accent); border-radius: 50%; animation: spin 1s linear infinite; box-shadow: 0 0 15px var(--accent-glow);"></div>
        <div style="text-align: center;">
            <h3 style="font-family: 'Outfit'; font-weight: 700; color: #ffffff; margin-bottom: 0.5rem; font-size: 1.4rem;">Analyzing Ride Telemetry</h3>
            <p style="color: var(--text-secondary); font-size: 0.95rem;">Parsing FIT file records and compiling gear calculations...</p>
        </div>
    </div>

    <!-- Modal for JSON view -->
    <div id="json-modal" style="display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.7); backdrop-filter: blur(8px); z-index: 9999; justify-content: center; align-items: center; padding: 2rem;">
        <div style="width: 100%; max-width: 800px; height: 80%; display: flex; flex-direction: column; gap: 1rem; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 20px; padding: 1.5rem; position: relative; box-shadow: 0 10px 25px rgba(0,0,0,0.5);">
            <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 1rem;">
                <div style="font-size: 1.3rem; font-weight: 700; background: linear-gradient(135deg, #ffffff, var(--accent)); -webkit-background-clip: text; -webkit-text-fill-color: transparent;">Coaching JSON Data</div>
                <div style="display: flex; gap: 0.75rem;">
                    <button id="modal-download-btn" class="btn-action" style="background: var(--accent-glow); border-color: var(--accent); color: var(--accent);">📥 Download JSON File</button>
                    <button id="modal-copy-btn" class="btn-action">📋 Copy Entire JSON</button>
                    <button id="modal-close-btn" class="btn-action">Close</button>
                </div>
            </div>
            <div style="font-size: 0.85rem; color: #a0aec0; padding: 0.5rem 0; border-bottom: 1px solid rgba(255,255,255,0.05);">
                ⚠️ <strong>Note:</strong> The full telemetry payload is large (~3.5 MB). We recommend downloading the file directly to upload into your Gemini coaching session. Copying the full text to the clipboard is supported, but pasting a massive payload might lag or crash some browser tabs.
            </div>
            <textarea id="json-textarea" readonly style="flex: 1; width: 100%; background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 12px; color: #e2e8f0; font-family: monospace; font-size: 0.85rem; padding: 1rem; resize: none; outline: none; line-height: 1.4;"></textarea>
        </div>
    </div>

    <!-- Modal for Schema view -->
    <div id="schema-modal" style="display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.7); backdrop-filter: blur(8px); z-index: 9999; justify-content: center; align-items: center; padding: 2rem;">
        <div style="width: 100%; max-width: 800px; height: 80%; display: flex; flex-direction: column; gap: 1rem; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 20px; padding: 1.5rem; position: relative; box-shadow: 0 10px 25px rgba(0,0,0,0.5);">
            <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 1rem;">
                <div style="font-size: 1.3rem; font-weight: 700; background: linear-gradient(135deg, #ffffff, var(--accent)); -webkit-background-clip: text; -webkit-text-fill-color: transparent;">JSON Schema Definition</div>
                <div style="display: flex; gap: 0.75rem;">
                    <button id="schema-download-btn" class="btn-action" style="background: var(--accent-glow); border-color: var(--accent); color: var(--accent);">📥 Download Schema</button>
                    <button id="schema-copy-btn" class="btn-action">📋 Copy Schema</button>
                    <button id="schema-close-btn" class="btn-action">Close</button>
                </div>
            </div>
            <textarea id="schema-textarea" readonly style="flex: 1; width: 100%; background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 12px; color: #e2e8f0; font-family: monospace; font-size: 0.85rem; padding: 1rem; resize: none; outline: none; line-height: 1.4;"></textarea>
        </div>
    </div>

    <!-- Modal for Show Saved Data -->
    <div id="saved-data-modal" style="display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.7); backdrop-filter: blur(8px); z-index: 9999; justify-content: center; align-items: center; padding: 2rem;">
        <div style="width: 100%; max-width: 800px; height: 85%; display: flex; flex-direction: column; gap: 1rem; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 20px; padding: 1.5rem; position: relative; box-shadow: 0 10px 30px rgba(0,0,0,0.5);">
            <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 1rem;">
                <div style="font-size: 1.4rem; font-weight: 700; color: #ffffff; display: flex; align-items: center; gap: 0.5rem; font-family: 'Outfit';">
                    <span>📦 Browser Local Storage Cache</span>
                </div>
                <div style="display: flex; gap: 0.75rem; align-items: center;">
                    <button id="saved-data-export-btn" class="btn-action" style="background: var(--accent-glow); border-color: var(--accent); color: var(--accent); font-weight: 600;">📤 Export Data</button>
                    <button id="saved-data-import-btn" class="btn-action" style="font-weight: 600;">📥 Import Data</button>
                    <input type="file" id="saved-data-import-file" style="display: none;" accept=".json" />
                    <button id="saved-data-close-btn" class="btn-action">Close</button>
                </div>
            </div>
            
            <div id="saved-data-content" style="flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: 1.5rem; padding-right: 0.5rem;">
                <!-- Content generated dynamically -->
            </div>
            
            <div style="display: flex; justify-content: space-between; align-items: center; border-top: 1px solid var(--border-color); padding-top: 1rem; margin-top: 0.5rem;">
                <span style="font-size: 0.8rem; color: var(--text-secondary);">Your data is stored completely client-side in this browser.</span>
                <button id="saved-data-clear-all-btn" class="btn-action" style="border-color: rgba(231, 76, 60, 0.5); color: #fc8181; font-weight: 600;">⚠️ Clear All Browser Data</button>
            </div>
        </div>
    </div>

    <!-- Modal for Gemini Coach -->
    <div id="coach-modal" style="display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.7); backdrop-filter: blur(8px); z-index: 9999; justify-content: center; align-items: center; padding: 2rem;">
        <div style="width: 100%; max-width: 850px; height: 85%; display: flex; flex-direction: column; gap: 1rem; background: var(--bg-secondary); border: 1px solid rgba(155, 89, 182, 0.4); border-radius: 20px; padding: 1.5rem; position: relative; box-shadow: 0 10px 30px rgba(155, 89, 182, 0.25);">
            <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 1rem;">
                <div style="font-size: 1.4rem; font-weight: 700; background: linear-gradient(135deg, #e0aaff, #3498db); -webkit-background-clip: text; -webkit-text-fill-color: transparent; display: flex; align-items: center; gap: 0.5rem; font-family: 'Outfit';">
                    <span>🤖 directeurAI [dsAI] Cycling Coach</span>
                </div>
                <div style="display: flex; gap: 0.75rem; align-items: center;">
                    <select id="coach-model-select" class="btn-action" style="font-size: 0.85rem; padding: 0.25rem 0.5rem; background: var(--bg-tertiary); border: 1px solid var(--border-color); color: #ffffff; border-radius: 6px; outline: none;">
                        <option value="gemini-3.5-flash" selected>Gemini 3.5 Flash (Default)</option>
                        <option value="gemini-3.5-pro">Gemini 3.5 Pro</option>
                        <option value="gemini-3.1-pro">Gemini 3.1 Pro</option>
                        <option value="gemini-2.5-pro">Gemini 2.5 Pro</option>
                        <option value="gemini-2.5-flash">Gemini 2.5 Flash</option>
                        <option value="gemini-1.5-pro">Gemini 1.5 Pro (Legacy)</option>
                    </select>
                    <button id="coach-clear-key-btn" class="btn-action" style="font-size: 0.8rem; display: none;">Clear Key</button>
                    <button id="coach-close-btn" class="btn-action">Close</button>
                </div>
            </div>

            <!-- API Key Input Panel -->
            <div id="coach-key-panel" style="display: flex; flex-direction: column; gap: 1.25rem; justify-content: center; align-items: center; flex: 1; padding: 2rem; text-align: center;">
                <div style="font-size: 2.5rem; animation: pulse 2s infinite;">🔑</div>
                <div>
                    <h3 style="color: #ffffff; margin-bottom: 0.5rem; font-family: 'Outfit'; font-weight: 600;">Set Your Gemini API Key</h3>
                    <p style="color: #a0aec0; max-width: 500px; font-size: 0.9rem; line-height: 1.5;">
                        To generate coach reports directly, provide a Gemini API Key. Your key is stored strictly on your local machine (in browser <code>localStorage</code>) and is sent directly to Google's API endpoint.
                    </p>
                </div>
                <div style="width: 100%; max-width: 450px; display: flex; gap: 0.5rem;">
                    <input id="coach-key-input" type="password" placeholder="AIzaSy..." style="flex: 1; background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.75rem; outline: none; font-family: monospace;" />
                    <button id="coach-save-key-btn" class="btn-action" style="background: var(--accent); color: #000000; border-color: var(--accent); font-weight: 600;">Save Key</button>
                </div>
                <div style="font-size: 0.85rem; color: var(--text-secondary);">
                    Don't have a key? <a href="https://aistudio.google.com/" target="_blank" style="color: var(--accent); text-decoration: underline;">Get a free API Key from Google AI Studio</a>
                </div>
            </div>

            <!-- Coaching Analysis Panel -->
            <div id="coach-analysis-panel" style="display: none; flex-direction: column; flex: 1; min-height: 0;">
                <!-- Setup/Generate view -->
                <div id="coach-generate-view" style="display: flex; flex-direction: row; gap: 2rem; flex: 1; padding: 1rem; min-height: 0; align-items: stretch; justify-content: space-between;">
                    <!-- Left side: Status & Generate action -->
                    <div style="flex: 1; display: flex; flex-direction: column; justify-content: center; align-items: center; text-align: center; gap: 1.25rem; background: rgba(255,255,255,0.02); border: 1px solid var(--border-color); border-radius: 16px; padding: 1.5rem;">
                        <div style="font-size: 2.8rem; animation: pulse 2s infinite;">🚴‍♂️</div>
                        <div>
                            <h3 style="color: #ffffff; margin-bottom: 0.5rem; font-family: 'Outfit'; font-weight: 600;">Ready for Analysis</h3>
                            <p style="color: #a0aec0; max-width: 320px; font-size: 0.85rem; line-height: 1.5; margin: 0 auto;">
                                Your ride telemetry, power profile, shifting stats, and climb details are ready. Click below to query Gemini and generate a personalized coaching report.
                            </p>
                        </div>
                        <button id="coach-run-btn" class="btn-action" style="background: linear-gradient(135deg, #9b59b6, #3498db); border-color: #9b59b6; color: #ffffff; font-weight: 600; padding: 0.75rem 2rem; font-size: 1rem; box-shadow: 0 0 15px rgba(155, 89, 182, 0.4); width: 100%; max-width: 280px; cursor: pointer;">
                            🚀 Generate Coach Report
                        </button>
                        <div id="coach-cache-status" style="font-size: 0.8rem; color: #a0aec0; margin-top: 0.5rem; max-width: 280px; line-height: 1.4; display: none;"></div>
                    </div>

                    <!-- Right side: Plan and History context -->
                    <div style="flex: 1.2; display: flex; flex-direction: column; gap: 0.75rem; min-height: 0;">
                        <!-- Plan section -->
                        <div style="display: flex; flex-direction: column; gap: 0.5rem; background: rgba(255,255,255,0.02); border: 1px solid var(--border-color); border-radius: 16px; padding: 1rem;">
                            <div style="font-size: 0.9rem; font-weight: 600; color: #ffffff; font-family: 'Outfit'; display: flex; align-items: center; gap: 0.5rem;">
                                <span>📋 My Training Plan & Goals</span>
                            </div>
                            <textarea id="coach-plan-input" placeholder="Enter your training plan or goals here (e.g. 'Build FTP to 280W, keep HR under 160 bpm on climbs'). The AI Coach will evaluate this ride against your goals." style="width: 100%; height: 90px; background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; font-size: 0.82rem; padding: 0.6rem; resize: none; outline: none; line-height: 1.4; font-family: inherit;"></textarea>
                        </div>

                        <!-- Ride Notes section -->
                        <div style="display: flex; flex-direction: column; gap: 0.5rem; background: rgba(255,255,255,0.02); border: 1px solid var(--border-color); border-radius: 16px; padding: 1rem;">
                            <div style="font-size: 0.9rem; font-weight: 600; color: #ffffff; font-family: 'Outfit'; display: flex; align-items: center; gap: 0.5rem;">
                                <span>💬 Ride Notes</span>
                                <span id="coach-notes-saved-badge" style="display: none; font-size: 0.65rem; font-weight: 500; color: #2ecc71; background: rgba(46, 204, 113, 0.1); padding: 0.1rem 0.4rem; border-radius: 4px;">✓ Saved</span>
                            </div>
                            <textarea id="coach-ride-notes" placeholder="Add notes about this ride (e.g. 'Felt strong on the climbs, legs tired from yesterday's intervals, testing new saddle position'). These will be shared with the AI Coach for context." style="width: 100%; height: 70px; background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; font-size: 0.82rem; padding: 0.6rem; resize: none; outline: none; line-height: 1.4; font-family: inherit;"></textarea>
                        </div>

                        <!-- History section -->
                        <div style="flex: 1; display: flex; flex-direction: column; gap: 0.5rem; background: rgba(255,255,255,0.02); border: 1px solid var(--border-color); border-radius: 16px; padding: 1rem; min-height: 0;">
                            <div style="display: flex; justify-content: space-between; align-items: center;">
                                <div style="font-size: 0.9rem; font-weight: 600; color: #ffffff; font-family: 'Outfit'; display: flex; align-items: center; gap: 0.5rem;">
                                    <span>📅 Training Log History</span>
                                </div>
                                <button id="coach-clear-history-btn" class="btn-action" style="font-size: 0.7rem; padding: 0.15rem 0.4rem; border-color: rgba(231, 76, 60, 0.4); color: #fc8181; cursor: pointer; display: none;">Clear History</button>
                            </div>
                            <div id="coach-history-list" style="flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: 0.5rem; min-height: 0; font-size: 0.8rem; color: #a0aec0;">
                                <div style="font-style: italic; text-align: center; padding-top: 1rem; color: var(--text-secondary);">No previous ride analyses stored. Your first analysis will be saved here automatically.</div>
                            </div>
                        </div>
                    </div>
                </div>

                <!-- Loading view -->
                <div id="coach-loading-view" style="display: none; flex-direction: column; gap: 1.5rem; justify-content: center; align-items: center; flex: 1; padding: 2rem;">
                    <div style="width: 50px; height: 50px; border: 4px solid rgba(155, 89, 182, 0.1); border-top-color: #9b59b6; border-radius: 50%; animation: spin 1s linear infinite;"></div>
                    <div style="text-align: center; display: flex; flex-direction: column; align-items: center; width: 100%;">
                        <h3 style="color: #ffffff; margin-bottom: 1rem; font-family: 'Outfit'; font-weight: 600;">directeurAI is analyzing...</h3>
                        
                        <!-- Progress Bar -->
                        <div style="width: 100%; max-width: 400px; background: rgba(255,255,255,0.05); height: 8px; border-radius: 4px; overflow: hidden; border: 1px solid var(--border-color); margin-bottom: 1.5rem;">
                            <div id="coach-progress-bar" style="width: 0%; height: 100%; background: linear-gradient(90deg, #9b59b6, #3498db); transition: width 0.4s ease; box-shadow: 0 0 8px rgba(155, 89, 182, 0.5);"></div>
                        </div>

                        <!-- Step Items -->
                        <ul style="text-align: left; display: flex; flex-direction: column; gap: 0.6rem; width: 100%; max-width: 320px; font-size: 0.85rem; color: var(--text-secondary); padding: 0; list-style: none; margin: 0;">
                            <li id="step-downsample" style="display: flex; align-items: center; gap: 0.5rem; transition: color 0.3s;"><span class="step-icon">○</span> Downsampling telemetry data</li>
                            <li id="step-prompt" style="display: flex; align-items: center; gap: 0.5rem; transition: color 0.3s;"><span class="step-icon">○</span> Structuring coaching prompt</li>
                            <li id="step-api" style="display: flex; align-items: center; gap: 0.5rem; transition: color 0.3s;"><span class="step-icon">○</span> Contacting Google Gemini API</li>
                            <li id="step-analyze" style="display: flex; align-items: center; gap: 0.5rem; transition: color 0.3s;"><span class="step-icon">○</span> AI analyzing pacing & power curves</li>
                            <li id="step-render" style="display: flex; align-items: center; gap: 0.5rem; transition: color 0.3s;"><span class="step-icon">○</span> Formatting coach recommendations</li>
                        </ul>
                    </div>
                </div>

                <!-- Report view -->
                <div id="coach-report-view" style="display: none; flex-direction: column; flex: 1; min-height: 0; gap: 1rem;">
                    <div style="display: flex; justify-content: space-between; align-items: center; background: rgba(255,255,255,0.02); padding: 0.5rem 1rem; border-radius: 8px; border: 1px solid var(--border-color);">
                        <span style="font-size: 0.85rem; color: #a0aec0;">Report generated by <strong>directeurAI</strong> (<span id="coach-model-used" style="color: var(--accent);">Gemini 3.5 Flash</span>)</span>
                        <button id="coach-regenerate-btn" class="btn-action" style="font-size: 0.8rem; padding: 0.25rem 0.75rem;">🔄 Re-analyze</button>
                    </div>
                    <div id="coach-report-content" style="flex: 1; overflow-y: auto; background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 12px; padding: 1.5rem; color: #e2e8f0; font-size: 0.95rem; line-height: 1.6;">
                        <!-- Report HTML gets injected here -->
                    </div>
                    
                    <!-- Chat / Follow-up Input Panel -->
                    <div id="coach-chat-input-container" style="display: flex; gap: 0.75rem; background: rgba(255, 255, 255, 0.02); padding: 0.75rem; border-radius: 12px; border: 1px solid var(--border-color); align-items: center;">
                        <input id="coach-chat-input" type="text" placeholder="Ask follow-up questions to your coach (e.g. 'Why was my normalized cadence so high?', 'How do I reduce heart rate coupling?')..." style="flex: 1; background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.75rem; outline: none; font-size: 0.9rem;" />
                        <button id="coach-chat-send-btn" class="btn-action" style="background: linear-gradient(135deg, #9b59b6, #3498db); border-color: #9b59b6; color: #ffffff; font-weight: 600; padding: 0.75rem 1.5rem; border-radius: 8px; cursor: pointer; display: flex; align-items: center; gap: 0.5rem; height: 100%;">
                            <span>Send</span>
                            <span>➔</span>
                        </button>
                    </div>
                </div>
            </div>
        </div>
    </div>

    <!-- Data Injection & Logic -->
    <script id="embedded-ride-data" type="application/json">{{.JSONStr}}</script>
    <script id="embedded-schema-data" type="application/json">{{.SchemaStr}}</script>
    <script id="embedded-bikes-data" type="application/json">{{.BikesStr}}</script>
    <script>
        let initialRideData = null;
        let schemaData = null;
        let configBikes = null;

        const clientStorage = {
            cache: {},
            async init() {
                try {
                    const res = await fetch('/api/storage');
                    if (res.ok) {
                        this.cache = await res.json();
                    }
                    
                    // Migration from local browser storage
                    let migrated = false;
                    for (let i = 0; i < localStorage.length; i++) {
                        const key = localStorage.key(i);
                        if (key && (key.startsWith('fit_') || key.startsWith('directeur_') || key === 'gemini_api_key')) {
                            if (this.cache[key] === undefined) {
                                this.cache[key] = localStorage.getItem(key);
                                migrated = true;
                            }
                        }
                    }
                    if (migrated) {
                        await this.sync();
                        // Clear localStorage once migrated
                        for (let key in this.cache) {
                            localStorage.removeItem(key);
                        }
                    }
                } catch (e) {
                    console.error("Storage initialization failed", e);
                }
            },
            getItem(key) {
                return this.cache.hasOwnProperty(key) ? this.cache[key] : null;
            },
            setItem(key, value) {
                this.cache[key] = String(value);
                this.sync(); // Async save
            },
            removeItem(key) {
                delete this.cache[key];
                this.sync();
            },
            clear() {
                this.cache = {};
                this.sync();
            },
            async sync() {
                try {
                    await fetch('/api/storage', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(this.cache)
                    });
                } catch (e) {
                    console.error("Failed to sync storage to server", e);
                }
            }
        };

        try {
            const rd = document.getElementById('embedded-ride-data').textContent;
            if (rd && rd.trim() && rd.trim() !== "null") initialRideData = JSON.parse(rd);
        } catch(e) {}
        try {
            const sd = document.getElementById('embedded-schema-data').textContent;
            if (sd && sd.trim() && sd.trim() !== "null") schemaData = JSON.parse(sd);
        } catch(e) {}
        try {
            const bd = document.getElementById('embedded-bikes-data').textContent;
            if (bd && bd.trim() && bd.trim() !== "null") configBikes = JSON.parse(bd);
        } catch(e) {}

        window.initialRideData = initialRideData;
        let rideData = initialRideData;
        console.log("Loaded Ride Data:", rideData);

        const defaultFTP = {{.FTP}} || 250;
        let athleteFTP = parseInt(clientStorage.getItem('fit_athlete_ftp')) || defaultFTP;
        const defaultMaxHR = {{.MaxHR}} || 190;
        let athleteMaxHR = parseInt(clientStorage.getItem('fit_athlete_max_hr')) || defaultMaxHR;

        // Global Chart and Map references for dynamic updating
        let powerChart, speedAltChart, hrCadenceChart, altGearsChart, powerCurveChart, chartPZones, chartHZones, routePolyline, quadrantAnalysisChart;

        const parseLocalDate = (d) => {
            if (!d) return new Date();
            if (d instanceof Date) return new Date(d);
            const s = typeof d === 'string' ? d : d.toString();
            const parts = s.split('T')[0].split('-');
            if (parts.length === 3) {
                return new Date(parseInt(parts[0]), parseInt(parts[1]) - 1, parseInt(parts[2]));
            }
            return new Date(d);
        };
        window.parseLocalDate = parseLocalDate;
        const formatLocalDateKey = (d) => {
            const date = parseLocalDate(d);
            const year = date.getFullYear();
            const month = String(date.getMonth() + 1).padStart(2, '0');
            const day = String(date.getDate()).padStart(2, '0');
            return year + '-' + month + '-' + day;
        };
        window.formatLocalDateKey = formatLocalDateKey;

        const selfHealTrainingPlans = () => {
            try {
                const plansByDateData = clientStorage.getItem('fit_training_plans_by_date');
                if (plansByDateData) {
                    const plans = JSON.parse(plansByDateData);
                    const moves = [];
                    Object.keys(plans).forEach(key => {
                        const dayPlan = plans[key];
                        if (dayPlan && dayPlan.day) {
                            const dayStr = dayPlan.day;
                            if (dayStr.includes(',') && (dayStr.match(/\d{4}/) || dayStr.match(/[A-Za-z]{3}\s+\d+/))) {
                                try {
                                    const parsedDate = new Date(dayStr);
                                    if (!isNaN(parsedDate.getTime())) {
                                        const correctKey = formatLocalDateKey(parsedDate);
                                        if (correctKey !== key) {
                                            moves.push({ from: key, to: correctKey, value: dayPlan });
                                        }
                                    }
                                } catch(e) {}
                            }
                        }
                    });
                    if (moves.length > 0) {
                        moves.forEach(m => {
                            delete plans[m.from];
                            plans[m.to] = m.value;
                            console.log("Self-healed shifted key: " + m.from + " -> " + m.to + " (from \"" + m.value.day + "\")");
                        });
                        clientStorage.setItem('fit_training_plans_by_date', JSON.stringify(plans));
                    }
                }
            } catch(e) {
                console.error("Self-heal error:", e);
            }
        };
        window.selfHealTrainingPlans = selfHealTrainingPlans;

        const getMonday = (d) => {
            const date = parseLocalDate(d);
            const day = date.getDay(); // 0 is Sunday, 1 is Monday, ..., 6 is Saturday
            const diff = date.getDate() - day + (day === 0 ? -6 : 1);
            const monday = new Date(date.setDate(diff));
            monday.setHours(0, 0, 0, 0);
            return monday;
        };
        window.getMonday = getMonday;
        let leafletMap, startMarker, endMarker;
        let fullJSONString = "";
        const fullSchemaString = JSON.stringify(schemaData, null, 2);

        const initialRideSource = "{{.Source}}";
        const initialRideParam = "{{.Param}}";
        const initialRideParam2 = "{{.Param2}}";

        let currentRideSource = initialRideSource || 'local';
        let currentRideParam = initialRideParam || (rideData ? (rideData.source_file || '') : '');
        let currentRideParam2 = initialRideParam2 || '';

        const getRideQueryString = (source, param, param2) => {
            if (!param) return 'javascript:void(0)';
            let q = '?source=' + encodeURIComponent(source);
            if (source === 'local') {
                q += '&file=' + encodeURIComponent(param);
            } else {
                q += '&id=' + encodeURIComponent(param);
                if (param2) {
                    q += '&url=' + encodeURIComponent(param2);
                }
            }
            const currentUrl = new URL(window.location.href);
            const bike = currentUrl.searchParams.get('bike');
            if (bike) {
                q += '&bike=' + encodeURIComponent(bike);
            }
            return q;
        };

        window.getRideQueryString = getRideQueryString;

        window.addEventListener('popstate', (event) => {
            if (event.state && event.state.source && event.state.param) {
                loadRideData(event.state.source, event.state.param, event.state.param2 || '', false);
            } else {
                if (window.initialRideData) {
                    renderDashboard(window.initialRideData);
                    currentRideSource = initialRideSource || 'local';
                    currentRideParam = initialRideParam || (window.initialRideData.source_file || '');
                    currentRideParam2 = initialRideParam2 || '';
                }
            }
        });

        // Apply Theme based on Month of the ride or user selection
        let defaultThemeClass = 'theme-carbon';
        if (rideData && rideData.summary) {
            const startDate = new Date(rideData.summary.start_time);
            const rideMonth = startDate.getMonth() + 1; // 1-12
            if (rideMonth === 3) {
                defaultThemeClass = 'theme-flandrian';
            } else if (rideMonth === 4 || rideMonth === 5) {
                defaultThemeClass = 'theme-giro';
            } else if (rideMonth === 6 || rideMonth === 7) {
                defaultThemeClass = 'theme-tour';
            } else if (rideMonth === 8 || rideMonth === 9) {
                defaultThemeClass = 'theme-vuelta';
            }
        }

        let currentAccentColor = '#00d2ff';
        let currentAccentGlow = 'rgba(0, 210, 255, 0.15)';

        function applyTheme(themeName) {
            let displayThemeName = 'Carbon Dark';

            if (themeName === 'theme-flandrian') {
                currentAccentColor = '#F5C400';
                currentAccentGlow = 'rgba(245, 196, 0, 0.15)';
                displayThemeName = 'Classics Flandrian';
            } else if (themeName === 'theme-giro') {
                currentAccentColor = '#FF8BB4';
                currentAccentGlow = 'rgba(255, 139, 180, 0.15)';
                displayThemeName = 'Giro Pink';
            } else if (themeName === 'theme-tour') {
                currentAccentColor = '#FDE100';
                currentAccentGlow = 'rgba(253, 225, 0, 0.15)';
                displayThemeName = 'Tour Yellow';
            } else if (themeName === 'theme-vuelta') {
                currentAccentColor = '#E30613';
                currentAccentGlow = 'rgba(227, 6, 19, 0.15)';
                displayThemeName = 'Vuelta Red';
            } else {
                themeName = 'theme-carbon';
                currentAccentColor = '#00d2ff';
                currentAccentGlow = 'rgba(0, 210, 255, 0.15)';
            }

            document.body.className = themeName;
            
            const themeSelector = document.getElementById('theme-selector');
            if (themeSelector) {
                themeSelector.style.borderColor = currentAccentColor;
                themeSelector.style.color = currentAccentColor;
                themeSelector.style.background = currentAccentGlow;
                themeSelector.style.boxShadow = "0 0 15px " + currentAccentGlow;
                themeSelector.value = themeName;
            }

            // Dynamically update the charts colors
            updateChartColors();
        }

        function updateChartColors() {
            if (powerChart) {
                powerChart.data.datasets[1].borderColor = currentAccentColor;
                powerChart.data.datasets[1].backgroundColor = currentAccentGlow;
                powerChart.update();
            }
            if (speedAltChart) {
                speedAltChart.data.datasets[0].borderColor = currentAccentColor;
                speedAltChart.options.scales['y-speed'].title.color = currentAccentColor;
                speedAltChart.options.scales['y-speed'].ticks.color = currentAccentColor;
                speedAltChart.update();
            }
            if (altGearsChart) {
                altGearsChart.data.datasets[1].borderColor = currentAccentColor;
                altGearsChart.options.scales['y-ratio'].title.color = currentAccentColor;
                altGearsChart.options.scales['y-ratio'].ticks.color = currentAccentColor;
                altGearsChart.update();
            }
            if (powerCurveChart) {
                powerCurveChart.data.datasets[0].backgroundColor = currentAccentColor;
                powerCurveChart.update();
            }
            if (routePolyline) {
                routePolyline.setStyle({ color: currentAccentColor });
            }
        }

        function destroyAllCharts() {
            if (powerChart) { powerChart.destroy(); powerChart = null; }
            if (speedAltChart) { speedAltChart.destroy(); speedAltChart = null; }
            if (hrCadenceChart) { hrCadenceChart.destroy(); hrCadenceChart = null; }
            if (altGearsChart) { altGearsChart.destroy(); altGearsChart = null; }
            if (powerCurveChart) { powerCurveChart.destroy(); powerCurveChart = null; }
            if (chartPZones) { chartPZones.destroy(); chartPZones = null; }
            if (chartHZones) { chartHZones.destroy(); chartHZones = null; }
            if (quadrantAnalysisChart) { quadrantAnalysisChart.destroy(); quadrantAnalysisChart = null; }
        }

        // Initialize theme on load
        window.hideLoadingOverlay = () => {
            const overlay = document.getElementById('global-loading-overlay');
            if (overlay) {
                overlay.style.opacity = '0';
                setTimeout(() => { overlay.style.display = 'none'; }, 500);
            }
        };

        window.addEventListener('DOMContentLoaded', async () => {
            await clientStorage.init();

            // Perform one-time migration of history to flat dictionary format
            try {
                if (!clientStorage.getItem('fit_training_plans_by_date')) {
                    const plansByDate = {};
                    const weeklySummaries = {};
                    
                    let historyList = [];
                    try {
                        const historyData = clientStorage.getItem('fit_training_programs_history');
                        if (historyData) historyList = JSON.parse(historyData);
                    } catch(e) {}
                    
                    try {
                        const legacyPlan = clientStorage.getItem('fit_training_program');
                        if (legacyPlan) {
                            const parsed = JSON.parse(legacyPlan);
                            if (parsed && !historyList.some(p => p.start_date === parsed.start_date)) {
                                historyList.push(parsed);
                            }
                        }
                    } catch(e) {}

                    const weekdayOffsets = {
                        'monday': 0, 'tuesday': 1, 'wednesday': 2, 'thursday': 3, 'friday': 4, 'saturday': 5, 'sunday': 6
                    };

                    historyList.forEach(plan => {
                        if (plan && plan.start_date && plan.days) {
                            let planStart = parseLocalDate(plan.start_date);
                            
                            // Apply legacy self-heal: adjust start_date if it was previously normalized to a Monday
                            if (plan.days[0]) {
                                const firstDayLower = (plan.days[0].day || '').toLowerCase();
                                let foundDay = '';
                                for (const name of Object.keys(weekdayOffsets)) {
                                    if (firstDayLower.includes(name)) {
                                        foundDay = name;
                                        break;
                                    }
                                }
                                const offset = foundDay ? weekdayOffsets[foundDay] : undefined;
                                if (typeof offset === 'number' && offset > 0) {
                                    const monday = getMonday(plan.start_date);
                                    const actualStart = new Date(monday);
                                    actualStart.setDate(monday.getDate() + offset);
                                    planStart = actualStart;
                                }
                            }
                            planStart.setHours(0,0,0,0);
                            const mondayStr = formatLocalDateKey(getMonday(planStart));
                            if (plan.weekly_summary) {
                                weeklySummaries[mondayStr] = plan.weekly_summary;
                            }
                            
                            plan.days.forEach((day, idx) => {
                                const dDate = new Date(planStart);
                                dDate.setDate(planStart.getDate() + idx);
                                const key = formatLocalDateKey(dDate);
                                plansByDate[key] = day;
                            });
                        }
                    });

                    clientStorage.setItem('fit_training_plans_by_date', JSON.stringify(plansByDate));
                    clientStorage.setItem('fit_weekly_summaries', JSON.stringify(weeklySummaries));
                }
            } catch(e) {
                console.error("Migration error:", e);
            }

            try {
                selfHealTrainingPlans();
            } catch(e) {
                console.error("Error running startup self-heal:", e);
            }
            
            if (rideData && rideData.summary) {
                const startDate = new Date(rideData.summary.start_time);
                const rideMonth = startDate.getMonth() + 1; // 1-12
                let defaultThemeClass = 'theme-carbon';
                if (rideMonth === 3) defaultThemeClass = 'theme-flandrian';
                else if (rideMonth === 4 || rideMonth === 5) defaultThemeClass = 'theme-giro';
                else if (rideMonth === 6 || rideMonth === 7) defaultThemeClass = 'theme-tour';
                else if (rideMonth === 8 || rideMonth === 9) defaultThemeClass = 'theme-vuelta';
                applyTheme(defaultThemeClass);
            } else {
                applyTheme('theme-carbon');
            }

            // Bind Data & Backup view action buttons
            if (typeof bindDataViewListeners === 'function') {
                bindDataViewListeners();
            }

            // Restore active view or default to landing
            const activeView = clientStorage.getItem('directeur_active_view') || 'landing';
            if (typeof switchToView === 'function') {
                switchToView(activeView);
            }

            document.getElementById('theme-selector').addEventListener('change', (e) => {
                applyTheme(e.target.value);
            });

            // Initialize Bike Selector from Config
            initializeBikeSelector();

            // Bind bike selector change listener
            const bikeSelector = document.getElementById('bike-selector');
            if (bikeSelector) {
                bikeSelector.addEventListener('change', () => {
                    const selectedBikeName = bikeSelector.value;
                    
                    // Persist selected bike to localStorage
                    if (selectedBikeName) {
                        clientStorage.setItem('directeur_selected_bike', selectedBikeName);
                    } else {
                        clientStorage.removeItem('directeur_selected_bike');
                    }

                    // Save association in fit_ride_history for the current ride
                    const historyData = clientStorage.getItem('fit_ride_history');
                    if (historyData) {
                        try {
                            const history = JSON.parse(historyData);
                            const rideId = rideData && rideData.summary ? rideData.summary.start_time : null;
                            if (rideId) {
                                const ride = history.find(r => r.id === rideId);
                                if (ride) {
                                    ride.bike = selectedBikeName;
                                    clientStorage.setItem('fit_ride_history', JSON.stringify(history));
                                    try {
                                        renderRidesCalendar();
                                    } catch(e){}
                                }
                            }
                        } catch (e) {
                            console.error("Error updating ride bike in history:", e);
                        }
                    }

                    // Update URL query parameter
                    const url = new URL(window.location.href);
                    if (selectedBikeName) {
                        url.searchParams.set('bike', selectedBikeName);
                    } else {
                        url.searchParams.delete('bike');
                    }
                    window.history.replaceState({}, '', url);
                    
                    if (window.location.protocol.startsWith('http') && currentRideParam) {
                        // Running on a server, perform server-side re-analysis
                        loadRideData(currentRideSource, currentRideParam, currentRideParam2);
                    } else {
                        // Running locally/statically, perform client-side recalculation
                        recalculateGearsClientSide(selectedBikeName);
                    }
                });
            }

            let calendarWeekOffset = 0;

            // ==========================================
            // Collapsible Rides Calendar Logic
            // ==========================================
            const renderRidesCalendar = () => {
                const container = document.getElementById('rides-calendar-grid');
                const statsSpan = document.getElementById('rides-calendar-stats');
                const titleDiv = document.getElementById('rides-calendar-title');
                if (!container) return;

                container.innerHTML = '';

                // Calculate date range based on offset, starting on Monday
                const today = new Date();
                const startDay = getMonday(today);
                startDay.setDate(startDay.getDate() - (calendarWeekOffset * 7));
                startDay.setHours(0, 0, 0, 0);

                const endDayLimit = new Date(startDay);
                endDayLimit.setDate(startDay.getDate() + 7);

                // Update Title date range
                if (titleDiv) {
                    const options = { month: 'short', day: 'numeric' };
                    const rangeEnd = new Date(startDay);
                    rangeEnd.setDate(startDay.getDate() + 6);
                    const dateRangeStr = startDay.toLocaleDateString('en-US', options) + ' - ' + rangeEnd.toLocaleDateString('en-US', options);
                    
                    if (calendarWeekOffset === 0) {
                        titleDiv.innerHTML = '<span>📅</span> Recent Ride Activity (This Week: ' + dateRangeStr + ')';
                    } else if (calendarWeekOffset === 1) {
                        titleDiv.innerHTML = '<span>📅</span> Recent Ride Activity (1 Week Ago: ' + dateRangeStr + ')';
                    } else {
                        titleDiv.innerHTML = '<span>📅</span> Recent Ride Activity (' + calendarWeekOffset + ' Weeks Ago: ' + dateRangeStr + ')';
                    }
                }

                // Update Next button disabled state
                const btnNext = document.getElementById('btn-next-week');
                if (btnNext) {
                    btnNext.disabled = (calendarWeekOffset === 0);
                    btnNext.style.opacity = (calendarWeekOffset === 0) ? '0.5' : '1';
                }

                const ridesByDate = {};
                let totalRideCount = 0;

                const formatLocalDateKey = (d) => {
                    const year = d.getFullYear();
                    const month = String(d.getMonth() + 1).padStart(2, '0');
                    const day = String(d.getDate()).padStart(2, '0');
                    return year + '-' + month + '-' + day;
                };

                const addRideToGroup = (dateStr, rideObj) => {
                    if (!ridesByDate[dateStr]) {
                        ridesByDate[dateStr] = [];
                    }
                    ridesByDate[dateStr].push(rideObj);
                    totalRideCount++;
                };

                // Group local rides
                if (window.allRidesData && window.allRidesData.local) {
                    window.allRidesData.local.forEach(file => {
                        if (!file.filename) return;
                        const parts = file.filename.match(/^(\d{4})[-_](\d{2})[-_](\d{2})[-_](\d{2})[-_](\d{2})[-_](\d{2})/);
                        if (parts) {
                            const fileTime = new Date(parseInt(parts[1]), parseInt(parts[2]) - 1, parseInt(parts[3]), parseInt(parts[4]), parseInt(parts[5]), parseInt(parts[6]));
                            if (!isNaN(fileTime.getTime()) && fileTime >= startDay && fileTime < endDayLimit) {
                                addRideToGroup(formatLocalDateKey(fileTime), {
                                    source: 'local',
                                param: file.filename,
                                param2: '',
                                label: 'Local: ' + file.filename,
                                distance: file.distance_meters ? (file.distance_meters / 1000).toFixed(1) + ' km' : 'FIT File',
                                timeStr: fileTime.toLocaleTimeString([], {hour: '2-digit', minute:'2-digit'}),
                                isFIT: true
                                });
                            }
                        }
                    });
                }

                // Group Hammerhead rides
                if (window.allRidesData && window.allRidesData.hammerhead) {
                    window.allRidesData.hammerhead.forEach(act => {
                        if (!act.startTime) return;
                        const fileTime = new Date(act.startTime);
                        if (!isNaN(fileTime.getTime()) && fileTime >= startDay && fileTime < endDayLimit) {
                            addRideToGroup(formatLocalDateKey(fileTime), {
                                source: 'hammerhead',
                                param: act.id,
                                param2: '',
                                label: act.name || 'Hammerhead Ride',
                                distance: (act.distance / 1000).toFixed(1) + ' km',
                                timeStr: fileTime.toLocaleTimeString([], {hour: '2-digit', minute:'2-digit'})
                            });
                        }
                    });
                }

                // Group Wahoo rides
                if (window.allRidesData && window.allRidesData.wahoo) {
                    window.allRidesData.wahoo.forEach(act => {
                        if (!act.starts) return;
                        const fileTime = new Date(act.starts);
                        if (!isNaN(fileTime.getTime()) && fileTime >= startDay && fileTime < endDayLimit) {
                            addRideToGroup(formatLocalDateKey(fileTime), {
                                source: 'wahoo',
                                param: act.id,
                                param2: act.file ? act.file.url : '',
                                label: act.name || 'Wahoo Ride',
                                distance: (act.distance / 1000).toFixed(1) + ' km',
                                timeStr: fileTime.toLocaleTimeString([], {hour: '2-digit', minute:'2-digit'})
                            });
                        }
                    });
                }

                // Group Intervals rides
                if (window.allRidesData && window.allRidesData.intervals) {
                    window.allRidesData.intervals.forEach(act => {
                        if (!act.start_time) return;
                        const fileTime = new Date(act.start_time);
                        if (!isNaN(fileTime.getTime()) && fileTime >= startDay && fileTime < endDayLimit) {
                            addRideToGroup(formatLocalDateKey(fileTime), {
                                source: 'intervals',
                                param: act.id,
                                param2: '',
                                label: act.name || 'Intervals.icu Ride',
                                distance: act.distance_km + ' km',
                                timeStr: fileTime.toLocaleTimeString([], {hour: '2-digit', minute:'2-digit'})
                            });
                        }
                    });
                }

                const historyData = clientStorage.getItem('fit_ride_history');
                const historyList = historyData ? JSON.parse(historyData) : [];

                if (statsSpan) {
                    if (calendarWeekOffset === 0) {
                        statsSpan.textContent = totalRideCount + ' ride(s) in this week';
                    } else if (calendarWeekOffset === 1) {
                        statsSpan.textContent = totalRideCount + ' ride(s) in week (1 week ago)';
                    } else {
                        statsSpan.textContent = totalRideCount + ' ride(s) in week (' + calendarWeekOffset + ' weeks ago)';
                    }
                }

                // Render 7 day cards
                for (let i = 0; i < 7; i++) {
                    const currentDay = new Date(startDay);
                    currentDay.setDate(startDay.getDate() + i);

                    const dateKey = formatLocalDateKey(currentDay);
                    const dayRides = ridesByDate[dateKey] || [];
                    
                    const isAnalyzed = dayRides.some(r => {
                        const rTime = new Date(currentDay);
                        return historyList.some(h => {
                            const hTime = new Date(h.id);
                            return hTime.getFullYear() === rTime.getFullYear() &&
                                   hTime.getMonth() === rTime.getMonth() &&
                                   hTime.getDate() === rTime.getDate();
                        });
                    });

                    const dayCard = document.createElement('div');
                    dayCard.style.background = 'rgba(255,255,255,0.02)';
                    dayCard.style.border = '1px solid var(--border-color)';
                    dayCard.style.borderRadius = '10px';
                    dayCard.style.padding = '0.4rem 0.5rem';
                    dayCard.style.display = 'flex';
                    dayCard.style.flexDirection = 'column';
                    dayCard.style.minWidth = '130px';
                    dayCard.style.minHeight = '85px';
                    dayCard.style.gap = '0.25rem';
                    dayCard.style.transition = 'all 0.2s';
                    dayCard.style.flex = '1 0 auto';
                    
                    const isToday = formatLocalDateKey(today) === dateKey;
                    if (isToday) {
                        dayCard.style.borderColor = 'var(--accent)';
                        dayCard.style.background = 'rgba(255,255,255,0.04)';
                    }

                    const dayName = currentDay.toLocaleDateString('en-US', { weekday: 'short' });
                    const dayNum = currentDay.getDate();
                    const monthName = currentDay.toLocaleDateString('en-US', { month: 'short' });

                    const dateLabel = document.createElement('div');
                    dateLabel.style.display = 'flex';
                    dateLabel.style.justifyContent = 'space-between';
                    dateLabel.style.alignItems = 'center';
                    dateLabel.style.fontSize = '0.7rem';
                    dateLabel.style.fontWeight = '600';
                    dateLabel.style.color = isToday ? 'var(--accent)' : 'var(--text-secondary)';
                    dateLabel.innerHTML = '<span>' + dayName + ', ' + monthName + ' ' + dayNum + '</span>';

                    if (isToday) {
                        dateLabel.innerHTML += '<span style="font-size: 0.55rem; background: var(--accent); color: #0a0a0c; padding: 1px 3px; border-radius: 3px; font-weight: 700; line-height: 1;">TODAY</span>';
                    }
                    dayCard.appendChild(dateLabel);

                    const ridesArea = document.createElement('div');
                    ridesArea.style.flex = '1';
                    ridesArea.style.display = 'flex';
                    ridesArea.style.flexDirection = 'column';
                    ridesArea.style.gap = '0.2rem';
                    ridesArea.style.justifyContent = 'center';

                    if (dayRides.length > 0) {
                        dayCard.style.background = 'rgba(255,255,255,0.05)';
                        if (isAnalyzed) {
                            dayCard.style.borderLeft = '3px solid #2ecc71';
                        } else {
                            dayCard.style.borderLeft = '3px solid var(--accent)';
                        }

                        dayRides.forEach(ride => {
                            const rideBtn = document.createElement('button');
                            rideBtn.className = 'btn-action';
                            rideBtn.style.width = '100%';
                            rideBtn.style.padding = '0.2rem 0.35rem';
                            rideBtn.style.fontSize = '0.65rem';
                            rideBtn.style.fontWeight = '600';
                            rideBtn.style.textAlign = 'left';
                            rideBtn.style.cursor = 'pointer';
                            rideBtn.style.whiteSpace = 'nowrap';
                            rideBtn.style.overflow = 'hidden';
                            rideBtn.style.textOverflow = 'ellipsis';
                            rideBtn.style.display = 'flex';
                            rideBtn.style.justifyContent = 'space-between';
                            rideBtn.style.alignItems = 'center';
                            rideBtn.style.borderRadius = '4px';

                            let badgeColor = '#ffffff';
                            let sourceLabel = '';

                            if (ride.source === 'local') {
                                sourceLabel = 'FIT';
                            } else if (ride.source === 'hammerhead') {
                                badgeColor = '#ff8b6b';
                                sourceLabel = 'HH';
                            } else if (ride.source === 'wahoo') {
                                badgeColor = '#e0aaff';
                                sourceLabel = 'WAH';
                            } else if (ride.source === 'intervals') {
                                badgeColor = 'var(--accent)';
                                sourceLabel = 'INT';
                            }

                            rideBtn.style.borderColor = 'rgba(255, 255, 255, 0.08)';
                            rideBtn.style.color = badgeColor;

                            rideBtn.innerHTML = '<span style="overflow: hidden; text-overflow: ellipsis; max-width: 70%;">' + ride.distance + '</span>' +
                                '<span style="font-size: 0.5rem; opacity: 0.8; font-weight: 700; border: 1px solid ' + badgeColor + '; padding: 0px 2px; border-radius: 2px; line-height: 1;">' + sourceLabel + '</span>';

                            rideBtn.title = ride.label + ' (' + ride.distance + ') at ' + ride.timeStr;

                            rideBtn.addEventListener('click', (e) => {
                                e.stopPropagation();
                                loadRideData(ride.source, ride.param, ride.param2);
                            });

                            ridesArea.appendChild(rideBtn);
                        });
                    } else {
                        const emptyText = document.createElement('div');
                        emptyText.style.fontSize = '0.65rem';
                        emptyText.style.color = 'rgba(255,255,255,0.1)';
                        emptyText.style.textAlign = 'center';
                        emptyText.textContent = '-';
                        ridesArea.appendChild(emptyText);
                    }

                    dayCard.appendChild(ridesArea);
                    container.appendChild(dayCard);
                }
            };
            window.renderRidesCalendar = renderRidesCalendar;

            // Bind Collapsible Rides Calendar toggling
            const calHeader = document.getElementById('rides-calendar-header');
            const calContent = document.getElementById('rides-calendar-content');
            const calArrow = document.getElementById('rides-calendar-arrow');
            const calBtnToggle = document.getElementById('btn-toggle-rides-calendar');
            
            const setCalendarCollapsed = (collapsed) => {
                if (collapsed) {
                    calContent.style.maxHeight = '0px';
                    calContent.style.opacity = '0';
                    calArrow.textContent = '▼';
                    calBtnToggle.innerHTML = '▼ Show Calendar';
                    clientStorage.setItem('directeur_calendar_collapsed', 'true');
                } else {
                    calContent.style.maxHeight = '500px';
                    calContent.style.opacity = '1';
                    calArrow.textContent = '▲';
                    calBtnToggle.innerHTML = '▲ Collapse';
                    clientStorage.setItem('directeur_calendar_collapsed', 'false');
                }
            };
            
            const toggleCalendar = (e) => {
                const isCollapsed = clientStorage.getItem('directeur_calendar_collapsed') === 'true';
                setCalendarCollapsed(!isCollapsed);
            };
            
            if (calHeader) calHeader.addEventListener('click', toggleCalendar);
            if (calBtnToggle) calBtnToggle.addEventListener('click', (e) => {
                e.stopPropagation();
                toggleCalendar();
            });
            
            const initialCollapsed = clientStorage.getItem('directeur_calendar_collapsed') !== 'false';
            setCalendarCollapsed(initialCollapsed);

            const btnPrev = document.getElementById('btn-prev-week');
            const btnNext = document.getElementById('btn-next-week');
            if (btnPrev) {
                btnPrev.addEventListener('click', (e) => {
                    e.stopPropagation();
                    calendarWeekOffset++;
                    renderRidesCalendar();
                });
            }
            if (btnNext) {
                btnNext.addEventListener('click', (e) => {
                    e.stopPropagation();
                    if (calendarWeekOffset > 0) {
                        calendarWeekOffset--;
                        renderRidesCalendar();
                    }
                });
            }

            const btnEvolve = document.getElementById('btn-evolve-dashboard');
            if (btnEvolve) {
                btnEvolve.addEventListener('click', generateCustomChart);
            }

            // Initial render - check query parameters first!
            const urlParams = new URLSearchParams(window.location.search);
            const qSource = urlParams.get('source');
            const qFile = urlParams.get('file');
            const qId = urlParams.get('id');
            const qUrl = urlParams.get('url');

            if (qSource && (qFile || qId)) {
                const param = qSource === 'local' ? qFile : qId;
                loadRideData(qSource, param, qUrl || '', false);
                showDashboardView();
            } else {
                renderDashboard(rideData);
            }
            try {
                initializeStaticCardsCollapse();
            } catch(e) {
                console.error("Error initializing static card collapse:", e);
            }
            updateIntervalsSyncUI();
        });

        function initializeBikeSelector() {
            const bikeSelector = document.getElementById('bike-selector');
            if (!bikeSelector) {
                window.hideLoadingOverlay();
                return;
            }
            
            // 1. Populate from embedded configBikes if available (for static mode)
            if (typeof configBikes !== 'undefined' && configBikes && configBikes.length > 0) {
                populateSelectorOptions(configBikes);
            }
            
            // 2. Query /api/rides on load to fetch the configured bikes from the live server
            if (window.location.protocol.startsWith('http')) {
                fetch('/api/rides')
                    .then(res => res.json())
                    .then(data => {
                        window.allRidesData = data;
                        try {
                            renderRidesCalendar();
                        } catch(e) {
                            console.error("Error rendering rides calendar on load:", e);
                        }
                        if (data.bikes && data.bikes.length > 0) {
                            populateSelectorOptions(data.bikes);
                        }
                        
                        // Re-render training calendar to resolve/heal completed rides on load
                        if (window.currentCalendarProgram) {
                            try {
                                renderTrainingCalendar(window.currentCalendarProgram);
                            } catch(e) {
                                console.error("Error re-rendering calendar on rides fetch:", e);
                            }
                        }

                        // Re-render the unified landing calendar to resolve completed rides
                        try {
                            if (typeof renderUnifiedLandingCalendar === 'function') {
                                renderUnifiedLandingCalendar();
                            }
                        } catch(e) {
                            console.error("Error rendering unified landing calendar on load:", e);
                        }
                        
                        // Handle connection error banner at the top of the page
                        const errBanner = document.getElementById('connection-error-banner');
                        if (data.hammerhead_error) {
                            const errMessage = document.getElementById('connection-error-message');
                            const reauthLink = document.getElementById('btn-reauth-banner');
                            if (errBanner && errMessage && reauthLink) {
                                errMessage.textContent = data.hammerhead_error;
                                const authUrl = 'https://api.hammerhead.io/v1/auth/oauth/authorize?client_id=' + encodeURIComponent(data.client_id) + '&redirect_uri=' + encodeURIComponent(window.location.origin + '/callback') + '&response_type=code&scope=activity:read%20route:write&state=directeur';
                                reauthLink.href = authUrl;
                                errBanner.style.display = 'block';
                            }
                        } else if (errBanner) {
                            errBanner.style.display = 'none';
                        }
                        window.hideLoadingOverlay();
                    })
                    .catch(err => {
                        console.log("Could not fetch bikes from server (normal if offline/static):", err);
                        window.hideLoadingOverlay();
                    });
            } else {
                window.hideLoadingOverlay();
            }
            
            function populateSelectorOptions(bikesList) {
                const currentVal = bikeSelector.value;
                bikeSelector.innerHTML = '<option value="">⚙️ Default Gears</option>';
                bikesList.forEach(bike => {
                    const opt = document.createElement('option');
                    opt.value = bike.name;
                    opt.textContent = '🚲 ' + bike.name;
                    bikeSelector.appendChild(opt);
                });
                bikeSelector.value = currentVal;
                if (bikeSelector.value !== currentVal) {
                    bikeSelector.value = '';
                }
                bikeSelector.style.display = 'block';
                
                const urlParams = new URLSearchParams(window.location.search);
                let initialBike = urlParams.get('bike');
                if (!initialBike) {
                    const rideId = rideData && rideData.summary ? rideData.summary.start_time : null;
                    if (rideId) {
                        const historyData = clientStorage.getItem('fit_ride_history');
                        if (historyData) {
                            try {
                                const history = JSON.parse(historyData);
                                const ride = history.find(r => r.id === rideId);
                                if (ride && ride.bike) {
                                    initialBike = ride.bike;
                                }
                            } catch(e){}
                        }
                    }
                }
                if (!initialBike) {
                    initialBike = clientStorage.getItem('directeur_selected_bike');
                }
                
                if (initialBike && bikesList.some(b => b.name === initialBike)) {
                    bikeSelector.value = initialBike;
                    setTimeout(() => {
                        recalculateGearsClientSide(initialBike);
                    }, 50);
                }
            }
        }

        function recalculateGearsClientSide(bikeName) {
            if (!rideData || !rideData.records) return;
            
            let bike = null;
            if (typeof configBikes !== 'undefined' && configBikes) {
                bike = configBikes.find(b => b.name === bikeName);
            }
            
            const currentRideId = rideData.summary.start_time + rideData.summary.duration_seconds;
            if (!window.originalRecords || window.originalRideId !== currentRideId) {
                window.originalRecords = JSON.parse(JSON.stringify(rideData.records));
                window.originalGearUsage = JSON.parse(JSON.stringify(rideData.gear_usage));
                window.originalSummary = JSON.parse(JSON.stringify(rideData.summary));
                window.originalRideId = currentRideId;
            }
            
            if (!bike) {
                rideData.records = JSON.parse(JSON.stringify(window.originalRecords));
                rideData.gear_usage = JSON.parse(JSON.stringify(window.originalGearUsage));
                rideData.summary = JSON.parse(JSON.stringify(window.originalSummary));
                renderDashboard(rideData);
                return;
            }
            
            const frontGears = bike.front_gears || [];
            const rearGears = bike.rear_gears || [];
            
            let frontShifts = 0;
            let rearShifts = 0;
            let lastFrontNum = 0;
            let lastRearNum = 0;
            
            const gearDurations = {};
            
            rideData.records.forEach((r, idx) => {
                const origRecord = window.originalRecords[idx];
                const fNum = origRecord.front_gear_num;
                const rNum = origRecord.rear_gear_num;
                
                let fTeeth = origRecord.front_gear_teeth;
                if (fNum > 0 && fNum <= frontGears.length) {
                    fTeeth = frontGears[fNum - 1];
                }
                
                let rTeeth = origRecord.rear_gear_teeth;
                if (rNum > 0 && rNum <= rearGears.length) {
                    rTeeth = rearGears[rNum - 1];
                }
                
                r.front_gear_teeth = fTeeth;
                r.rear_gear_teeth = rTeeth;
                r.gear_ratio = rTeeth > 0 ? fTeeth / rTeeth : 0;
                
                if (idx > 0) {
                    if (fNum !== lastFrontNum && lastFrontNum !== 0) frontShifts++;
                    if (rNum !== lastRearNum && lastRearNum !== 0) rearShifts++;
                }
                lastFrontNum = fNum;
                lastRearNum = rNum;
                
                if (fTeeth > 0 && rTeeth > 0) {
                    const combo = fTeeth + 'x' + rTeeth;
                    gearDurations[combo] = (gearDurations[combo] || 0) + 1;
                }
            });
            
            const duration = rideData.summary.duration_seconds || 1;
            const usage = [];
            for (const combo in gearDurations) {
                const secs = gearDurations[combo];
                const pct = (secs / duration) * 100.0;
                usage.push({
                    combination: combo,
                    seconds: secs,
                    percentage: pct
                });
            }
            usage.sort((a, b) => b.seconds - a.seconds);
            
            rideData.gear_usage = usage;
            rideData.summary.total_front_shifts = frontShifts;
            rideData.summary.total_rear_shifts = rearShifts;
            rideData.summary.total_shifts = frontShifts + rearShifts;
            
            renderDashboard(rideData);
        }

        // Modular renderDashboard function for dynamic updates
        function renderDashboard(data) {
            if (!data || !data.records || data.records.length === 0) {
                console.log("No ride data to render");
                document.getElementById('ride-date-sub').innerText = "No Ride Loaded";
                const reparseBtn = document.getElementById('btn-reparse-ride');
                if (reparseBtn) reparseBtn.style.display = 'none';
                return;
            }

            // Cache original data for client-side bike mapping swaps
            const currentRideId = data.summary.start_time + data.summary.duration_seconds;
            if (!window.originalRecords || window.originalRideId !== currentRideId) {
                window.originalRecords = JSON.parse(JSON.stringify(data.records));
                window.originalGearUsage = JSON.parse(JSON.stringify(data.gear_usage));
                window.originalSummary = JSON.parse(JSON.stringify(data.summary));
                window.originalRideId = currentRideId;
            }

            rideData = data;
            fullJSONString = JSON.stringify(rideData, null, 2);

            // Populate textarea JSON preview
            const jsonLines = fullJSONString.split('\n');
            const jsonPreview = jsonLines.slice(0, 100).join('\n') + 
                '\n\n... [Telemetry records truncated for performance. Download the full JSON file or copy it below] ...';
            document.getElementById('json-textarea').value = jsonPreview;

            // Apply Theme based on Month of the ride
            const startDate = new Date(rideData.summary.start_time);
            const rideMonth = startDate.getMonth() + 1; // 1-12
            let themeClass = 'theme-carbon';
            if (rideMonth === 3) themeClass = 'theme-flandrian';
            else if (rideMonth === 4 || rideMonth === 5) themeClass = 'theme-giro';
            else if (rideMonth === 6 || rideMonth === 7) themeClass = 'theme-tour';
            else if (rideMonth === 8 || rideMonth === 9) themeClass = 'theme-vuelta';
            applyTheme(themeClass);

            // Format Date
            const dateOptions = { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit' };
            document.getElementById('ride-date-sub').innerText = startDate.toLocaleDateString('en-US', dateOptions);

            // Toggle power related UI elements based on telemetry presence
            const hasPower = rideData.summary.max_power > 0 || rideData.summary.average_power > 0;
            const powerElements = [
                'card-np',
                'card-max-power',
                'card-tss',
                'card-quadrant',
                'card-ftp-history',
                'container-chart-power-zones',
                'container-power-zones-table',
                'card-power-timeline',
                'card-power-curve'
            ];
            powerElements.forEach(id => {
                const el = document.getElementById(id);
                if (el) {
                    el.style.display = hasPower ? '' : 'none';
                }
            });

            // Populate Stats
            document.getElementById('val-np').innerHTML = rideData.summary.normalized_power + ' <span class="stat-unit">W</span>';
            document.getElementById('val-avg-power').innerText = 'Avg: ' + Math.round(rideData.summary.average_power) + ' W';
            document.getElementById('val-max-power').innerHTML = rideData.summary.max_power + ' <span class="stat-unit">W</span>';
        
        // Intensity Factor (IF) & Training Stress Score (TSS) calculation
        const updateIFDisplay = () => {
            const np = rideData.summary.normalized_power;
            const duration = rideData.summary.duration_seconds;
            const ifVal = np / athleteFTP;
            const intensityFactorStr = ifVal.toFixed(2);
            
            // Calculate TSS
            const tss = Math.round((duration * np * ifVal) / (athleteFTP * 36));
            
            // Update TSS elements
            document.getElementById('val-tss').innerText = isNaN(tss) ? '-' : tss;
            const tssDetailsEl = document.getElementById('val-tss-details');
            if (tssDetailsEl) {
                tssDetailsEl.innerText = 'IF: ' + intensityFactorStr + ' | FTP: ' + athleteFTP + 'W';
            }
            
            // Update Max Power subtext: show Max Power as ratio to FTP
            const maxPower = rideData.summary.max_power;
            const maxFtpRatio = Math.round((maxPower / athleteFTP) * 100);
            const valMaxPowerSub = document.getElementById('val-max-power-sub');
            if (valMaxPowerSub) {
                valMaxPowerSub.innerText = maxFtpRatio + '% FTP (' + (maxPower / athleteFTP).toFixed(1) + 'x)';
            }
        };
        updateIFDisplay();
        window.updateIFDisplay = updateIFDisplay;

        document.getElementById('val-avg-hr').innerHTML = Math.round(rideData.summary.average_heart_rate) + ' <span class="stat-unit">bpm</span>';
        document.getElementById('val-max-hr').innerText = 'Max: ' + rideData.summary.max_heart_rate + ' bpm';

        document.getElementById('val-avg-cadence').innerHTML = Math.round(rideData.summary.average_cadence) + ' <span class="stat-unit">rpm</span>';
        document.getElementById('val-max-cadence').innerText = 'Max: ' + rideData.summary.max_cadence + ' rpm';

        document.getElementById('val-elevation').innerHTML = Math.round(rideData.summary.total_elevation_gain_meters) + ' <span class="stat-unit">m</span>';
        document.getElementById('val-alt-range').innerText = 'Range: ' + Math.round(rideData.summary.min_altitude_meters) + ' - ' + Math.round(rideData.summary.max_altitude_meters) + ' m';

        document.getElementById('val-distance').innerHTML = (rideData.summary.distance_meters / 1000).toFixed(2) + ' <span class="stat-unit">km</span>';
        
        // Format Duration
        const durSecs = rideData.summary.duration_seconds;
        const hrs = Math.floor(durSecs / 3600);
        const mins = Math.floor((durSecs % 3600) / 60);
        const secs = Math.floor(durSecs % 60);
        const durStr = hrs > 0 ? (hrs + 'h ' + mins + 'm ' + secs + 's') : (mins + 'm ' + secs + 's');
        document.getElementById('val-duration').innerText = 'Duration: ' + durStr;

        // Shifting stats
        document.getElementById('val-shifts-front').innerText = rideData.summary.total_front_shifts;
        document.getElementById('val-shifts-rear').innerText = rideData.summary.total_rear_shifts;
        document.getElementById('val-shifts-total').innerText = rideData.summary.total_shifts;

        // Update Gear Combo Usage List
        const listContainer = document.getElementById('gear-usage-list');
        listContainer.innerHTML = '';
        if (rideData.gear_usage && rideData.gear_usage.length > 0) {
            rideData.gear_usage.forEach(g => {
                listContainer.innerHTML += '<li class="gear-item">' +
                    '<div><span class="gear-combo">' + g.combination + '</span></div>' +
                    '<div class="gear-bar-container">' +
                    '<div class="gear-bar" style="width: ' + g.percentage + '%;"></div>' +
                    '</div>' +
                    '<div class="gear-duration">' + g.percentage.toFixed(1) + '%</div>' +
                    '</li>';
            });
        } else {
            listContainer.innerHTML = '<li class="gear-item">No gear data found</li>';
        }

        // Clean up previous leaflet map if it exists
        if (leafletMap) {
            try {
                leafletMap.remove();
            } catch (e) {
                console.error("Error removing map instance:", e);
            }
            leafletMap = null;
        }

        // Initialize Map
        const routeCoords = rideData.records
            .filter(r => r.latitude_deg !== 0 && r.longitude_deg !== 0)
            .map(r => [r.latitude_deg, r.longitude_deg]);

        const mapContainer = document.getElementById('map');
        if (routeCoords.length > 0) {
            mapContainer.innerHTML = '';
            mapContainer.style.display = 'block';
            leafletMap = L.map('map', {
                zoomControl: true,
                dragging: true,
                scrollWheelZoom: false
            });

            L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
                attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>',
                subdomains: 'abcd',
                maxZoom: 20
            }).addTo(leafletMap);

            routePolyline = L.polyline(routeCoords, {
                color: currentAccentColor,
                weight: 4,
                opacity: 0.85,
                lineJoin: 'round'
            }).addTo(leafletMap);

            const startDot = L.divIcon({
                className: 'start-marker',
                html: '<div style="background-color: #2ecc71; width: 12px; height: 12px; border-radius: 50%; border: 2px solid white; box-shadow: 0 0 10px rgba(0,0,0,0.5);"></div>',
                iconSize: [12, 12],
                iconAnchor: [6, 6]
            });
            const endDot = L.divIcon({
                className: 'end-marker',
                html: '<div style="background-color: #e74c3c; width: 12px; height: 12px; border-radius: 50%; border: 2px solid white; box-shadow: 0 0 10px rgba(0,0,0,0.5);"></div>',
                iconSize: [12, 12],
                iconAnchor: [6, 6]
            });

            startMarker = L.marker(routeCoords[0], { icon: startDot }).addTo(leafletMap);
            endMarker = L.marker(routeCoords[routeCoords.length - 1], { icon: endDot }).addTo(leafletMap);

            leafletMap.invalidateSize();
            leafletMap.fitBounds(routePolyline.getBounds(), { padding: [20, 20] });
        } else {
            mapContainer.style.display = 'flex';
            mapContainer.style.alignItems = 'center';
            mapContainer.style.justifyContent = 'center';
            mapContainer.style.backgroundColor = 'var(--bg-tertiary)';
            mapContainer.innerHTML = '<div style="color: var(--text-secondary); font-size: 1rem; font-weight: 500;">No GPS Route Data Found (Indoor Ride)</div>';
        }

        // Clean up old charts before recreating them
        destroyAllCharts();

        // Shared chart configuration helper
        const timeLabels = rideData.records.map(r => {
            const s = Math.round(r.elapsed_time_seconds);
            const m = Math.floor(s / 60);
            const hrs = Math.floor(m / 60);
            if (hrs > 0) {
                return hrs + ':' + String(m % 60).padStart(2, '0') + ':' + String(s % 60).padStart(2, '0');
            }
            return m + ':' + String(s % 60).padStart(2, '0');
        });

        const chartOptions = {
            responsive: true,
            maintainAspectRatio: false,
            interaction: {
                mode: 'index',
                intersect: false,
            },
            plugins: {
                legend: {
                    labels: { color: '#94a3b8', font: { family: 'Outfit' } }
                },
                tooltip: {
                    backgroundColor: '#1b1b26',
                    titleFont: { family: 'Outfit', size: 13 },
                    bodyFont: { family: 'Outfit', size: 12 },
                    borderColor: '#27273a',
                    borderWidth: 1
                }
            },
            scales: {
                x: {
                    grid: { color: 'rgba(255, 255, 255, 0.02)' },
                    ticks: { color: '#94a3b8', font: { family: 'Outfit', size: 10 } }
                },
                y: {
                    grid: { color: 'rgba(255, 255, 255, 0.02)' },
                    ticks: { color: '#94a3b8', font: { family: 'Outfit', size: 10 } }
                }
            }
        };

        // Chart 1: Power & 30s Power
        powerChart = new Chart(document.getElementById('chart-power').getContext('2d'), {
            type: 'line',
            data: {
                labels: timeLabels,
                datasets: [
                    {
                        label: 'Instant Power (W)',
                        data: rideData.records.map(r => r.power),
                        borderColor: 'rgba(255,255,255,0.15)',
                        borderWidth: 1,
                        fill: false,
                        pointRadius: 0,
                    },
                    {
                        label: '30s Power (W)',
                        data: rideData.records.map(r => r.power_30s),
                        borderColor: currentAccentColor,
                        borderWidth: 2,
                        backgroundColor: currentAccentGlow,
                        fill: true,
                        pointRadius: 0,
                    }
                ]
            },
            options: chartOptions
        });

        // Chart 2: Speed & Altitude Chart
        speedAltChart = new Chart(document.getElementById('chart-speed-alt').getContext('2d'), {
            type: 'line',
            data: {
                labels: timeLabels,
                datasets: [
                    {
                        label: 'Speed (km/h)',
                        data: rideData.records.map(r => r.speed_kmh),
                        borderColor: currentAccentColor,
                        borderWidth: 2,
                        yAxisID: 'y-speed',
                        fill: false,
                        pointRadius: 0,
                    },
                    {
                        label: 'Altitude (m)',
                        data: rideData.records.map(r => r.altitude_meters),
                        borderColor: 'rgba(255, 255, 255, 0.3)',
                        borderWidth: 1.5,
                        backgroundColor: 'rgba(255, 255, 255, 0.02)',
                        fill: true,
                        yAxisID: 'y-alt',
                        pointRadius: 0,
                    }
                ]
            },
            options: {
                ...chartOptions,
                scales: {
                    x: chartOptions.scales.x,
                    'y-speed': {
                        type: 'linear',
                        position: 'left',
                        grid: { color: 'rgba(255, 255, 255, 0.02)' },
                        ticks: { color: currentAccentColor, font: { family: 'Outfit', size: 10 } },
                        title: { display: true, text: 'Speed (km/h)', color: currentAccentColor, font: { family: 'Outfit' } }
                    },
                    'y-alt': {
                        type: 'linear',
                        position: 'right',
                        grid: { drawOnChartArea: false },
                        ticks: { color: '#94a3b8', font: { family: 'Outfit', size: 10 } },
                        title: { display: true, text: 'Altitude (m)', color: '#94a3b8', font: { family: 'Outfit' } }
                    }
                }
            }
        });

        // Chart 3: HR & Cadence
        const maxCadVal = Math.max(120, ...data.records.map(rec => rec.cadence || 0));
        hrCadenceChart = new Chart(document.getElementById('chart-hr-cadence').getContext('2d'), {
            type: 'line',
            data: {
                labels: timeLabels,
                datasets: [
                    {
                        label: 'Coasting (Not Pedalling)',
                        data: data.records.map(r => r.cadence === 0 ? maxCadVal : 0),
                        borderColor: 'transparent',
                        backgroundColor: 'rgba(231, 76, 60, 0.08)',
                        fill: true,
                        yAxisID: 'y-cadence',
                        pointRadius: 0,
                        stepped: 'before',
                    },
                    {
                        label: 'Heart Rate (bpm)',
                        data: data.records.map(r => r.heart_rate),
                        borderColor: '#e74c3c',
                        borderWidth: 2,
                        yAxisID: 'y-hr',
                        fill: false,
                        pointRadius: 0,
                    },
                    {
                        label: 'Cadence (rpm)',
                        data: data.records.map(r => r.cadence),
                        borderColor: '#2ecc71',
                        borderWidth: 1.5,
                        yAxisID: 'y-cadence',
                        fill: false,
                        pointRadius: 0,
                    }
                ]
            },
            options: {
                ...chartOptions,
                scales: {
                    x: chartOptions.scales.x,
                    'y-hr': {
                        type: 'linear',
                        position: 'left',
                        grid: { color: 'rgba(255, 255, 255, 0.02)' },
                        ticks: { color: '#e74c3c', font: { family: 'Outfit', size: 10 } },
                        title: { display: true, text: 'Heart Rate (bpm)', color: '#e74c3c', font: { family: 'Outfit' } }
                    },
                    'y-cadence': {
                        type: 'linear',
                        position: 'right',
                        grid: { drawOnChartArea: false },
                        ticks: { color: '#2ecc71', font: { family: 'Outfit', size: 10 } },
                        title: { display: true, text: 'Cadence (rpm)', color: '#2ecc71', font: { family: 'Outfit' } }
                    }
                }
            }
        });

        // Calculate Normalised Cadence stats
        const pedallingCadences = data.records.map(r => r.cadence || 0).filter(c => c > 0);
        let normalisedCadence = 0;
        let pedallingPercent = 0;
        let pedallingMin = 0;
        let pedallingMax = 0;
        let cadenceStDev = 0;
        
        if (pedallingCadences.length > 0) {
            const sum = pedallingCadences.reduce((a, b) => a + b, 0);
            normalisedCadence = sum / pedallingCadences.length;
            pedallingMin = Math.min(...pedallingCadences);
            pedallingMax = Math.max(...pedallingCadences);
            
            const mean = normalisedCadence;
            const squareDiffs = pedallingCadences.map(c => (c - mean) * (c - mean));
            const avgSquareDiff = squareDiffs.reduce((a, b) => a + b, 0) / squareDiffs.length;
            cadenceStDev = Math.sqrt(avgSquareDiff);
            pedallingPercent = (pedallingCadences.length / data.records.length) * 100;
        }

        const formatStatsDuration = (secs) => {
            const h = Math.floor(secs / 3600);
            const m = Math.floor((secs % 3600) / 60);
            const s = Math.floor(secs % 60);
            if (h > 0) return h + 'h ' + m + 'm';
            return m + 'm ' + s + 's';
        };

        const statsNormVal = document.getElementById('stats-norm-cadence');
        const statsPedPct = document.getElementById('stats-pedalling-percent');
        const statsPedRange = document.getElementById('stats-pedalling-range');
        const statsCadVar = document.getElementById('stats-cadence-variance');

        if (statsNormVal && statsPedPct && statsPedRange && statsCadVar) {
            statsNormVal.innerHTML = Math.round(normalisedCadence) + ' <span style="font-size: 0.8rem; font-weight: 500; color: var(--text-secondary);">rpm</span>';
            statsPedPct.innerHTML = Math.round(pedallingPercent) + '% <span style="font-size: 0.75rem; font-weight: 500; color: var(--text-secondary);">(' + formatStatsDuration(pedallingCadences.length) + ')</span>';
            statsPedRange.innerHTML = pedallingMin + '/' + pedallingMax + ' <span style="font-size: 0.8rem; font-weight: 500; color: var(--text-secondary);">rpm</span>';
            statsCadVar.innerHTML = cadenceStDev.toFixed(1) + ' <span style="font-size: 0.8rem; font-weight: 500; color: var(--text-secondary);">rpm</span>';
        }

        // Advanced Shifting Analytics (Gear Choice vs. Terrain)
        const frontTeethSet = new Set();
        const rearTeethSet = new Set();
        rideData.records.forEach(r => {
            if (r.front_gear_teeth > 0) frontTeethSet.add(r.front_gear_teeth);
            if (r.rear_gear_teeth > 0) rearTeethSet.add(r.rear_gear_teeth);
        });
        const frontGears = Array.from(frontTeethSet).sort((a, b) => a - b);
        const rearGears = Array.from(rearTeethSet).sort((a, b) => a - b);
        const drivetrain = {
            frontGears,
            rearGears,
            isDouble: frontGears.length >= 2,
            bigRing: frontGears[frontGears.length - 1] || 0,
            smallRing: frontGears[0] || 0,
            largestRearCogs: rearGears.slice(-2),
            smallestRearCogs: rearGears.slice(0, 2)
        };

        // Instantaneous Incline/Grade Calculation (10-second rolling window)
        const grades = new Array(rideData.records.length).fill(0);
        const windowSize = 10;
        for (let i = 0; i < rideData.records.length; i++) {
            let prevIdx = Math.max(0, i - windowSize);
            if (i === prevIdx) {
                grades[i] = 0;
                continue;
            }
            const distDiff = rideData.records[i].distance_meters - rideData.records[prevIdx].distance_meters;
            const altDiff = rideData.records[i].altitude_meters - rideData.records[prevIdx].altitude_meters;
            if (distDiff > 0.5) {
                grades[i] = (altDiff / distDiff) * 100;
                if (grades[i] > 30) grades[i] = 30;
                if (grades[i] < -30) grades[i] = -30;
            } else {
                grades[i] = i > 0 ? grades[i - 1] : 0;
            }
        }

        // Mechanical efficiency and stats
        let bigBigSeconds = 0;
        let smallSmallSeconds = 0;
        let bigBigPowerSum = 0, bigBigPowerCount = 0;
        let bigBigCadenceSum = 0, bigBigCadenceCount = 0;
        let bigBigGradeSum = 0, bigBigGradeCount = 0;
        
        let smallSmallPowerSum = 0, smallSmallPowerCount = 0;
        let smallSmallCadenceSum = 0, smallSmallCadenceCount = 0;
        let smallSmallGradeSum = 0, smallSmallGradeCount = 0;

        rideData.records.forEach((r, idx) => {
            if (!drivetrain.isDouble) return;
            const isBigBig = r.front_gear_teeth === drivetrain.bigRing && drivetrain.largestRearCogs.includes(r.rear_gear_teeth);
            const isSmallSmall = r.front_gear_teeth === drivetrain.smallRing && drivetrain.smallestRearCogs.includes(r.rear_gear_teeth);
            const curGrade = grades[idx] || 0;

            if (isBigBig) {
                bigBigSeconds++;
                if (r.power > 0) {
                    bigBigPowerSum += r.power;
                    bigBigPowerCount++;
                }
                if (r.cadence > 0) {
                    bigBigCadenceSum += r.cadence;
                    bigBigCadenceCount++;
                }
                bigBigGradeSum += curGrade;
                bigBigGradeCount++;
            } else if (isSmallSmall) {
                smallSmallSeconds++;
                if (r.power > 0) {
                    smallSmallPowerSum += r.power;
                    smallSmallPowerCount++;
                }
                if (r.cadence > 0) {
                    smallSmallCadenceSum += r.cadence;
                    smallSmallCadenceCount++;
                }
                smallSmallGradeSum += curGrade;
                smallSmallGradeCount++;
            }
        });

        const totalDurationSecs = rideData.summary.duration_seconds || 1;
        const bigBigPct = (bigBigSeconds / totalDurationSecs) * 100;
        const smallSmallPct = (smallSmallSeconds / totalDurationSecs) * 100;

        const avgBigBigPower = bigBigPowerCount > 0 ? Math.round(bigBigPowerSum / bigBigPowerCount) : 0;
        const avgBigBigCadence = bigBigCadenceCount > 0 ? Math.round(bigBigCadenceSum / bigBigCadenceCount) : 0;
        const avgBigBigGrade = bigBigGradeCount > 0 ? (bigBigGradeSum / bigBigGradeCount) : 0;

        const avgSmallSmallPower = smallSmallPowerCount > 0 ? Math.round(smallSmallPowerSum / smallSmallPowerCount) : 0;
        const avgSmallSmallCadence = smallSmallCadenceCount > 0 ? Math.round(smallSmallCadenceSum / smallSmallCadenceCount) : 0;
        const avgSmallSmallGrade = smallSmallGradeCount > 0 ? (smallSmallGradeSum / smallSmallGradeCount) : 0;

        // Render Stats Panel
        const totalShifts = (rideData.summary.total_front_shifts || 0) + (rideData.summary.total_rear_shifts || 0);
        document.getElementById('shift-total').innerText = totalShifts;
        document.getElementById('shift-front-rear').innerText = 'F: ' + (rideData.summary.total_front_shifts || 0) + ' / R: ' + (rideData.summary.total_rear_shifts || 0);
        
        if (drivetrain.isDouble) {
            document.getElementById('shift-cross-big').innerHTML = bigBigSeconds + 's <span style="font-size: 0.72rem; color: var(--text-secondary);">(' + bigBigPct.toFixed(1) + '%)</span>';
            document.getElementById('shift-cross-small').innerHTML = smallSmallSeconds + 's <span style="font-size: 0.72rem; color: var(--text-secondary);">(' + smallSmallPct.toFixed(1) + '%)</span>';
        } else {
            document.getElementById('shift-cross-big').innerText = 'N/A (1x Setup)';
            document.getElementById('shift-cross-small').innerText = 'N/A (1x Setup)';
        }

        // Diagnostic recommendation block
        let diagText = "";
        if (!drivetrain.isDouble) {
            diagText = "Your bike setup is configured as a single chainring (1x). Cross-chaining friction is mechanically managed by the chainring profile and clutched derailleur. Shifting discipline looks optimal.";
        } else {
            const crossPct = bigBigPct + smallSmallPct;
            if (crossPct === 0) {
                diagText = "Flawless shifting discipline! You did not spend any time in cross-chained gear combinations, keeping drivetrain friction at its absolute minimum.";
            } else if (crossPct < 1.0) {
                diagText = "Excellent drivetrain management. You spent very little time cross-chaining (" + crossPct.toFixed(1) + "%). This ensures maximum drivetrain efficiency and component life.";
            } else {
                diagText = "You spent " + Math.round(bigBigSeconds + smallSmallSeconds) + "s (" + crossPct.toFixed(1) + "% of ride) in cross-chained gear configurations. ";
                
                const recs = [];
                if (bigBigSeconds > 10) {
                    if (avgBigBigGrade > 3) {
                        recs.push("Big-Big cross-chaining occurred mostly on climbs (avg grade: " + avgBigBigGrade.toFixed(1) + "%). Swap to the small chainring and shift down the cassette to maintain efficiency.");
                    } else {
                        recs.push("Big-Big cross-chaining occurred on flat/descents (avg grade: " + avgBigBigGrade.toFixed(1) + "%), suggesting accidental gearing. Shift to the small chainring and a mid-cassette cog.");
                    }
                }
                if (smallSmallSeconds > 10) {
                    if (avgSmallSmallGrade < 1) {
                        recs.push("Small-Small cross-chaining occurred on flats/descents (avg grade: " + avgSmallSmallGrade.toFixed(1) + "%). Switch to the big chainring and shift up the cassette.");
                    } else {
                        recs.push("Small-Small cross-chaining occurred on uphill sections. Select a lower gear ratio to avoid high-torque cross-chain friction.");
                    }
                }
                diagText += recs.join(" ");
            }
        }
        document.getElementById('drivetrain-diagnostics-text').innerText = diagText;

        // Build Datasets for the Shifting Analytics Chart
        const crossChainingData = rideData.records.map(r => {
            if (!drivetrain.isDouble) return null;
            const isBigBig = r.front_gear_teeth === drivetrain.bigRing && drivetrain.largestRearCogs.includes(r.rear_gear_teeth);
            const isSmallSmall = r.front_gear_teeth === drivetrain.smallRing && drivetrain.smallestRearCogs.includes(r.rear_gear_teeth);
            return (isBigBig || isSmallSmall) ? r.gear_ratio : null;
        });

        altGearsChart = new Chart(document.getElementById('chart-alt-gears').getContext('2d'), {
            type: 'line',
            data: {
                labels: timeLabels,
                datasets: [
                    {
                        label: 'Altitude (m)',
                        data: rideData.records.map(r => r.altitude_meters),
                        borderColor: 'rgba(255,255,255,0.4)',
                        borderWidth: 2,
                        backgroundColor: 'rgba(255,255,255,0.03)',
                        fill: true,
                        yAxisID: 'y-alt',
                        pointRadius: 0,
                    },
                    {
                        label: 'Gear Ratio',
                        data: rideData.records.map(r => r.gear_ratio),
                        borderColor: currentAccentColor,
                        borderWidth: 2.5,
                        yAxisID: 'y-ratio',
                        fill: false,
                        pointRadius: 0,
                        stepped: true,
                        segment: {
                            borderColor: (ctx) => {
                                const idx = ctx.p0.parsed.x;
                                const r = rideData.records[idx];
                                if (!r) return currentAccentColor;
                                if (!drivetrain.isDouble) return currentAccentColor;
                                const isBigBig = r.front_gear_teeth === drivetrain.bigRing && drivetrain.largestRearCogs.includes(r.rear_gear_teeth);
                                const isSmallSmall = r.front_gear_teeth === drivetrain.smallRing && drivetrain.smallestRearCogs.includes(r.rear_gear_teeth);
                                if (isBigBig) return '#e74c3c'; // Red
                                if (isSmallSmall) return '#e67e22'; // Orange
                                if (r.front_gear_teeth === drivetrain.bigRing) return '#2ecc71'; // Green
                                if (r.front_gear_teeth === drivetrain.smallRing) return '#f1c40f'; // Yellow
                                return currentAccentColor;
                            }
                        }
                    },
                    {
                        label: 'Cross-Chaining Point',
                        data: crossChainingData,
                        borderColor: '#e74c3c',
                        backgroundColor: '#e74c3c',
                        pointRadius: (ctx) => {
                            const val = ctx.dataset.data[ctx.dataIndex];
                            return (val !== null && val !== undefined) ? 3.5 : 0;
                        },
                        pointHoverRadius: 5.5,
                        showLine: false,
                        yAxisID: 'y-ratio'
                    }
                ]
            },
            options: {
                ...chartOptions,
                plugins: {
                    ...chartOptions.plugins,
                    tooltip: {
                        ...chartOptions.plugins.tooltip,
                        callbacks: {
                            label: function(context) {
                                let label = context.dataset.label || '';
                                if (label) {
                                    label += ': ';
                                }
                                if (context.datasetIndex === 1 || context.datasetIndex === 2) {
                                    const record = rideData.records[context.dataIndex];
                                    if (record && record.front_gear_teeth > 0 && record.rear_gear_teeth > 0) {
                                        const isBigBig = record.front_gear_teeth === drivetrain.bigRing && drivetrain.largestRearCogs.includes(record.rear_gear_teeth);
                                        const isSmallSmall = record.front_gear_teeth === drivetrain.smallRing && drivetrain.smallestRearCogs.includes(record.rear_gear_teeth);
                                        let detail = context.raw.toFixed(2) + ' (' + record.front_gear_teeth + 'x' + record.rear_gear_teeth + ')';
                                        if (isBigBig || isSmallSmall) {
                                            detail += ' [CROSS-CHAINING!]';
                                        }
                                        return label + detail;
                                    }
                                    return label + context.raw.toFixed(2);
                                } else {
                                    return label + context.raw.toFixed(1) + ' m';
                                }
                            }
                        }
                    }
                },
                scales: {
                    x: chartOptions.scales.x,
                    'y-alt': {
                        type: 'linear',
                        position: 'left',
                        grid: { color: 'rgba(255, 255, 255, 0.02)' },
                        ticks: { color: '#94a3b8', font: { family: 'Outfit', size: 10 } },
                        title: { display: true, text: 'Altitude (m)', color: '#94a3b8', font: { family: 'Outfit' } }
                    },
                    'y-ratio': {
                        type: 'linear',
                        position: 'right',
                        grid: { drawOnChartArea: false },
                        ticks: { color: currentAccentColor, font: { family: 'Outfit', size: 10 } },
                        title: { display: true, text: 'Gear Ratio (Front/Rear)', color: currentAccentColor, font: { family: 'Outfit' } }
                    }
                }
            }
        });

        // Chart 5: Power Duration Curve (MMP)
        if (hasPower) {
            const curveLabels = ["1s", "3s", "5s", "30s", "1m", "3m", "5m", "20m", "1h"];
            const curveData = curveLabels.map(label => rideData.summary.power_curve[label] || 0);

            powerCurveChart = new Chart(document.getElementById('chart-power-curve').getContext('2d'), {
                type: 'bar',
                data: {
                    labels: curveLabels,
                    datasets: [{
                        label: 'Peak Power (W)',
                        data: curveData,
                        backgroundColor: currentAccentColor,
                        borderColor: 'rgba(255,255,255,0.1)',
                        borderWidth: 1,
                        borderRadius: 6,
                    }]
                },
                options: {
                    responsive: true,
                    maintainAspectRatio: false,
                    indexAxis: 'y', // horizontal bar chart
                    plugins: {
                        legend: { display: false },
                        tooltip: {
                            backgroundColor: '#1b1b26',
                            titleFont: { family: 'Outfit' },
                            bodyFont: { family: 'Outfit' }
                        }
                    },
                    scales: {
                        x: {
                            grid: { color: 'rgba(255, 255, 255, 0.02)' },
                            ticks: { color: '#94a3b8', font: { family: 'Outfit', size: 10 } },
                            title: { display: true, text: 'Watts (W)', color: '#94a3b8', font: { family: 'Outfit' } }
                        },
                        y: {
                            grid: { display: false },
                            ticks: { color: '#94a3b8', font: { family: 'Outfit', size: 10 } }
                        }
                    }
                }
            });
        }

        // ==========================================
        // Training Zones Calculations & Charts
        // ==========================================
        const renderZones = () => {
            const maxHR = athleteMaxHR || rideData.summary.max_heart_rate || 180;
            document.getElementById('zones-max-hr').innerText = maxHR;

            const pZones = [
                { name: 'Z1 - Active Recovery', min: 0, max: Math.round(athleteFTP * 0.55), secs: 0, color: 'rgba(52, 152, 219, 0.65)' },
                { name: 'Z2 - Endurance', min: Math.round(athleteFTP * 0.55) + 1, max: Math.round(athleteFTP * 0.75), secs: 0, color: 'rgba(46, 204, 113, 0.65)' },
                { name: 'Z3 - Tempo', min: Math.round(athleteFTP * 0.75) + 1, max: Math.round(athleteFTP * 0.90), secs: 0, color: 'rgba(241, 196, 15, 0.65)' },
                { name: 'Z4 - Threshold', min: Math.round(athleteFTP * 0.90) + 1, max: Math.round(athleteFTP * 1.05), secs: 0, color: 'rgba(230, 126, 34, 0.65)' },
                { name: 'Z5 - VO2 Max', min: Math.round(athleteFTP * 1.05) + 1, max: Math.round(athleteFTP * 1.20), secs: 0, color: 'rgba(231, 76, 60, 0.65)' },
                { name: 'Z6 - Anaerobic', min: Math.round(athleteFTP * 1.20) + 1, max: Math.round(athleteFTP * 1.50), secs: 0, color: 'rgba(155, 89, 182, 0.65)' },
                { name: 'Z7 - Neuromuscular', min: Math.round(athleteFTP * 1.50) + 1, max: 9999, secs: 0, color: 'rgba(149, 165, 166, 0.65)' }
            ];

            const hZones = [
                { name: 'Z1 - Recovery', min: 0, max: Math.round(maxHR * 0.60), secs: 0, color: 'rgba(52, 152, 219, 0.65)' },
                { name: 'Z2 - Endurance', min: Math.round(maxHR * 0.60) + 1, max: Math.round(maxHR * 0.70), secs: 0, color: 'rgba(46, 204, 113, 0.65)' },
                { name: 'Z3 - Tempo', min: Math.round(maxHR * 0.70) + 1, max: Math.round(maxHR * 0.80), secs: 0, color: 'rgba(241, 196, 15, 0.65)' },
                { name: 'Z4 - Threshold', min: Math.round(maxHR * 0.80) + 1, max: Math.round(maxHR * 0.90), secs: 0, color: 'rgba(230, 126, 34, 0.65)' },
                { name: 'Z5 - Anaerobic', min: Math.round(maxHR * 0.90) + 1, max: 999, secs: 0, color: 'rgba(231, 76, 60, 0.65)' }
            ];

            const records = rideData.records || [];
            let totalSecs = 0;

            records.forEach(r => {
                totalSecs++;
                if (hasPower) {
                    const power = r.power;
                    const pz = pZones.find(z => power >= z.min && power <= z.max);
                    if (pz) pz.secs++;
                }

                const hr = r.heart_rate;
                const hz = hZones.find(z => hr >= z.min && hr <= z.max);
                if (hz) hz.secs++;
            });

            const formatSecs = (s) => {
                const m = Math.floor(s / 60);
                const sec = s % 60;
                return m + 'm ' + sec + 's';
            };

            // Render Power Zones Table
            if (hasPower) {
                const pBody = document.getElementById('power-zones-tbody');
                if (pBody) {
                    pBody.innerHTML = '';
                    pZones.forEach(z => {
                        const pct = totalSecs ? ((z.secs / totalSecs) * 100).toFixed(1) : '0.0';
                        const rangeStr = z.max === 9999 ? '> ' + z.min + 'W' : z.min + ' - ' + z.max + 'W';
                        pBody.innerHTML += '<tr style="border-bottom: 1px solid rgba(255,255,255,0.02);">' +
                            '<td style="padding: 0.35rem 0; font-weight: 500; color: #ffffff;">' + z.name + '</td>' +
                            '<td style="padding: 0.35rem 0;">' + rangeStr + '</td>' +
                            '<td style="padding: 0.35rem 0; text-align: right; font-family: monospace;">' + formatSecs(z.secs) + '</td>' +
                            '<td style="padding: 0.35rem 0; text-align: right; font-weight: 600; color: ' + z.color.replace('0.65', '1') + ';">' + pct + '%</td>' +
                            '</tr>';
                    });
                }
            }

            // Render HR Zones Table
            const hBody = document.getElementById('hr-zones-tbody');
            if (hBody) {
                hBody.innerHTML = '';
                hZones.forEach(z => {
                    const pct = totalSecs ? ((z.secs / totalSecs) * 100).toFixed(1) : '0.0';
                    const rangeStr = z.max === 999 ? '> ' + z.min + ' bpm' : z.min + ' - ' + z.max + ' bpm';
                    hBody.innerHTML += '<tr style="border-bottom: 1px solid rgba(255,255,255,0.02);">' +
                        '<td style="padding: 0.35rem 0; font-weight: 500; color: #ffffff;">' + z.name + '</td>' +
                        '<td style="padding: 0.35rem 0;">' + rangeStr + '</td>' +
                        '<td style="padding: 0.35rem 0; text-align: right; font-family: monospace;">' + formatSecs(z.secs) + '</td>' +
                        '<td style="padding: 0.35rem 0; text-align: right; font-weight: 600; color: ' + z.color.replace('0.65', '1') + ';">' + pct + '%</td>' +
                        '</tr>';
                });
            }

            // Power Zones Chart
            if (hasPower) {
                if (chartPZones) {
                    chartPZones.destroy();
                }
                chartPZones = new Chart(document.getElementById('chart-power-zones').getContext('2d'), {
                    type: 'bar',
                    data: {
                        labels: pZones.map(z => z.name.split(' - ')[0]),
                        datasets: [{
                            label: 'Power Zones Time (seconds)',
                            data: pZones.map(z => z.secs),
                            backgroundColor: pZones.map(z => z.color),
                            borderColor: pZones.map(z => z.color.replace('0.65', '1')),
                            borderWidth: 1,
                            borderRadius: 4
                        }]
                    },
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        plugins: {
                            legend: { display: false },
                            tooltip: {
                                callbacks: {
                                    label: function(context) {
                                        const secs = context.parsed.y;
                                        const total = context.dataset.data.reduce((a, b) => a + b, 0);
                                        const pct = total ? ((secs / total) * 100).toFixed(1) : 0;
                                        return pZones[context.dataIndex].name + ': ' + formatSecs(secs) + ' (' + pct + '%)';
                                    }
                                }
                            }
                        },
                        scales: {
                            x: {
                                grid: { display: false },
                                ticks: { color: '#94a3b8', font: { family: 'Outfit', size: 10 } }
                            },
                            y: {
                                grid: { color: 'rgba(255,255,255,0.02)' },
                                ticks: {
                                    color: '#94a3b8',
                                    font: { family: 'Outfit', size: 10 },
                                    callback: function(val) { return formatSecs(val).split(' ')[0] + 'm'; }
                                }
                            }
                        }
                    }
                });
            }

            // HR Zones Chart
            if (chartHZones) {
                chartHZones.destroy();
            }
            chartHZones = new Chart(document.getElementById('chart-hr-zones').getContext('2d'), {
                type: 'bar',
                data: {
                    labels: hZones.map(z => z.name.split(' - ')[0]),
                    datasets: [{
                        label: 'HR Zones Time (seconds)',
                        data: hZones.map(z => z.secs),
                        backgroundColor: hZones.map(z => z.color),
                        borderColor: hZones.map(z => z.color.replace('0.65', '1')),
                        borderWidth: 1,
                        borderRadius: 4
                    }]
                },
                options: {
                    responsive: true,
                    maintainAspectRatio: false,
                    plugins: {
                        legend: { display: false },
                        tooltip: {
                            callbacks: {
                                label: function(context) {
                                    const secs = context.parsed.y;
                                    const total = context.dataset.data.reduce((a, b) => a + b, 0);
                                    const pct = total ? ((secs / total) * 100).toFixed(1) : 0;
                                    return hZones[context.dataIndex].name + ': ' + formatSecs(secs) + ' (' + pct + '%)';
                                }
                            }
                        }
                    },
                    scales: {
                        x: {
                            grid: { display: false },
                            ticks: { color: '#94a3b8', font: { family: 'Outfit', size: 10 } }
                        },
                        y: {
                            grid: { color: 'rgba(255,255,255,0.02)' },
                            ticks: {
                                color: '#94a3b8',
                                font: { family: 'Outfit', size: 10 },
                                callback: function(val) { return formatSecs(val).split(' ')[0] + 'm'; }
                            }
                        }
                    }
                }
            });
        };

        // Initial zones render
        renderZones();
        window.renderZones = renderZones;

        // ==========================================
        // Neuromuscular vs. Aerobic Load (Quadrant Analysis)
        // ==========================================
        const crankLength = 0.1725; // meters
        const activeRecords = (data.records || []).filter(r => (r.cadence || 0) > 0 && (r.power || 0) > 0);

        if (activeRecords.length > 0) {
            let totalCpv = 0;
            let totalAepf = 0;
            let totalPower = 0;
            let totalCadence = 0;

            const processedPoints = activeRecords.map(r => {
                const cpv = (r.cadence * 2 * Math.PI * crankLength) / 60;
                const aepf = r.power / cpv;
                
                totalCpv += cpv;
                totalAepf += aepf;
                totalPower += r.power;
                totalCadence += r.cadence;

                return { cpv, aepf, power: r.power, cadence: r.cadence };
            });

            const meanCpv = totalCpv / activeRecords.length;
            const meanAepf = totalAepf / activeRecords.length;
            const meanPower = totalPower / activeRecords.length;
            const meanCadence = totalCadence / activeRecords.length;

            const sortedCpv = processedPoints.map(p => p.cpv).sort((a, b) => a - b);
            const sortedAepf = processedPoints.map(p => p.aepf).sort((a, b) => a - b);

            // Use the 99th percentile to filter out extreme low-cadence spikes/outliers from the scaling
            const cpv99 = sortedCpv[Math.floor(sortedCpv.length * 0.99)] || meanCpv * 2;
            const aepf99 = sortedAepf[Math.floor(sortedAepf.length * 0.99)] || meanAepf * 2;

            const xMaxVal = Math.max(meanCpv * 2, cpv99);
            const yMaxVal = Math.max(meanAepf * 2, aepf99);

            // Sort points into Quadrants for stats and plot
            let quad1Count = 0;
            let quad2Count = 0;
            let quad3Count = 0;
            let quad4Count = 0;

            const q1Points = [];
            const q2Points = [];
            const q3Points = [];
            const q4Points = [];

            processedPoints.forEach(p => {
                const isHighForce = p.aepf >= meanAepf;
                const isHighVelocity = p.cpv >= meanCpv;

                if (isHighForce && isHighVelocity) {
                    quad1Count++;
                    q1Points.push({ x: p.cpv, y: p.aepf });
                } else if (isHighForce && !isHighVelocity) {
                    quad2Count++;
                    q2Points.push({ x: p.cpv, y: p.aepf });
                } else if (!isHighForce && !isHighVelocity) {
                    quad3Count++;
                    q3Points.push({ x: p.cpv, y: p.aepf });
                } else {
                    quad4Count++;
                    q4Points.push({ x: p.cpv, y: p.aepf });
                }
            });

            const totalCount = activeRecords.length;
            const pct1 = (quad1Count / totalCount) * 100;
            const pct2 = (quad2Count / totalCount) * 100;
            const pct3 = (quad3Count / totalCount) * 100;
            const pct4 = (quad4Count / totalCount) * 100;

            // Populate HTML stats
            document.getElementById('quad-active-time').innerText = formatStatsDuration(totalCount);
            document.getElementById('quad-mean-power').innerText = Math.round(meanPower) + ' W';
            document.getElementById('quad-mean-cadence').innerText = Math.round(meanCadence) + ' rpm';
            document.getElementById('quad-mean-cpv').innerText = meanCpv.toFixed(2) + ' m/s';
            document.getElementById('quad-mean-aepf').innerText = Math.round(meanAepf) + ' N';

            document.getElementById('quad-pct-1').innerText = pct1.toFixed(1) + '%';
            document.getElementById('quad-pct-2').innerText = pct2.toFixed(1) + '%';
            document.getElementById('quad-pct-3').innerText = pct3.toFixed(1) + '%';
            document.getElementById('quad-pct-4').innerText = pct4.toFixed(1) + '%';

            // Downsample datasets for performance to limit total scatter points plotted (~800 points total)
            const downsampleDataset = (points) => {
                const targetPoints = Math.max(1, Math.floor(points.length / (800 / 4)));
                if (targetPoints <= 1) return points;
                return points.filter((_, idx) => idx % targetPoints === 0);
            };

            const dsQ1 = downsampleDataset(q1Points);
            const dsQ2 = downsampleDataset(q2Points);
            const dsQ3 = downsampleDataset(q3Points);
            const dsQ4 = downsampleDataset(q4Points);

            // Custom plugins for crosshair lines & corner labels
            const quadrantPlugin = {
                id: 'quadrantCrosshairsAndLabels',
                afterDraw: (chart) => {
                    const { ctx, chartArea: { left, right, top, bottom }, scales: { x, y } } = chart;
                    ctx.save();
                    
                    // 1. Draw dashed crosshairs at mean CPV and mean AEPF
                    ctx.strokeStyle = 'rgba(255, 255, 255, 0.4)';
                    ctx.lineWidth = 1.5;
                    ctx.setLineDash([5, 5]);

                    const xPixel = x.getPixelForValue(meanCpv);
                    if (xPixel >= left && xPixel <= right) {
                        ctx.beginPath();
                        ctx.moveTo(xPixel, top);
                        ctx.lineTo(xPixel, bottom);
                        ctx.stroke();
                    }

                    const yPixel = y.getPixelForValue(meanAepf);
                    if (yPixel >= top && yPixel <= bottom) {
                        ctx.beginPath();
                        ctx.moveTo(left, yPixel);
                        ctx.lineTo(right, yPixel);
                        ctx.stroke();
                    }

                    // 2. Draw quadrant description labels & percentages in the corners
                    ctx.fillStyle = 'rgba(255, 255, 255, 0.55)';
                    ctx.font = 'bold 10px Outfit, sans-serif';
                    ctx.setLineDash([]); // clear dash for text

                    // Left-aligned labels (QII, QIII)
                    ctx.textAlign = 'left';
                    ctx.fillText('QII: High Force/Low Vel (' + pct2.toFixed(1) + '%)', left + 15, top + 20);
                    ctx.fillText('QIII: Low Force/Low Vel (' + pct3.toFixed(1) + '%)', left + 15, bottom - 15);

                    // Right-aligned labels (QI, QIV)
                    ctx.textAlign = 'right';
                    ctx.fillText('QI: High Force/Vel (' + pct1.toFixed(1) + '%)', right - 15, top + 20);
                    ctx.fillText('QIV: Low Force/High Vel (' + pct4.toFixed(1) + '%)', right - 15, bottom - 15);

                    ctx.restore();
                }
            };

            const ctxQuad = document.getElementById('chart-quadrant-analysis').getContext('2d');
            quadrantAnalysisChart = new Chart(ctxQuad, {
                type: 'scatter',
                data: {
                    datasets: [
                        {
                            label: 'QI (High Force/Vel)',
                            data: dsQ1,
                            backgroundColor: 'rgba(255, 139, 107, 0.5)',
                            borderColor: '#ff8b6b',
                            borderWidth: 1,
                            pointRadius: 2.5,
                            pointHoverRadius: 5
                        },
                        {
                            label: 'QII (High Force/Low Vel)',
                            data: dsQ2,
                            backgroundColor: 'rgba(241, 196, 15, 0.5)',
                            borderColor: '#f1c40f',
                            borderWidth: 1,
                            pointRadius: 2.5,
                            pointHoverRadius: 5
                        },
                        {
                            label: 'QIII (Low Force/Low Vel)',
                            data: dsQ3,
                            backgroundColor: 'rgba(52, 152, 219, 0.5)',
                            borderColor: '#3498db',
                            borderWidth: 1,
                            pointRadius: 2.5,
                            pointHoverRadius: 5
                        },
                        {
                            label: 'QIV (Low Force/High Vel)',
                            data: dsQ4,
                            backgroundColor: 'rgba(46, 204, 113, 0.5)',
                            borderColor: '#2ecc71',
                            borderWidth: 1,
                            pointRadius: 2.5,
                            pointHoverRadius: 5
                        }
                    ]
                },
                options: {
                    responsive: true,
                    maintainAspectRatio: false,
                    plugins: {
                        legend: {
                            display: true,
                            position: 'top',
                            labels: {
                                color: '#94a3b8',
                                font: { family: 'Outfit', size: 9 },
                                boxWidth: 6,
                                usePointStyle: true
                            }
                        },
                        tooltip: {
                            backgroundColor: '#1b1b26',
                            titleFont: { family: 'Outfit' },
                            bodyFont: { family: 'Outfit' },
                            callbacks: {
                                label: function(context) {
                                    return 'Velocity: ' + context.parsed.x.toFixed(2) + ' m/s | Force: ' + Math.round(context.parsed.y) + ' N';
                                }
                            }
                        }
                    },
                    scales: {
                        x: {
                            min: 0,
                            max: xMaxVal,
                            grid: { color: 'rgba(255, 255, 255, 0.02)' },
                            ticks: { color: '#94a3b8', font: { family: 'Outfit', size: 10 } },
                            title: { display: true, text: 'Circumferential Pedal Velocity (m/s)', color: '#94a3b8', font: { family: 'Outfit', size: 10 } }
                        },
                        y: {
                            min: 0,
                            max: yMaxVal,
                            grid: { color: 'rgba(255, 255, 255, 0.02)' },
                            ticks: { color: '#94a3b8', font: { family: 'Outfit', size: 10 } },
                            title: { display: true, text: 'Average Effective Pedal Force (Newtons)', color: '#94a3b8', font: { family: 'Outfit', size: 10 } }
                        }
                    }
                },
                plugins: [quadrantPlugin]
            });
        }

        // ==========================================
        // FTP Estimation & Configuration
        // ==========================================
        const estimateFTPForRide = (ride) => {
            if (ride.twenty_min_power && ride.twenty_min_power > 0) {
                return {
                    val: Math.round(ride.twenty_min_power * 0.95),
                    method: '95% of 20-min power (' + ride.twenty_min_power + 'W)'
                };
            } else if (ride.power_curve && ride.power_curve["20m"] && ride.power_curve["20m"] > 0) {
                return {
                    val: Math.round(ride.power_curve["20m"] * 0.95),
                    method: '95% of 20-min power (' + ride.power_curve["20m"] + 'W)'
                };
            } else if (ride.np && ride.np > 0) {
                return {
                    val: Math.round(ride.np * 0.95),
                    method: '95% of NP (' + ride.np + 'W)'
                };
            } else if (ride.avg_power && ride.avg_power > 0) {
                return {
                    val: Math.round(ride.avg_power * 1.05),
                    method: 'Avg Power + 5% (' + ride.avg_power + 'W)'
                };
            }
            return null;
        };

        const renderFtpEstimates = () => {
            // 1. Current Ride Estimate
            const currentEst = estimateFTPForRide({
                power_curve: rideData.summary.power_curve,
                np: rideData.summary.normalized_power,
                avg_power: rideData.summary.average_power
            });

            const currentEstValEl = document.getElementById('ftp-current-estimate-val');
            const currentEstMethodEl = document.getElementById('ftp-current-estimate-method');
            const btnApplyCurrentFtp = document.getElementById('btn-apply-current-ftp');

            if (currentEst) {
                if (currentEstValEl) currentEstValEl.innerText = currentEst.val + ' W';
                if (currentEstMethodEl) currentEstMethodEl.innerText = currentEst.method;
                if (btnApplyCurrentFtp) {
                    btnApplyCurrentFtp.style.display = 'inline-block';
                    btnApplyCurrentFtp.onclick = () => {
                        updateFTP(currentEst.val);
                    };
                }
            } else {
                if (currentEstValEl) currentEstValEl.innerText = '-';
                if (currentEstMethodEl) currentEstMethodEl.innerText = 'No power data available';
                if (btnApplyCurrentFtp) btnApplyCurrentFtp.style.display = 'none';
            }

            // 2. Historical Estimates
            const ftpHistoryList = document.getElementById('ftp-history-list');
            if (ftpHistoryList) {
                const historyData = clientStorage.getItem('fit_ride_history');
                let history = [];
                if (historyData) {
                    try {
                        history = JSON.parse(historyData);
                    } catch (e) {
                        console.error("Error parsing history:", e);
                    }
                }

                if (history.length === 0) {
                    ftpHistoryList.innerHTML = '<div style="font-style: italic; color: var(--text-secondary); font-size: 0.75rem; text-align: center; padding-top: 1rem;">No historical rides analyzed yet. Your training history estimates will appear here.</div>';
                } else {
                    const sortedHistory = [...history].reverse();
                    ftpHistoryList.innerHTML = sortedHistory.map(ride => {
                        const est = estimateFTPForRide(ride);
                        if (!est) return '';

                        const isActive = est.val === athleteFTP;
                        const activeStyle = isActive ? 'border-color: var(--accent); background: rgba(155, 89, 182, 0.1);' : '';
                        const buttonText = isActive ? 'Active' : 'Apply';
                        const buttonColor = isActive ? 'color: var(--accent); border-color: var(--accent);' : '';
                        const disabledAttr = isActive ? 'disabled' : '';

                        return '<div style="display: flex; justify-content: space-between; align-items: center; background: rgba(0,0,0,0.15); border: 1px solid var(--border-color); border-radius: 8px; padding: 0.5rem 0.75rem; font-size: 0.75rem; ' + activeStyle + '">' +
                            '<div>' +
                                '<div style="font-weight: 600; color: #ffffff;">📅 ' + ride.date + ' (' + est.val + ' W)</div>' +
                                '<div style="font-size: 0.68rem; color: var(--text-secondary);">' + est.method + ' | ' + ride.distance_km + ' km</div>' +
                            '</div>' +
                            '<button class="btn-action" onclick="updateFTP(' + est.val + ')" style="font-size: 0.7rem; padding: 0.2rem 0.5rem; ' + buttonColor + '" ' + disabledAttr + '>' + buttonText + '</button>' +
                        '</div>';
                    }).join('');
                }
            }
        };

        const updateFTP = (newFTP) => {
            athleteFTP = parseInt(newFTP);
            if (isNaN(athleteFTP) || athleteFTP <= 0) athleteFTP = 250;
            
            clientStorage.setItem('fit_athlete_ftp', athleteFTP);
            
            const ftpInput = document.getElementById('ftp-input');
            if (ftpInput) {
                ftpInput.value = athleteFTP;
            }

            if (window.updateIFDisplay) window.updateIFDisplay();
            if (window.renderZones) window.renderZones();
            renderFtpEstimates();
        };

        // Expose updateFTP globally for inline onclick buttons
        window.updateFTP = updateFTP;

        const updateMaxHR = (newMaxHR) => {
            athleteMaxHR = parseInt(newMaxHR);
            if (isNaN(athleteMaxHR) || athleteMaxHR <= 0) athleteMaxHR = 190;
            
            clientStorage.setItem('fit_athlete_max_hr', athleteMaxHR);
            
            const maxHrInput = document.getElementById('max-hr-input');
            if (maxHrInput) {
                maxHrInput.value = athleteMaxHR;
            }

            if (window.renderZones) window.renderZones();
        };
        window.updateMaxHR = updateMaxHR;

        const viewRideAnalysis = (rideId) => {
            if (!rideId) return;

            // 1. Check history for this ride to get metadata
            const historyData = clientStorage.getItem('fit_ride_history');
            if (historyData) {
                try {
                    const history = JSON.parse(historyData);
                    const ride = history.find(r => r.id === rideId);
                    if (ride && ride.source && ride.param) {
                        const bikeSelector = document.getElementById('bike-selector');
                        if (bikeSelector) {
                            bikeSelector.value = ride.bike || '';
                        }
                        loadRideData(ride.source, ride.param, ride.param2 || '');
                        showDashboardView();
                        return;
                    }
                } catch (e) {
                    console.error("Error checking history in viewRideAnalysis:", e);
                }
            }

            // 2. Fallback: Try to resolve using rideId (start_time) and window.allRidesData
            if (window.allRidesData) {
                const targetTime = new Date(rideId).getTime();
                if (!isNaN(targetTime)) {
                    // Check local rides by parsing their filename dates
                    if (window.allRidesData.local) {
                        const match = window.allRidesData.local.find(file => {
                            if (!file.filename) return false;
                            const parts = file.filename.match(/^(\d{4})[-_](\d{2})[-_](\d{2})[-_](\d{2})[-_](\d{2})[-_](\d{2})/);
                            if (parts) {
                                const fileTime = new Date(parseInt(parts[1]), parseInt(parts[2]) - 1, parseInt(parts[3]), parseInt(parts[4]), parseInt(parts[5]), parseInt(parts[6])).getTime();
                                return !isNaN(fileTime) && Math.abs(fileTime - targetTime) <= 300000;
                            }
                            return false;
                        });
                        if (match) {
                            loadRideData('local', match.filename, '');
                            showDashboardView();
                            return;
                        }
                    }
                    // Check Hammerhead rides
                    if (window.allRidesData.hammerhead) {
                        const match = window.allRidesData.hammerhead.find(act => {
                            if (!act.startTime) return false;
                            const actTime = new Date(act.startTime).getTime();
                            return !isNaN(actTime) && Math.abs(actTime - targetTime) <= 300000; // 5 minutes window
                        });
                        if (match) {
                            loadRideData('hammerhead', match.id, '');
                            showDashboardView();
                            return;
                        }
                    }
                    // Check Wahoo rides
                    if (window.allRidesData.wahoo) {
                        const match = window.allRidesData.wahoo.find(act => {
                            if (!act.starts) return false;
                            const actTime = new Date(act.starts).getTime();
                            return !isNaN(actTime) && Math.abs(actTime - targetTime) <= 300000; // 5 minutes window
                        });
                        if (match) {
                            loadRideData('wahoo', match.id, match.file ? match.file.url : '');
                            showDashboardView();
                            return;
                        }
                    }
                }
            }

            if (window.initialRideData) {
                renderDashboard(window.initialRideData);
            }
            showDashboardView();
        };
        window.viewRideAnalysis = viewRideAnalysis;

        // Global View Router
        const switchToView = (viewName) => {
            // Update active view tracking in localStorage
            clientStorage.setItem('directeur_active_view', viewName);

            // Hide all view containers
            const views = ['landing-view', 'dashboard-view', 'calendar-view', 'settings-view', 'data-view'];
            views.forEach(v => {
                const el = document.getElementById(v);
                if (el) el.style.display = 'none';
            });

            // Deactivate all navigation tabs
            const tabs = ['nav-tab-landing', 'nav-tab-dashboard', 'nav-tab-calendar', 'nav-tab-settings', 'nav-tab-data'];
            tabs.forEach(t => {
                const el = document.getElementById(t);
                if (el) el.classList.remove('active');
            });

            // Show selected view container and activate its tab
            const targetView = viewName + '-view';
            const targetTab = 'nav-tab-' + viewName;

            const viewEl = document.getElementById(targetView);
            if (viewEl) {
                if (viewName === 'landing') {
                    viewEl.style.display = 'flex'; // Landing/Home layout is flex
                } else {
                    viewEl.style.display = 'block'; // All others are block
                }
            }

            const tabEl = document.getElementById(targetTab);
            if (tabEl) tabEl.classList.add('active');

            // Trigger specific view loading/rendering side-effects
            if (viewName === 'calendar') {
                loadCalendarViewDetails(); // load calendar constraints and history lists
            } else if (viewName === 'landing') {
                renderUnifiedLandingCalendar(); // render the unified home calendar
            } else if (viewName === 'settings') {
                populateSettingsView(); // load settings inputs
            } else if (viewName === 'data') {
                populateDataView(); // load JSON telemetry previews
            } else if (viewName === 'dashboard') {
                window.dispatchEvent(new Event('resize'));
                if (leafletMap) {
                    leafletMap.invalidateSize();
                    if (routePolyline) {
                        leafletMap.fitBounds(routePolyline.getBounds(), { padding: [20, 20] });
                    }
                }
            }

            // Clean query params on view change to maintain a clean URL bar
            const url = new URL(window.location.origin + window.location.pathname);
            const bike = new URL(window.location.href).searchParams.get('bike');
            if (bike) {
                url.searchParams.set('bike', bike);
            }
            window.history.pushState({}, '', url.toString());
        };
        window.switchToView = switchToView;

        // Define helpers globally in scope
        const getSynthesizedWeek = (weekIndex) => {
            const todayMonday = getMonday(new Date());
            const displayStartDate = new Date(todayMonday);
            displayStartDate.setDate(todayMonday.getDate() + (weekIndex * 7));
            const displayStartStr = formatLocalDateKey(displayStartDate);

            let plansByDate = {};
            try {
                const plansByDateData = clientStorage.getItem('fit_training_plans_by_date');
                if (plansByDateData) plansByDate = JSON.parse(plansByDateData);
            } catch(e) {}

            let weeklySummaries = {};
            try {
                const summariesData = clientStorage.getItem('fit_weekly_summaries');
                if (summariesData) weeklySummaries = JSON.parse(summariesData);
            } catch(e) {}

            const synthesizedDays = [];
            for (let i = 0; i < 7; i++) {
                const dayDate = new Date(displayStartDate);
                dayDate.setDate(displayStartDate.getDate() + i);
                const dateKey = formatLocalDateKey(dayDate);
                const dayPlan = plansByDate[dateKey];
                if (dayPlan) {
                    dayPlan.date_key = dateKey;
                    synthesizedDays.push(dayPlan);
                } else {
                    synthesizedDays.push({
                        day: dayDate.toLocaleDateString('en-US', { weekday: 'long' }),
                        date_key: dateKey,
                        workout_type: "No Plan",
                        title: "No Plan",
                        duration_mins: 0,
                        target_tss: 0,
                        target_if: 0,
                        description: "No training plan focus generated for this day.",
                        is_fallback: true
                    });
                }
            }

            return {
                start_date: formatLocalDateKey(displayStartDate),
                weekly_summary: weeklySummaries[displayStartStr] || "No training plan focus for this week. Use the Planner Configuration on the left to generate a training plan!",
                days: synthesizedDays
            };
        };
        window.getSynthesizedWeek = getSynthesizedWeek;

        const getWeeksWithPlans = () => {
            let plansByDate = {};
            try {
                const plansByDateData = clientStorage.getItem('fit_training_plans_by_date');
                if (plansByDateData) plansByDate = JSON.parse(plansByDateData);
            } catch(e) {}

            let weeklySummaries = {};
            try {
                const summariesData = clientStorage.getItem('fit_weekly_summaries');
                if (summariesData) weeklySummaries = JSON.parse(summariesData);
            } catch(e) {}

            const weeksMap = {};
            Object.keys(plansByDate).forEach(dateStr => {
                const dayPlan = plansByDate[dateStr];
                if (dayPlan && dayPlan.workout_type && dayPlan.workout_type !== "Rest Day") {
                    const monday = getMonday(dateStr);
                    const mondayStr = formatLocalDateKey(monday);
                    weeksMap[mondayStr] = true;
                }
            });

            Object.keys(weeklySummaries).forEach(mondayStr => {
                weeksMap[mondayStr] = true;
            });

            const sortedMondays = Object.keys(weeksMap).sort((a, b) => new Date(b).getTime() - new Date(a).getTime());
            return sortedMondays.map(mondayStr => {
                return {
                    start_date: mondayStr,
                    weekly_summary: weeklySummaries[mondayStr] || "Training plan for this week."
                };
            });
        };
        window.getWeeksWithPlans = getWeeksWithPlans;

        // Migration logic moved to DOMContentLoaded initialization

        // Legacy compatibility wrapper functions
        const showLandingView = () => switchToView('landing');
        window.showLandingView = showLandingView;

        const showCalendarView = () => switchToView('calendar');
        window.showCalendarView = showCalendarView;

        // Calendar Loader Helper
        const loadCalendarViewDetails = () => {
            // Run self-heal on loaded plansByDate to correct any key/day-label date mismatches
            try {
                const plansByDateData = clientStorage.getItem('fit_training_plans_by_date');
                if (plansByDateData) {
                    const plans = JSON.parse(plansByDateData);
                    const moves = [];
                    Object.keys(plans).forEach(key => {
                        const dayPlan = plans[key];
                        if (dayPlan && dayPlan.day) {
                            const dayStr = dayPlan.day;
                            if (dayStr.includes(',') && (dayStr.match(/\d{4}/) || dayStr.match(/[A-Za-z]{3}\s+\d+/))) {
                                try {
                                    const parsedDate = new Date(dayStr);
                                    if (!isNaN(parsedDate.getTime())) {
                                        const correctKey = formatLocalDateKey(parsedDate);
                                        if (correctKey !== key) {
                                            moves.push({ from: key, to: correctKey, value: dayPlan });
                                        }
                                    }
                                } catch(e) {}
                            }
                        }
                    });
                    if (moves.length > 0) {
                        moves.forEach(m => {
                            delete plans[m.from];
                            plans[m.to] = m.value;
                            console.log("Self-healed shifted key: " + m.from + " -> " + m.to + " (from \"" + m.value.day + "\")");
                        });
                        clientStorage.setItem('fit_training_plans_by_date', JSON.stringify(plans));
                    }
                }
            } catch(e) {
                console.error("Self-heal error:", e);
            }

            // Load custom inputs from local storage if saved
            const savedGoals = clientStorage.getItem('fit_calendar_goals');
            if (savedGoals) {
                document.getElementById('calendar-goals-input').value = savedGoals;
            }
            const savedConstraints = clientStorage.getItem('fit_calendar_constraints');
            if (savedConstraints) {
                document.getElementById('calendar-constraints-input').value = savedConstraints;
            }
            const savedModel = clientStorage.getItem('fit_calendar_model');
            if (savedModel) {
                document.getElementById('calendar-model-select').value = savedModel;
            }
            const savedWeeks = clientStorage.getItem('fit_calendar_weeks');
            if (savedWeeks) {
                document.getElementById('calendar-weeks-select').value = savedWeeks;
            }

            // Sync plannerCalendarWeekIndex to the week of the current day (offset 0)
            window.plannerCalendarWeekIndex = 0;

            const synthesizedWeek = getSynthesizedWeek(window.plannerCalendarWeekIndex);
            window.currentCalendarProgram = synthesizedWeek;
            renderTrainingCalendar(synthesizedWeek);

            updateIntervalsSyncUI();
            renderPlannerHistory();
        };

        // Unified Landing & Planner Calendar Week Navigation Offsets
        let landingCalendarWeekIndex = 0; // Tracks offsets or matches in historyList
        window.plannerCalendarWeekIndex = 0; // Tracks planner view week navigation offsets

        const renderUnifiedLandingCalendar = () => {
            const grid = document.getElementById('landing-calendar-grid');
            const weekLabel = document.getElementById('landing-calendar-week-label');
            const summaryContent = document.getElementById('landing-plan-summary-content');
            const recentList = document.getElementById('landing-recent-activity-list');
            if (!grid) return;

            // 1. Calculate displayStartDate (Monday of the displayed week)
            const today = new Date();
            let displayStartDate = new Date(today);
            displayStartDate.setDate(today.getDate() + (landingCalendarWeekIndex * 7));
            displayStartDate = getMonday(displayStartDate);

            const data = getSynthesizedWeek(landingCalendarWeekIndex);
            const hasPlan = data && data.days && data.days.some(d => d.workout_type && d.workout_type !== "Rest Day");

            // Render weekly summary widget
            if (hasPlan && data.weekly_summary && !data.weekly_summary.startsWith("No training plan focus")) {
                const mondayLabelDate = getMonday(data.start_date);
                summaryContent.innerHTML = '<div style="font-weight: 600; color: #ffffff; margin-bottom: 0.5rem;">Week starting ' + mondayLabelDate.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' }) + ':</div>' +
                    '<div style="font-size: 0.85rem; color: var(--text-secondary);">' + data.weekly_summary + '</div>';
            } else {
                summaryContent.innerHTML = 'No active training plan focus. Go to the <a href="#" onclick="event.preventDefault(); switchToView(\'calendar\');" style="color: var(--accent); font-weight: 600; text-decoration: none;">Training Planner</a> to generate one!';
            }

            // Load completed ride history for match-ups
            const historyData = clientStorage.getItem('fit_ride_history');
            let completedRidesHistory = [];
            if (historyData) {
                try {
                    completedRidesHistory = JSON.parse(historyData);
                } catch(e){}
            }

            // Render Recent Rides list widget on Home Page
            if (completedRidesHistory.length === 0) {
                recentList.innerHTML = '<div style="font-style: italic; color: var(--text-secondary); font-size: 0.8rem; text-align: center; padding-top: 1rem;">No analyzed rides found.</div>';
            } else {
                const sortedRecent = [...completedRidesHistory].reverse().slice(0, 5);
                recentList.innerHTML = sortedRecent.map(ride => {
                    return '<div onclick="window.viewRideAnalysis(\'' + ride.id + '\')" style="display: flex; justify-content: space-between; align-items: center; background: rgba(255,255,255,0.03); border: 1px solid rgba(255,255,255,0.05); border-radius: 8px; padding: 0.5rem 0.75rem; font-size: 0.78rem; cursor: pointer; transition: all 0.2s;" onmouseover="this.style.background=\'rgba(255,255,255,0.08)\'; this.style.borderColor=\'var(--accent)\'" onmouseout="this.style.background=\'rgba(255,255,255,0.03)\'; this.style.borderColor=\'rgba(255,255,255,0.05)\'">' +
                        '<div>' +
                            '<div style="font-weight: 600; color: #ffffff;">📅 ' + ride.date + '</div>' +
                            '<div style="font-size: 0.7rem; color: var(--text-secondary);">' + ride.distance_km + ' km | NP: ' + ride.np + 'W</div>' +
                        '</div>' +
                        '<div style="font-size: 0.8rem; color: var(--accent); font-weight: 600;">➔</div>' +
                    '</div>';
                }).join('');
            }

            // Render calendar cards
            const mondayDate = displayStartDate;
            const endWeekDate = new Date(mondayDate);
            endWeekDate.setDate(mondayDate.getDate() + 6);
            weekLabel.innerText = "Week of " + mondayDate.toLocaleDateString('en-US', { month: 'short', day: 'numeric' }) + " – " + endWeekDate.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });

            const weekdays = ['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday'];

            let gridHTML = '';
            weekdays.forEach((dayName, idx) => {
                const dayDate = new Date(mondayDate);
                dayDate.setDate(mondayDate.getDate() + idx);
                const dateKey = formatLocalDateKey(dayDate);

                const d = data.days[idx];

                // Match completed rides
                const dYear = dayDate.getFullYear();
                const dMonth = dayDate.getMonth();
                const dDay = dayDate.getDate();
                const dayCompleted = completedRidesHistory.filter(ride => {
                    const rDate = new Date(ride.id);
                    return rDate.getFullYear() === dYear && rDate.getMonth() === dMonth && rDate.getDate() === dDay;
                });

                const isToday = dayDate.toDateString() === new Date().toDateString();
                const cardBorder = isToday ? 'border-color: var(--accent); background: rgba(255, 107, 107, 0.05);' : 'border-color: var(--border-color);';

                const workoutType = (d.workout_type || '').toLowerCase();
                const isPlanned = d.workout_type && !workoutType.includes('rest') && !workoutType.includes('no plan');

                let workoutBadgeColor = 'background: rgba(255,255,255,0.06); color: var(--text-secondary);';
                if (isPlanned) {
                    if (workoutType.includes('endurance') || workoutType.includes('aerobic')) {
                        workoutBadgeColor = 'background: rgba(52, 152, 219, 0.1); color: #3498db; border: 1px solid rgba(52, 152, 219, 0.2);';
                    } else if (workoutType.includes('sweet spot') || workoutType.includes('tempo')) {
                        workoutBadgeColor = 'background: rgba(241, 196, 15, 0.1); color: #f1c40f; border: 1px solid rgba(241, 196, 15, 0.2);';
                    } else if (workoutType.includes('threshold') || workoutType.includes('intervals')) {
                        workoutBadgeColor = 'background: rgba(230, 126, 34, 0.1); color: #e67e22; border: 1px solid rgba(230, 126, 34, 0.2);';
                    } else if (workoutType.includes('vo2') || workoutType.includes('anaerobic')) {
                        workoutBadgeColor = 'background: rgba(155, 89, 182, 0.1); color: #9b59b6; border: 1px solid rgba(155, 89, 182, 0.2);';
                    } else if (workoutType.includes('recovery')) {
                        workoutBadgeColor = 'background: rgba(46, 204, 113, 0.1); color: #2ecc71; border: 1px solid rgba(46, 204, 113, 0.2);';
                    }
                }

                // Render planned workout segment
                let workoutPlannedHTML = '';
                if (isPlanned) {
                    const workoutTitle = d.title || 'Workout';
                    const workoutDuration = d.duration_mins ? d.duration_mins + 'm' : '';
                    workoutPlannedHTML = 
                        '<div style="margin-top: 0.5rem; padding: 0.4rem; border-radius: 6px; ' + workoutBadgeColor + ' font-size: 0.72rem; font-weight: 500;">' +
                            '<div style="font-weight: 700; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;" title="' + workoutTitle + '">📋 ' + workoutTitle + '</div>' +
                            (workoutDuration ? '<div style="font-size: 0.65rem; opacity: 0.8; margin-top: 0.15rem;">Target: ' + workoutDuration + '</div>' : '') +
                        '</div>';
                } else {
                    const statusText = d.is_fallback ? 'No Plan' : 'Rest Day';
                    workoutPlannedHTML = '<div style="font-size: 0.65rem; color: var(--text-secondary); font-style: italic; margin-top: 0.5rem; text-align: center;">' + statusText + '</div>';
                }

                // Render completed actual rides segment
                let completedHTML = '';
                if (dayCompleted.length > 0) {
                    completedHTML = dayCompleted.map(ride => 
                        '<div onclick="window.viewRideAnalysis(\'' + ride.id + '\')" style="background: rgba(46, 204, 113, 0.15); border: 1px solid #2ecc71; border-radius: 6px; padding: 0.4rem; font-size: 0.72rem; color: #ffffff; cursor: pointer; text-align: center; margin-top: 0.5rem; transition: transform 0.15s;" onmouseover="this.style.transform=\'scale(1.03)\'" onmouseout="this.style.transform=\'scale(1)\'">' +
                            '<div style="color: #2ecc71; font-weight: 700;">✔ Completed</div>' +
                            '<div style="font-size: 0.68rem; opacity: 0.9;">' + ride.distance_km + ' km (' + ride.np + 'W)</div>' +
                        '</div>'
                    ).join('');
                } else if (!isPlanned) {
                    const statusText = d.is_fallback ? 'No Plan' : 'Rest Day';
                    completedHTML = '<div style="font-style: italic; color: #2ecc71; font-size: 0.7rem; text-align: center; margin-top: 0.75rem;">' + statusText + '</div>';
                } else {
                    completedHTML = '<div style="font-style: italic; color: var(--text-secondary); font-size: 0.7rem; text-align: center; margin-top: 0.75rem;">Pending</div>';
                }

                const displayDayLabel = dayName.charAt(0).toUpperCase() + dayName.slice(1);
                const formattedDateStr = dayDate.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });

                gridHTML += '<div class="card" style="padding: 0.75rem; ' + cardBorder + ' display: flex; flex-direction: column; justify-content: space-between; min-height: 180px;">' +
                    '<div>' +
                        '<div style="font-size: 0.8rem; font-weight: 700; color: #ffffff; text-align: center;">' + displayDayLabel + '</div>' +
                        '<div style="font-size: 0.7rem; color: var(--text-secondary); text-align: center; margin-top: 0.1rem;">' + formattedDateStr + '</div>' +
                        (isToday ? '<div style="font-size: 0.65rem; background: var(--accent); color: #ffffff; border-radius: 4px; padding: 0.1rem 0.25rem; font-weight: 700; text-transform: uppercase; width: max-content; margin: 0.25rem auto 0 auto;">Today</div>' : '') +
                        workoutPlannedHTML +
                    '</div>' +
                    '<div>' + completedHTML + '</div>' +
                '</div>';
            });

            grid.innerHTML = gridHTML;
        };
        window.renderUnifiedLandingCalendar = renderUnifiedLandingCalendar;

        const navigateLandingWeek = (direction) => {
            if (direction === 'prev') {
                landingCalendarWeekIndex--;
            } else {
                landingCalendarWeekIndex++;
            }
            renderUnifiedLandingCalendar();
        };
        window.navigateLandingWeek = navigateLandingWeek;

        const btnLandingPrev = document.getElementById('btn-landing-prev-week');
        const btnLandingNext = document.getElementById('btn-landing-next-week');
        if (btnLandingPrev) {
            btnLandingPrev.addEventListener('click', (e) => {
                e.stopPropagation();
                navigateLandingWeek('prev');
            });
        }
        if (btnLandingNext) {
            btnLandingNext.addEventListener('click', (e) => {
                e.stopPropagation();
                navigateLandingWeek('next');
            });
        }

        // Settings View Functions
        const populateSettingsView = () => {
            // FTP
            const ftpEl = document.getElementById('settings-ftp-input');
            if (ftpEl) ftpEl.value = athleteFTP;

            // Max HR
            const maxHrEl = document.getElementById('settings-max-hr-input');
            if (maxHrEl) maxHrEl.value = athleteMaxHR;

            // Gemini Key
            const apiKeyEl = document.getElementById('settings-api-key-input');
            if (apiKeyEl) apiKeyEl.value = clientStorage.getItem('gemini_api_key') || '';

            // Intervals.icu
            fetch('/api/intervals/config')
                .then(r => r.json())
                .then(data => {
                    const athleteIdEl = document.getElementById('settings-intervals-athlete-id');
                    if (athleteIdEl) athleteIdEl.value = data.athlete_id || '0';
                    
                    const enabledEl = document.getElementById('settings-intervals-enabled');
                    if (enabledEl) enabledEl.checked = data.enabled || false;
                    
                    const apiKeyEl = document.getElementById('settings-intervals-api-key');
                    if (apiKeyEl) apiKeyEl.value = '';
                })
                .catch(err => console.error("Error loading Intervals config:", err));

            // Default Bike
            const dashBike = document.getElementById('bike-selector');
            const settingsBike = document.getElementById('settings-bike-selector');
            if (dashBike && settingsBike) {
                settingsBike.innerHTML = dashBike.innerHTML;
                settingsBike.value = dashBike.value;
            }
        };
        window.populateSettingsView = populateSettingsView;

        const saveFTPFromSettings = () => {
            const val = parseInt(document.getElementById('settings-ftp-input').value);
            if (val && !isNaN(val) && val > 0) {
                updateFTP(val);
                alert('FTP updated to ' + val + 'W');
            } else {
                alert('Please enter a valid FTP number.');
            }
        };
        window.saveFTPFromSettings = saveFTPFromSettings;

        const saveMaxHRFromSettings = () => {
            const val = parseInt(document.getElementById('settings-max-hr-input').value);
            if (val && !isNaN(val) && val > 0) {
                updateMaxHR(val);
                alert('Max HR updated to ' + val + ' bpm');
            } else {
                alert('Please enter a valid Max HR number.');
            }
        };
        window.saveMaxHRFromSettings = saveMaxHRFromSettings;

        const saveAPIKeyFromSettings = () => {
            const key = document.getElementById('settings-api-key-input').value.trim();
            clientStorage.setItem('gemini_api_key', key);
            alert('Gemini API Key updated successfully!');
        };
        window.saveAPIKeyFromSettings = saveAPIKeyFromSettings;

        const clearAPIKeyFromSettings = () => {
            clientStorage.removeItem('gemini_api_key');
            document.getElementById('settings-api-key-input').value = '';
            alert('Gemini API Key cleared.');
        };
        window.clearAPIKeyFromSettings = clearAPIKeyFromSettings;

        const saveBikeFromSettings = (value) => {
            const dashBike = document.getElementById('bike-selector');
            if (dashBike) {
                dashBike.value = value;
                dashBike.dispatchEvent(new Event('change'));
            } else {
                if (value) {
                    clientStorage.setItem('directeur_selected_bike', value);
                } else {
                    clientStorage.removeItem('directeur_selected_bike');
                }
            }
        };
        window.saveBikeFromSettings = saveBikeFromSettings;

        const saveIntervalsFromSettings = () => {
            const athlete_id = document.getElementById('settings-intervals-athlete-id').value.trim();
            const api_key = document.getElementById('settings-intervals-api-key').value.trim();
            const enabled = document.getElementById('settings-intervals-enabled').checked;

            fetch('/api/intervals/config', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ athlete_id, api_key, enabled })
            })
            .then(r => r.json())
            .then(data => {
                if (data.status === 'success') {
                    alert('Intervals.icu settings saved successfully!');
                    updateIntervalsSyncUI();
                } else {
                    alert('Error saving settings: ' + data.message);
                }
            })
            .catch(err => alert('Network error: ' + err.message));
        };
        window.saveIntervalsFromSettings = saveIntervalsFromSettings;

        const testIntervalsFromSettings = () => {
            const statusEl = document.getElementById('settings-intervals-test-status');
            if (statusEl) {
                statusEl.style.display = 'block';
                statusEl.style.background = 'rgba(255,255,255,0.05)';
                statusEl.style.color = '#ffffff';
                statusEl.innerText = 'Testing connection...';
            }

            const athlete_id = document.getElementById('settings-intervals-athlete-id').value.trim();
            const api_key = document.getElementById('settings-intervals-api-key').value.trim();

            fetch('/api/intervals/test', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ athlete_id, api_key })
            })
            .then(r => r.json())
            .then(data => {
                if (statusEl) {
                    if (data.status === 'success') {
                        statusEl.style.background = 'rgba(46, 204, 113, 0.15)';
                        statusEl.style.color = '#2ecc71';
                        statusEl.innerText = '✔ Connection Successful! Athlete: ' + (data.name || athlete_id);
                    } else {
                        statusEl.style.background = 'rgba(231, 76, 60, 0.15)';
                        statusEl.style.color = '#ff6b6b';
                        statusEl.innerText = '❌ Connection Failed: ' + data.message;
                    }
                }
            })
            .catch(err => {
                if (statusEl) {
                    statusEl.style.background = 'rgba(231, 76, 60, 0.15)';
                    statusEl.style.color = '#ff6b6b';
                    statusEl.innerText = '❌ Error: ' + err.message;
                }
            });
        };
        window.testIntervalsFromSettings = testIntervalsFromSettings;

        // Data View Functions
        const populateDataView = () => {
            const textarea = document.getElementById('data-json-preview');
            if (!textarea) return;

            if (window.rideData) {
                textarea.value = JSON.stringify(window.rideData, null, 2);
            } else {
                textarea.value = 'No active ride telemetry loaded. Select a ride on the Rides page to view JSON.';
            }
        };
        window.populateDataView = populateDataView;

        const bindDataViewListeners = () => {
            const clearBtn = document.getElementById('data-clear-all-btn');
            if (clearBtn) {
                clearBtn.onclick = () => {
                    if (confirm("⚠️ WARNING: This will permanently wipe all local storage data, including your Gemini API key, ride history, default bike settings, and training programs. This cannot be undone!\n\nAre you sure you want to clear all data?")) {
                        clientStorage.clear();
                        alert("Local storage wiped successfully. Reloading...");
                        window.location.reload();
                    }
                };
            }

            const copyBtn = document.getElementById('data-copy-json-btn');
            if (copyBtn) {
                copyBtn.onclick = () => {
                    const text = document.getElementById('data-json-preview').value;
                    navigator.clipboard.writeText(text)
                        .then(() => alert('Ride JSON copied to clipboard!'))
                        .catch(err => alert('Failed to copy: ' + err));
                };
            }

            const downloadBtn = document.getElementById('data-download-json-btn');
            if (downloadBtn) {
                downloadBtn.onclick = () => {
                    const dlBtn = document.getElementById('btn-download-json');
                    if (dlBtn) dlBtn.click();
                };
            }

            const schemaBtn = document.getElementById('data-view-schema-btn');
            if (schemaBtn) {
                schemaBtn.onclick = () => {
                    const schBtn = document.getElementById('btn-view-schema');
                    if (schBtn) schBtn.click();
                };
            }
        };
        window.bindDataViewListeners = bindDataViewListeners;

        const getWeekOptionLabel = (dateStr) => {
            const d = new Date(dateStr);
            if (isNaN(d.getTime())) return "Unknown Week";
            return "Week of " + d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
        };

        const updateWeekSelector = (historyList, activeDateStr) => {
            const selectEl = document.getElementById('calendar-week-select');
            const navEl = document.getElementById('calendar-week-nav');
            if (!selectEl || !navEl) return;
            
            navEl.style.display = 'flex';
            selectEl.innerHTML = '';
            
            // Add history list items
            if (historyList && historyList.length > 0) {
                historyList.forEach(p => {
                    const opt = document.createElement('option');
                    opt.value = p.start_date;
                    opt.textContent = getWeekOptionLabel(p.start_date);
                    opt.style.background = 'var(--bg-secondary)';
                    opt.style.color = '#ffffff';
                    if (activeDateStr && p.start_date === activeDateStr) {
                        opt.selected = true;
                    }
                    selectEl.appendChild(opt);
                });
            }
            
            // If activeDateStr is not in historyList (or history is empty), add a temporary option
            const exists = historyList ? historyList.some(p => p.start_date === activeDateStr) : false;
            if (activeDateStr && !exists) {
                const opt = document.createElement('option');
                opt.value = activeDateStr;
                opt.textContent = getWeekOptionLabel(activeDateStr);
                opt.style.background = 'var(--bg-secondary)';
                opt.style.color = 'var(--text-secondary)';
                opt.selected = true;
                selectEl.appendChild(opt);
            } else if (!activeDateStr && (!historyList || historyList.length === 0)) {
                // Default fallback if everything is empty
                const opt = document.createElement('option');
                const todayMonday = getMonday(new Date());
                opt.value = formatLocalDateKey(todayMonday);
                opt.textContent = getWeekOptionLabel(todayMonday);
                opt.style.background = 'var(--bg-secondary)';
                opt.style.color = 'var(--text-secondary)';
                opt.selected = true;
                selectEl.appendChild(opt);
            }
        };

        const renderPlannerHistory = () => {
            const listEl = document.getElementById('calendar-history-list');
            if (!listEl) return;
            
            const weeksList = getWeeksWithPlans();
            
            if (weeksList.length === 0) {
                listEl.innerHTML = '<div style="color: var(--text-secondary); font-size: 0.85rem; text-align: center; padding: 1.5rem 0; font-style: italic;">No previous plans saved.</div>';
                return;
            }
            
            listEl.innerHTML = '';
            
            weeksList.forEach(p => {
                const isActive = window.currentCalendarProgram && window.currentCalendarProgram.start_date === p.start_date;
                
                const item = document.createElement('div');
                item.className = 'history-list-item';
                item.style.display = 'flex';
                item.style.justifyContent = 'space-between';
                item.style.alignItems = 'center';
                item.style.padding = '0.75rem';
                item.style.borderRadius = '8px';
                item.style.background = isActive ? 'rgba(255, 107, 107, 0.12)' : 'var(--bg-tertiary)';
                item.style.border = isActive ? '1px solid var(--accent)' : '1px solid var(--border-color)';
                item.style.cursor = 'pointer';
                item.style.transition = 'all 0.2s ease';
                item.style.marginBottom = '0.5rem';
                
                // Truncate summary for subtext
                const summary = p.weekly_summary ? (p.weekly_summary.length > 70 ? p.weekly_summary.substring(0, 67) + '...' : p.weekly_summary) : 'No focus summary.';
                
                const textSection = document.createElement('div');
                textSection.style.flex = '1';
                textSection.style.minWidth = '0';
                textSection.style.paddingRight = '0.5rem';
                textSection.innerHTML = 
                    '<div style="font-weight: 600; font-size: 0.85rem; color: ' + (isActive ? 'var(--accent)' : '#ffffff') + '; margin-bottom: 0.2rem;">' +
                        getWeekOptionLabel(p.start_date) +
                    '</div>' +
                    '<div style="font-size: 0.75rem; color: var(--text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">' +
                        summary +
                    '</div>';
                
                textSection.addEventListener('click', (e) => {
                    loadWeekFromSelect(p.start_date);
                });
                
                const deleteBtn = document.createElement('button');
                deleteBtn.innerHTML = '🗑️';
                deleteBtn.style.background = 'transparent';
                deleteBtn.style.border = 'none';
                deleteBtn.style.cursor = 'pointer';
                deleteBtn.style.fontSize = '0.85rem';
                deleteBtn.style.padding = '0.2rem';
                deleteBtn.style.opacity = '0.6';
                deleteBtn.style.transition = 'opacity 0.2s';
                deleteBtn.title = 'Delete Week';
                deleteBtn.addEventListener('mouseover', () => deleteBtn.style.opacity = '1');
                deleteBtn.addEventListener('mouseout', () => deleteBtn.style.opacity = '0.6');
                
                deleteBtn.addEventListener('click', (e) => {
                    e.stopPropagation();
                    if (confirm('Are you sure you want to delete the plan for ' + getWeekOptionLabel(p.start_date) + '?')) {
                        deleteProgramFromHistory(p.start_date);
                    }
                });
                
                item.appendChild(textSection);
                item.appendChild(deleteBtn);
                
                item.addEventListener('mouseenter', () => {
                    if (!isActive) {
                        item.style.borderColor = 'var(--text-secondary)';
                        item.style.background = 'rgba(255, 255, 255, 0.05)';
                    }
                });
                item.addEventListener('mouseleave', () => {
                    if (!isActive) {
                        item.style.borderColor = 'var(--border-color)';
                        item.style.background = 'var(--bg-tertiary)';
                    }
                });
                
                listEl.appendChild(item);
            });
        };
        window.renderPlannerHistory = renderPlannerHistory;

        const deleteProgramFromHistory = (startDateStr) => {
            try {
                const monday = getMonday(startDateStr);
                const mondayStr = formatLocalDateKey(monday);

                let plansByDate = {};
                try {
                    const plansByDateData = clientStorage.getItem('fit_training_plans_by_date');
                    if (plansByDateData) plansByDate = JSON.parse(plansByDateData);
                } catch(e) {}

                let weeklySummaries = {};
                try {
                    const summariesData = clientStorage.getItem('fit_weekly_summaries');
                    if (summariesData) weeklySummaries = JSON.parse(summariesData);
                } catch(e) {}

                // Delete the 7 days of this week
                for (let i = 0; i < 7; i++) {
                    const dayDate = new Date(monday);
                    dayDate.setDate(monday.getDate() + i);
                    const dateKey = formatLocalDateKey(dayDate);
                    delete plansByDate[dateKey];
                }

                // Delete the weekly summary
                delete weeklySummaries[mondayStr];

                clientStorage.setItem('fit_training_plans_by_date', JSON.stringify(plansByDate));
                clientStorage.setItem('fit_weekly_summaries', JSON.stringify(weeklySummaries));

                // Re-render
                const synthesizedWeek = getSynthesizedWeek(window.plannerCalendarWeekIndex);
                window.currentCalendarProgram = synthesizedWeek;
                renderTrainingCalendar(synthesizedWeek);
                renderPlannerHistory();
            } catch (e) {
                console.error("Failed to delete program:", e);
            }
        };
        window.deleteProgramFromHistory = deleteProgramFromHistory;

        const saveProgramToHistory = (program) => {
            if (!program || !program.start_date) return program;
            try {
                let plansByDate = {};
                try {
                    const plansByDateData = clientStorage.getItem('fit_training_plans_by_date');
                    if (plansByDateData) plansByDate = JSON.parse(plansByDateData);
                } catch(e) {}

                let weeklySummaries = {};
                try {
                    const summariesData = clientStorage.getItem('fit_weekly_summaries');
                    if (summariesData) weeklySummaries = JSON.parse(summariesData);
                } catch(e) {}

                const planStart = parseLocalDate(program.start_date);
                planStart.setHours(0, 0, 0, 0);

                const today = new Date();
                today.setHours(0, 0, 0, 0);

                // Save weekly summary anchored to Monday
                const monday = getMonday(planStart);
                const mondayStr = formatLocalDateKey(monday);
                if (program.weekly_summary) {
                    weeklySummaries[mondayStr] = program.weekly_summary;
                }

                // Process each day in the generated program
                const savedDays = [];
                for (let idx = 0; idx < 7; idx++) {
                    const dayDate = new Date(planStart);
                    dayDate.setDate(planStart.getDate() + idx);
                    dayDate.setHours(0, 0, 0, 0);
                    const key = formatLocalDateKey(dayDate);

                    const isFuture = dayDate.getTime() > today.getTime();
                    const existingDay = plansByDate[key];
                    let newDay = null;
                    if (program.days) {
                        newDay = program.days.find(d => d.date === key);
                        if (!newDay) {
                            newDay = program.days[idx];
                        }
                    }

                    let targetDay = newDay;
                    // If it is in the past, and there's an existing workout, preserve it
                    if (!isFuture && existingDay) {
                        targetDay = existingDay;
                    } else if (!newDay && existingDay) {
                        targetDay = existingDay;
                    }

                    if (targetDay) {
                        plansByDate[key] = targetDay;
                        savedDays.push(targetDay);
                    } else {
                        // Fallback if no targetDay exists
                        const fallback = {
                            day: dayDate.toLocaleDateString('en-US', { weekday: 'long' }),
                            workout_type: "Rest Day",
                            title: "Rest Day",
                            duration_mins: 0,
                            target_tss: 0,
                            target_if: 0,
                            description: "Rest Day"
                        };
                        plansByDate[key] = fallback;
                        savedDays.push(fallback);
                    }
                }

                clientStorage.setItem('fit_training_plans_by_date', JSON.stringify(plansByDate));
                clientStorage.setItem('fit_weekly_summaries', JSON.stringify(weeklySummaries));

                // Re-render
                renderPlannerHistory();
                program.days = savedDays;
            } catch (e) {
                console.error("Failed to save training program:", e);
            }
            return program;
        };

        const loadWeekFromSelect = (startDateStr) => {
            try {
                // Sync plannerCalendarWeekIndex
                const planStart = getMonday(startDateStr);
                const todayMonday = getMonday(new Date());
                const diffWeeks = Math.round((planStart.getTime() - todayMonday.getTime()) / (7 * 24 * 60 * 60 * 1000));
                window.plannerCalendarWeekIndex = diffWeeks;

                const synthesizedWeek = getSynthesizedWeek(window.plannerCalendarWeekIndex);
                window.currentCalendarProgram = synthesizedWeek;
                renderTrainingCalendar(synthesizedWeek);
            } catch(e) {
                console.error("Failed to load selected week:", e);
            }
        };
        window.loadWeekFromSelect = loadWeekFromSelect;

        const navigateWeek = (dir) => {
            if (dir === 'prev') {
                window.plannerCalendarWeekIndex--;
            } else if (dir === 'next') {
                window.plannerCalendarWeekIndex++;
            }
            
            const synthesizedWeek = getSynthesizedWeek(window.plannerCalendarWeekIndex);
            window.currentCalendarProgram = synthesizedWeek;
            renderTrainingCalendar(synthesizedWeek);
        };
        window.navigateWeek = navigateWeek;

        const renderTrainingCalendar = (data) => {
            let needsHistorySave = false;
            const grid = document.getElementById('calendar-grid');
            const summaryBox = document.getElementById('calendar-summary-box');
            const summaryText = document.getElementById('calendar-summary-text');
            const overviewBox = document.getElementById('calendar-overview-box');
            const overviewGrid = document.getElementById('calendar-overview-grid');
            const emptyState = document.getElementById('calendar-empty-state');
            
            // Calculate displayed week starting Monday.
            // Anchor to the Monday of the current week so that each index step
            // is exactly 7 days regardless of today's day-of-week.
            const todayMonday = getMonday(new Date());
            let displayStartDate = new Date(todayMonday);
            if (typeof window.plannerCalendarWeekIndex !== 'undefined') {
                displayStartDate.setDate(todayMonday.getDate() + (window.plannerCalendarWeekIndex * 7));
            }

            // If data is null/undefined, build a placeholder plan object for the navigated week
            if (!data || !data.days) {
                data = {
                    start_date: formatLocalDateKey(displayStartDate),
                    weekly_summary: "No planned workouts for this week. Use the Planner Configuration on the left to generate a training plan!",
                    days: []
                };
            }

            window.currentCalendarProgram = data;

            emptyState.style.display = 'none';
            grid.style.display = 'flex';
            summaryText.innerText = data.weekly_summary || 'Weekly training plan focus.';
            summaryBox.style.display = 'block';
            overviewBox.style.display = 'block';

            updateWeekSelector(getWeeksWithPlans(), data.start_date);

            grid.innerHTML = '';
            overviewGrid.innerHTML = '';
            
            // Retrieve local ride history for matching completed rides
            const historyData = clientStorage.getItem('fit_ride_history');
            let history = [];
            if (historyData) {
                try {
                    history = JSON.parse(historyData);
                } catch (e) {
                    console.error("Error parsing ride history for calendar:", e);
                }
            }

            const mondayDate = getMonday(data.start_date);
            const planStartDate = parseLocalDate(data.start_date);
            planStartDate.setHours(0,0,0,0);

            const weekdays = ['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday'];

            weekdays.forEach((dayName, idx) => {
                const dayDate = new Date(mondayDate);
                dayDate.setDate(mondayDate.getDate() + idx);
                const dateKey = formatLocalDateKey(dayDate);
                const displayDayLabel = dayName.charAt(0).toUpperCase() + dayName.slice(1);
                const d = data.days[idx];

                let badgeColor = 'rgba(255,255,255,0.08)';
                let textColor = '#ffffff';
                let borderColor = 'rgba(255,255,255,0.15)';
                const type = (d.workout_type || '').toLowerCase();
                
                if (type.includes('rest') || type.includes('recovery')) {
                    badgeColor = 'rgba(46, 204, 113, 0.1)';
                    textColor = '#2ecc71';
                    borderColor = 'rgba(46, 204, 113, 0.25)';
                } else if (type.includes('endurance') || type.includes('aerobic')) {
                    badgeColor = 'rgba(52, 152, 219, 0.1)';
                    textColor = '#3498db';
                    borderColor = 'rgba(52, 152, 219, 0.25)';
                } else if (type.includes('sweet spot') || type.includes('tempo')) {
                    badgeColor = 'rgba(241, 196, 15, 0.1)';
                    textColor = '#f1c40f';
                    borderColor = 'rgba(241, 196, 15, 0.25)';
                } else if (type.includes('threshold') || type.includes('intervals')) {
                    badgeColor = 'rgba(230, 126, 34, 0.1)';
                    textColor = '#e67e22';
                    borderColor = 'rgba(230, 126, 34, 0.25)';
                } else if (type.includes('vo2') || type.includes('anaerobic')) {
                    badgeColor = 'rgba(155, 89, 182, 0.1)';
                    textColor = '#9b59b6';
                    borderColor = 'rgba(155, 89, 182, 0.25)';
                }

                let dateDisplay = '';
                let shortDateStr = '';
                shortDateStr = dayDate.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
                dateDisplay = '<span style="font-size: 0.85rem; color: var(--text-secondary); margin-top: -0.2rem; margin-bottom: 0.2rem; font-weight: 500;">' + shortDateStr + '</span>';

                // Check if there are analyzed rides matching this dayDate (local time comparison)
                let completedRides = [];
                const dYear = dayDate.getFullYear();
                const dMonth = dayDate.getMonth();
                const dDay = dayDate.getDate();
                
                completedRides = history.filter(ride => {
                    if (!ride.id) return false;
                    const rDate = new Date(ride.id);
                    return rDate.getFullYear() === dYear &&
                           rDate.getMonth() === dMonth &&
                           rDate.getDate() === dDay;
                });

                completedRides.forEach(completedRide => {
                    if (completedRide && (!completedRide.source || !completedRide.param)) {
                        const targetTime = new Date(completedRide.id).getTime();
                        if (!isNaN(targetTime)) {
                            // 1. Check local files
                            if (window.allRidesData && window.allRidesData.local) {
                                const match = window.allRidesData.local.find(file => {
                                    if (!file.filename) return false;
                                    const parts = file.filename.match(/^(\d{4})[-_](\d{2})[-_](\d{2})[-_](\d{2})[-_](\d{2})[-_](\d{2})/);
                                    if (parts) {
                                        const fileTime = new Date(parseInt(parts[1]), parseInt(parts[2]) - 1, parseInt(parts[3]), parseInt(parts[4]), parseInt(parts[5]), parseInt(parts[6])).getTime();
                                        return !isNaN(fileTime) && Math.abs(fileTime - targetTime) <= 300000;
                                    }
                                    return false;
                                });
                                if (match) {
                                    completedRide.source = 'local';
                                    completedRide.param = match.filename;
                                    needsHistorySave = true;
                                }
                            }
                            // 2. Check Hammerhead
                            if ((!completedRide.source || !completedRide.param) && window.allRidesData && window.allRidesData.hammerhead) {
                                const match = window.allRidesData.hammerhead.find(act => {
                                    if (!act.startTime) return false;
                                    const actTime = new Date(act.startTime).getTime();
                                    return !isNaN(actTime) && Math.abs(actTime - targetTime) <= 300000;
                                });
                                if (match) {
                                    completedRide.source = 'hammerhead';
                                    completedRide.param = match.id;
                                    needsHistorySave = true;
                                }
                            }
                            // 3. Check Wahoo
                            if ((!completedRide.source || !completedRide.param) && window.allRidesData && window.allRidesData.wahoo) {
                                const match = window.allRidesData.wahoo.find(act => {
                                    if (!act.starts) return false;
                                    const actTime = new Date(act.starts).getTime();
                                    return !isNaN(actTime) && Math.abs(actTime - targetTime) <= 300000;
                                });
                                if (match) {
                                    completedRide.source = 'wahoo';
                                    completedRide.param = match.id;
                                    completedRide.param2 = match.file ? match.file.url : '';
                                    needsHistorySave = true;
                                }
                            }
                        }
                    }
                });

                // Create the top bar overview capsule
                const overviewCard = document.createElement('div');
                overviewCard.style.background = 'var(--bg-tertiary)';
                overviewCard.style.border = '1px solid var(--border-color)';
                overviewCard.style.borderRadius = '8px';
                overviewCard.style.padding = '0.75rem';
                overviewCard.style.cursor = 'pointer';
                overviewCard.style.transition = 'all 0.2s ease-in-out';
                overviewCard.style.display = 'flex';
                overviewCard.style.flexDirection = 'column';
                overviewCard.style.gap = '0.35rem';
                overviewCard.style.minWidth = '100px';

                overviewCard.onmouseover = () => {
                    overviewCard.style.transform = 'translateY(-2px)';
                    overviewCard.style.borderColor = 'var(--accent)';
                    overviewCard.style.background = 'rgba(255,255,255,0.03)';
                };
                overviewCard.onmouseout = () => {
                    overviewCard.style.transform = 'translateY(0)';
                    overviewCard.style.borderColor = 'var(--border-color)';
                    overviewCard.style.background = 'var(--bg-tertiary)';
                };

                overviewCard.onclick = () => {
                    const targetRow = document.getElementById('calendar-day-row-' + dayName);
                    if (targetRow) {
                        targetRow.scrollIntoView({ behavior: 'smooth', block: 'center' });
                        targetRow.style.boxShadow = '0 0 20px var(--accent-glow)';
                        targetRow.style.borderColor = 'var(--accent)';
                        targetRow.style.transform = 'scale(1.01)';
                        targetRow.style.transition = 'all 0.4s ease';
                        setTimeout(() => {
                            targetRow.style.boxShadow = 'none';
                            targetRow.style.borderColor = 'var(--border-color)';
                            targetRow.style.transform = 'scale(1)';
                        }, 1500);
                    }
                };

                const titleText = d.title || 'Workout';
                const durationText = d.is_fallback ? 'No Plan' : (d.duration_mins ? d.duration_mins + ' mins' : 'Rest Day');
                const completionBadge = completedRides.length > 0 
                    ? '<span class="badge" style="background: rgba(46, 204, 113, 0.15); color: #2ecc71; border: 1px solid rgba(46, 204, 113, 0.3); font-size: 0.65rem; font-weight: bold; border-radius: 4px; padding: 0.05rem 0.2rem; text-transform: uppercase; margin-top: 0.1rem; width: fit-content; display: inline-flex; align-items: center; gap: 0.15rem;">✓ Complete</span>'
                    : '';

                overviewCard.innerHTML = 
                    '<div style="display: flex; justify-content: space-between; align-items: baseline; gap: 0.25rem;">' +
                        '<strong style="font-size: 0.85rem; color: #ffffff; font-family: \'Outfit\';">' + displayDayLabel.substring(0, 3) + '</strong>' +
                        '<span style="font-size: 0.75rem; color: var(--text-secondary);">' + shortDateStr + '</span>' +
                    '</div>' +
                    '<div style="display: flex; gap: 0.25rem; flex-wrap: wrap; margin-bottom: 0.1rem;">' +
                        '<span class="badge" style="background: ' + badgeColor + '; color: ' + textColor + '; border: 1px solid ' + borderColor + '; font-size: 0.65rem; text-align: center; border-radius: 4px; padding: 0.05rem 0.25rem; text-transform: uppercase; width: fit-content; font-weight: 600; font-family: var(--font-family);">' + (type.includes('rest') ? 'REST' : d.workout_type) + '</span>' +
                        completionBadge +
                    '</div>' +
                    '<div style="font-size: 0.75rem; font-weight: 500; color: #ffffff; text-overflow: ellipsis; white-space: nowrap; overflow: hidden; margin-top: 0.1rem;" title="' + titleText + '">' + titleText + '</div>' +
                    '<div style="font-size: 0.7rem; color: var(--text-secondary);">' + durationText + '</div>';

                overviewGrid.appendChild(overviewCard);

                // Create the detailed Day Row Card
                const row = document.createElement('div');
                row.id = 'calendar-day-row-' + dayName;
                row.className = 'calendar-day-row';
                row.style.display = 'flex';
                row.style.gap = '1.5rem';
                row.style.background = 'var(--bg-secondary)';
                row.style.border = '1px solid var(--border-color)';
                row.style.borderRadius = '12px';
                row.style.padding = '1.25rem';
                row.style.alignItems = 'start';
                row.style.transition = 'all 0.3s ease';

                const completionBadgeRow = completedRides.length > 0
                    ? (completedRides.length > 1
                        ? '<span class="badge" style="background: rgba(46, 204, 113, 0.15); color: #2ecc71; border: 1px solid rgba(46, 204, 113, 0.3); font-size: 0.75rem; text-align: center; border-radius: 4px; padding: 0.15rem 0.4rem; text-transform: uppercase; width: fit-content; font-weight: 700; display: inline-flex; align-items: center; gap: 0.2rem; margin-top: 0.25rem;">✓ ' + completedRides.length + ' Complete</span>'
                        : '<span class="badge" style="background: rgba(46, 204, 113, 0.15); color: #2ecc71; border: 1px solid rgba(46, 204, 113, 0.3); font-size: 0.75rem; text-align: center; border-radius: 4px; padding: 0.15rem 0.4rem; text-transform: uppercase; width: fit-content; font-weight: 700; display: inline-flex; align-items: center; gap: 0.2rem; margin-top: 0.25rem;">✓ Complete</span>')
                    : '';

                let analysisLinkHtml = '';
                if (completedRides.length === 1) {
                    const completedRide = completedRides[0];
                    const rideUrl = getRideQueryString(completedRide.source || 'local', completedRide.param || '', completedRide.param2 || '');
                    analysisLinkHtml = '<div style="margin-top: 0.75rem; display: flex; align-items: center;">' +
                        '<a href="' + rideUrl + '" class="view-analysis-link" style="color: var(--accent); text-decoration: none; font-size: 0.8rem; font-weight: 600; display: inline-flex; align-items: center; gap: 0.35rem; border: 1px solid var(--accent); border-radius: 6px; padding: 0.4rem 0.8rem; background: rgba(228, 92, 134, 0.05); transition: all 0.2s;" onmouseover="this.style.background=\'rgba(228, 92, 134, 0.15)\'; this.style.transform=\'translateY(-1px)\'" onmouseout="this.style.background=\'rgba(228, 92, 134, 0.05)\'; this.style.transform=\'translateY(0)\'" onclick="event.preventDefault(); window.viewRideAnalysis(\'' + completedRide.id + '\')">' +
                            '📊 View Ride Analysis (' + completedRide.distance_km + ' km)' +
                        '</a>' +
                    '</div>';
                } else if (completedRides.length > 1) {
                    let optionsHtml = '<option value="" disabled selected style="background: var(--bg-secondary); color: var(--text-secondary);">Select completed ride...</option>';
                    completedRides.forEach((ride, rIdx) => {
                        optionsHtml += '<option value="' + ride.id + '" style="background: var(--bg-secondary); color: #ffffff;">' +
                            'Ride ' + (rIdx + 1) + ': ' + ride.distance_km + ' km (' + ride.duration + ')' +
                        '</option>';
                    });

                    analysisLinkHtml = '<div style="margin-top: 0.75rem; display: flex; flex-direction: column; gap: 0.35rem;">' +
                        '<label style="font-size: 0.75rem; color: var(--text-secondary); font-weight: 500;">Multiple rides completed on this day:</label>' +
                        '<select class="ride-select-dropdown" style="background: rgba(255,255,255,0.03); border: 1px solid var(--border-color); color: #ffffff; border-radius: 6px; padding: 0.4rem 0.6rem; font-size: 0.8rem; font-weight: 600; cursor: pointer; outline: none; width: fit-content; transition: all 0.2s;" onchange="if(this.value) { window.viewRideAnalysis(this.value); this.value=\'\'; }" onmouseover="this.style.borderColor=\'var(--accent)\'" onmouseout="this.style.borderColor=\'var(--border-color)\'">' +
                            optionsHtml +
                        '</select>' +
                    '</div>';
                }
                
                let displayStructure = d.structure || '';
                if (displayStructure && (/^\s*-\s+\d+/m.test(displayStructure) || /^\s*\d+x/m.test(displayStructure))) {
                    displayStructure = displayStructure
                        .replace(/^-\s+/gm, '') // Remove leading hyphens
                        .replace(/\n+/g, ' | ') // Replace newlines with dividers
                        .replace(/\s*\|\s*\|\s*/g, ' | ') // Remove duplicate dividers
                        .trim();
                }

                let detailsHtml = '';
                if (!d.is_fallback) {
                    detailsHtml = 
                        '<div style="display: flex; gap: 1.5rem; font-size: 0.8rem; color: var(--text-secondary);">' +
                            '<span>⏱️ <strong>' + d.duration_mins + '</strong> mins</span>' +
                            '<span>⚡ Target TSS: <strong>' + (d.target_tss || 0) + '</strong></span>' +
                            '<span>📈 Target IF: <strong>' + (d.target_if || 0) + '</strong></span>' +
                        '</div>' +
                        '<div style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 6px; padding: 0.5rem 0.75rem; font-size: 0.8rem; font-family: var(--font-family); line-height: 1.4; color: var(--text-primary);">' +
                            displayStructure +
                        '</div>';
                }

                let routeScheduleHtml = '';
                const isPlanned = d.scheduled_start_time || d.route_name;
                if (isPlanned) {
                    const timeStr = (d.scheduled_start_time && d.scheduled_finish_time) 
                        ? '⏰ ' + d.scheduled_start_time + ' - ' + d.scheduled_finish_time
                        : (d.scheduled_start_time ? '⏰ Starts ' + d.scheduled_start_time : '⏰ Not Scheduled');
                    
                    const routeNameStr = d.route_name ? '🗺️ ' + d.route_name + ' (' + (d.route_distance || 0) + ' km)' : '🗺️ No route generated';
                    
                    routeScheduleHtml = '<div style="margin-top: 0.5rem; display: flex; flex-direction: column; gap: 0.35rem; background: rgba(255, 255, 255, 0.02); border: 1px dashed var(--border-color); border-radius: 8px; padding: 0.5rem 0.75rem;">' +
                        '<div style="font-size: 0.75rem; font-weight: 600; color: #ffffff; display: flex; justify-content: space-between;"><span>Schedule & Route</span><span style="color: var(--accent); cursor: pointer; font-size: 0.7rem;" onclick="showRoutePlannerModal(\'' + dateKey + '\')">✏️ Edit</span></div>' +
                        '<div style="font-size: 0.75rem; color: var(--text-secondary);">' + timeStr + '</div>' +
                        '<div style="font-size: 0.75rem; color: var(--text-secondary); font-weight: 500;">' + routeNameStr + '</div>' +
                        '<div style="display: flex; gap: 0.5rem; margin-top: 0.25rem;">' +
                            (d.route_gpx ? '<button class="btn-action" style="padding: 0.15rem 0.35rem; font-size: 0.65rem;" onclick="downloadGPXForDay(\'' + dateKey + '\')">💾 GPX</button>' : '') +
                            (d.route_gpx ? '<button class="btn-action" style="padding: 0.15rem 0.35rem; font-size: 0.65rem;" onclick="syncGPXForDay(\'' + dateKey + '\')">🔄 Export to Karoo</button>' : '') +
                        '</div>' +
                    '</div>';
                } else {
                    routeScheduleHtml = '<div style="margin-top: 0.5rem;">' +
                        '<button class="btn-action" style="display: inline-flex; align-items: center; gap: 0.25rem; font-size: 0.75rem; padding: 0.25rem 0.5rem; font-weight: 600;" onclick="showRoutePlannerModal(\'' + dateKey + '\')">' +
                            '🗺️ Schedule & Route' +
                        '</button>' +
                    '</div>';
                }

                row.innerHTML = 
                    '<div style="flex: 0 0 160px; min-width: 160px; display: flex; flex-direction: column; gap: 0.4rem;">' +
                        '<span style="font-size: 1.15rem; font-weight: 700; color: #ffffff; font-family: \'Outfit\';">' + displayDayLabel + '</span>' +
                        dateDisplay +
                        '<span class="badge" style="background: ' + badgeColor + '; color: ' + textColor + '; border: 1px solid ' + borderColor + '; font-size: 0.75rem; text-align: center; border-radius: 4px; padding: 0.15rem 0.4rem; text-transform: uppercase; width: fit-content; font-weight: 600;">' + d.workout_type + '</span>' +
                        completionBadgeRow +
                    '</div>' +
                    '<div style="flex: 3 1 0px; min-width: 0; display: flex; flex-direction: column; gap: 0.3rem; padding-right: 0.5rem;">' +
                        '<span style="font-size: 1rem; font-weight: 600; color: #ffffff; font-family: \'Outfit\';">' + d.title + '</span>' +
                        '<span style="font-size: 0.85rem; color: var(--text-secondary); line-height: 1.4;">' + d.description + '</span>' +
                    '</div>' +
                    '<div class="calendar-day-details" style="flex: 4 1 0px; min-width: 0; display: flex; flex-direction: column; gap: 0.5rem; border-left: 1px solid rgba(255,255,255,0.05); padding-left: 1.5rem;">' +
                        detailsHtml +
                        analysisLinkHtml +
                        routeScheduleHtml +
                    '</div>';
                grid.appendChild(row);
            });

            if (needsHistorySave) {
                try {
                    clientStorage.setItem('fit_ride_history', JSON.stringify(history));
                } catch (e) {
                    console.error("Failed to update history with resolved metadata:", e);
                }
            }

            grid.style.display = 'flex';
            renderPlannerHistory();
        };
        window.renderTrainingCalendar = renderTrainingCalendar;

        const generateTrainingCalendar = () => {
            const key = clientStorage.getItem('gemini_api_key');
            if (!key) {
                alert('Gemini API Key missing! Please configure your API key first.');
                return;
            }

            const goals = document.getElementById('calendar-goals-input').value.trim();
            const constraints = document.getElementById('calendar-constraints-input').value.trim();
            const model = document.getElementById('calendar-model-select').value;
            const weeksNum = parseInt(document.getElementById('calendar-weeks-select').value) || 1;
            
            // Persist parameters in local storage
            clientStorage.setItem('fit_calendar_goals', goals);
            clientStorage.setItem('fit_calendar_constraints', constraints);
            clientStorage.setItem('fit_calendar_model', model);
            clientStorage.setItem('fit_calendar_weeks', weeksNum);

            // Show loading, hide outputs
            document.getElementById('calendar-loading').style.display = 'flex';
            document.getElementById('calendar-empty-state').style.display = 'none';
            document.getElementById('calendar-grid').style.display = 'none';
            document.getElementById('calendar-summary-box').style.display = 'none';
            document.getElementById('calendar-overview-box').style.display = 'none';

            
            // Build recent ride history context
            let historyText = "No previous ride history found.";
            const historyData = clientStorage.getItem('fit_ride_history');
            if (historyData) {
                try {
                    const parsed = JSON.parse(historyData);
                    if (parsed && parsed.length > 0) {
                        historyText = parsed.slice(-5).map(r => 
                            "- Date: " + r.date + " | Distance: " + r.distance_km + " km | NP: " + r.np + " W | Avg HR: " + r.avg_hr + " bpm\n  Key Performance Summary: " + r.summary
                        ).join('\n');
                    }
                } catch(e) {
                    console.error("Error reading history for calendar:", e);
                }
            }

            // Retrieve last generated training program from localStorage for context
            let lastPlanText = "No previous training plan found in local storage.";
            const lastPlanData = clientStorage.getItem('fit_training_program');
            if (lastPlanData) {
                try {
                    const parsed = JSON.parse(lastPlanData);
                    if (parsed) {
                        const lastSummary = parsed.weekly_summary || "No weekly focus available.";
                        const lastDays = (parsed.days || []).map(d => 
                            "- " + d.day + ": " + d.title + " (" + d.workout_type + ", " + d.duration_mins + " mins) - " + d.description
                        ).join('\n');
                        lastPlanText = "Weekly Summary: " + lastSummary + "\n" + lastDays;
                    }
                } catch(e) {
                    console.error("Error loading previous plan for prompt context:", e);
                }
            }

            // Calculate current date context and training week dates
            const today = new Date();
            const formatDate = (d) => d.toLocaleDateString('en-US', { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' });
            const todayStr = formatDate(today);
            
            const tomorrow = new Date(today);
            tomorrow.setDate(today.getDate() + 1);
            const tomorrowStr = formatDate(tomorrow);

            const todayMonday = getMonday(today);
            const planStart = new Date(todayMonday);
            planStart.setDate(todayMonday.getDate() + (window.plannerCalendarWeekIndex * 7));
            const planStartStr = formatDate(planStart);
            
            // Generate chronological list of weeks and days starting on the Monday of the planned week
            let promptDateGuides = "";
            for (let w = 0; w < weeksNum; w++) {
                const weekStart = new Date(planStart);
                weekStart.setDate(planStart.getDate() + (w * 7));
                const weekStartStr = formatDate(weekStart);
                promptDateGuides += "### Week " + (w + 1) + " (Starting Monday " + weekStartStr + "):\n";
                for (let i = 0; i < 7; i++) {
                    const d = new Date(planStart);
                    d.setDate(planStart.getDate() + (w * 7) + i);
                    promptDateGuides += "- Day " + (i + 1) + " (" + d.toLocaleDateString('en-US', { weekday: 'long' }) + "): " + d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' }) + "\n";
                }
                promptDateGuides += "\n";
            }

            const promptText = "You are an elite cycling coach and exercise physiologist. Design a structured multi-week training program for the next " + weeksNum + " weeks (" + (weeksNum * 7) + " days in total), starting on Monday, " + planStartStr + " (with today being " + todayStr + "), for an athlete based on their recent ride history, current FTP, training goals, constraints, and their last generated training plan.\n\n" +
                "Date Context & Weeks:\n" +
                "- Today is: " + todayStr + "\n" +
                "- Tomorrow is: " + tomorrowStr + "\n\n" +
                "Here are the specific weeks and days to plan:\n" + promptDateGuides + "\n" +
                "IMPORTANT RULES FOR HANDLING DAYS/DATES:\n" +
                "1. Use the today date (" + todayStr + ") to interpret relative days/dates mentioned in the constraints (e.g. if today is Tuesday, then 'yesterday' is Monday, 'tomorrow' is Wednesday, 'this Friday' is Friday, etc.).\n" +
                "2. If the athlete says they 'just finished' or 'completed' a workout today (which is " + todayStr + "), you MUST record that completed workout on the corresponding calendar date in your training plan. DO NOT prescribe 'Rest' or a different workout for that date if a workout has already been completed.\n" +
                "3. Align all other planned workouts and rest days with the constraints (e.g. if they say they will ride 100km on Friday, place that on the Friday date; if they suggest rides for Saturday and Sunday, schedule those weekend workouts).\n" +
                "4. Apply the training constraints to all weeks in the generated program (e.g. if Monday and Friday are rest days, apply this to each week; if Tuesday/Thursday trainer sessions are capped at 1 hour, apply this to each week) unless the constraints specify a particular date.\n" +
                "5. Ensure continuity in training load, progression, recovery, and volume across the weeks. Start from the context of the last generated training plan if available.\n\n" +
                "Last Generated Training Plan Context:\n" + lastPlanText + "\n\n" +
                "Athlete FTP: " + athleteFTP + " W\n" +
                "Athlete Max Heart Rate (HR): " + athleteMaxHR + " bpm\n\n" +
                "Recent Ride History:\n" + historyText + "\n\n" +
                "Athlete's Training Goals:\n" + goals + "\n\n" +
                "Athlete's Constraints for the Training Week:\n" + constraints + "\n\n" +
                "Please output the program strictly as a JSON object matching the following structure:\n" +
                "{\n" +
                "  \"weeks\": [\n" +
                "    {\n" +
                "      \"weekly_summary\": \"Provide a 2-3 sentence overview of this week's physiological focus and progression.\",\n" +
                "      \"days\": [\n" +
                "        {\n" +
                "          \"date\": \"2026-06-22\",\n" +
                "          \"day\": \"Monday, Jun 22, 2026\",\n" +
                "          \"workout_type\": \"Rest Day / Recovery / Endurance / Tempo / Sweet Spot / Threshold / VO2 Max / Anaerobic\",\n" +
                "          \"title\": \"Workout Name\",\n" +
                "          \"duration_mins\": 60,\n" +
                "          \"target_tss\": 55,\n" +
                "          \"target_if\": 0.72,\n" +
                "          \"description\": \"Overview of the workout focus.\",\n" +
                "          \"structure\": \"Warm Up: 10m easy spinning. Main Set: 3x8m at Sweet Spot (200-215W) with 4m recovery. Cool Down: 10m easy spinning.\",\n" +
                "          \"intervals_icu_structure\": \"- 10m ramp 50-75%\\n\\n3x\\n- 8m 85% 90rpm\\n- 4m 50% recovery\\n\\n- 10m 50%\"\n" +
                "        },\n" +
                "        ... (exactly 7 days for this week starting on Monday and ending on Sunday)\n" +
                "      ]\n" +
                "    },\n" +
                "    ... (continue for all " + weeksNum + " weeks)\n" +
                "  ]\n" +
                "}\n\n" +
                "Please output two structure fields for each day:\n" +
                "1. \"structure\": a friendly, human-readable summary of the workout steps, suitable for displaying to the athlete.\n" +
                "2. \"intervals_icu_structure\": MUST be written in the specific plain-text formatting language that Intervals.icu parses to generate structured workout graphs. Use this exact syntax:\n" +
                "- Each interval step starts with a hyphen and space (e.g. \"- 10m ramp 50-75%\", \"- 5m 140w 95-100rpm\", or \"- 20m 85%\").\n" +
                "- Specify duration using \"m\" (minutes) or \"s\" (seconds).\n" +
                "- Specify intensity target as either absolute wattage (e.g., \"140w\"), target percentage of FTP (e.g., \"85%\"), or percentage ramp (e.g., \"50-75%\").\n" +
                "- Specify target cadences if any using rpm (e.g., \"95-100rpm\").\n" +
                "- Repeats must use \"Nx\" (e.g. \"3x\") followed by a newline and indented interval steps.\n" +
                "- Separate major blocks (like Warm Up, Main Set, Cool Down) with empty lines.\n" +
                "Ensure the output is valid JSON. Do not wrap it in anything other than the JSON block. Do not include markdown code block syntax outside the raw JSON text.";

            const callGemini = (apiVersion) => {
                const url = 'https://generativelanguage.googleapis.com/' + apiVersion + '/models/' + model + ':generateContent?key=' + key;
                return fetch(url, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        contents: [{
                            role: 'user',
                            parts: [{ text: promptText }]
                        }]
                    })
                })
                .then(res => {
                    if (!res.ok) {
                        return res.json().then(errData => {
                            const errMsg = errData.error?.message || ('HTTP ' + res.status);
                            return { ok: false, status: res.status, message: errMsg };
                        });
                    }
                    return res.json().then(data => ({ ok: true, data }));
                });
            };

            callGemini('v1')
                .then(result => {
                    if (result.ok) return result.data;
                    if (result.status === 404) {
                        return callGemini('v1beta').then(betaResult => {
                            if (betaResult.ok) return betaResult.data;
                            throw new Error(betaResult.message);
                        });
                    }
                    throw new Error(result.message);
                })
                .then(data => {
                    const text = data.candidates?.[0]?.content?.parts?.[0]?.text;
                    if (!text) throw new Error("Empty response from Gemini API.");
                    
                    const jsonStart = text.indexOf('{');
                    const jsonEnd = text.lastIndexOf('}');
                    if (jsonStart === -1 || jsonEnd === -1 || jsonEnd < jsonStart) {
                        throw new Error("Failed to parse training plan: response was not in JSON format.");
                    }
                    const jsonText = text.substring(jsonStart, jsonEnd + 1);
                    const parsedProgram = JSON.parse(jsonText);
                    
                    // Parse weeks (supporting both multi-week "weeks" array and single-week format)
                    let weeks = [];
                    if (parsedProgram.weeks && Array.isArray(parsedProgram.weeks)) {
                        weeks = parsedProgram.weeks;
                    } else if (parsedProgram.days) {
                        weeks = [parsedProgram];
                    } else {
                        throw new Error("Invalid program structure: 'weeks' or 'days' not found.");
                    }
                    
                    let firstMergedProgram = null;
                    weeks.forEach((weekData, wIdx) => {
                        const weekStartDate = new Date(planStart);
                        weekStartDate.setDate(planStart.getDate() + (wIdx * 7));
                        
                        const weekProgram = {
                            start_date: formatLocalDateKey(weekStartDate),
                            weekly_summary: weekData.weekly_summary || "",
                            days: weekData.days || []
                        };
                        
                        const merged = saveProgramToHistory(weekProgram);
                        if (wIdx === 0) {
                            firstMergedProgram = merged;
                        }
                    });
                    
                    if (firstMergedProgram) {
                        clientStorage.setItem('fit_training_program', JSON.stringify(firstMergedProgram));
                        
                        // Sync current calendar display view to the current selected week index
                        const synthesizedWeek = getSynthesizedWeek(window.plannerCalendarWeekIndex);
                        window.currentCalendarProgram = synthesizedWeek;
                        renderTrainingCalendar(synthesizedWeek);
                        renderPlannerHistory();
                        if (typeof renderUnifiedLandingCalendar === 'function') {
                            renderUnifiedLandingCalendar();
                        }
                    }
                })
                .catch(err => {
                    console.error("Calendar generation error:", err);
                    alert("Error planning training week: " + err.message);
                    document.getElementById('calendar-empty-state').style.display = 'flex';
                })
                .finally(() => {
                    document.getElementById('calendar-loading').style.display = 'none';
                });
        };
        window.generateTrainingCalendar = generateTrainingCalendar;

        const triggerDeviceLinking = () => {
            showDashboardView();
            setTimeout(() => {
                const selectRideBtn = document.getElementById('btn-select-ride');
                if (selectRideBtn) selectRideBtn.click();
            }, 150);
        };
        window.triggerDeviceLinking = triggerDeviceLinking;

        const promptFTPConfig = () => {
            const currentFTP = athleteFTP;
            const newFTP = prompt('Enter your Functional Threshold Power (FTP) in Watts:', currentFTP);
            if (newFTP && !isNaN(parseInt(newFTP)) && parseInt(newFTP) > 0) {
                updateFTP(parseInt(newFTP));
                alert('FTP updated to ' + parseInt(newFTP) + 'W');
            }
        };
        window.promptFTPConfig = promptFTPConfig;

        const promptAPIConfig = () => {
            const currentKey = clientStorage.getItem('gemini_api_key') || '';
            const newKey = prompt('Enter your Gemini API Key:', currentKey);
            if (newKey !== null) {
                clientStorage.setItem('gemini_api_key', newKey.trim());
                alert('Gemini API Key updated successfully!');
            }
        };
        window.promptAPIConfig = promptAPIConfig;

        const showIntervalsConfigModal = () => {
            const modal = document.getElementById('intervals-config-modal');
            const statusEl = document.getElementById('intervals-test-status');
            if (statusEl) {
                statusEl.style.display = 'none';
                statusEl.innerHTML = '';
            }
            if (modal) modal.style.display = 'flex';

            fetch('/api/intervals/config')
                .then(r => r.json())
                .then(data => {
                    document.getElementById('intervals-athlete-id').value = data.athlete_id || '0';
                    document.getElementById('intervals-api-key').value = '';
                    if (data.has_api_key) {
                        document.getElementById('intervals-api-key').placeholder = '•••••••••••••••• (API Key is saved)';
                    } else {
                        document.getElementById('intervals-api-key').placeholder = 'Paste Intervals.icu API Key';
                    }
                    document.getElementById('intervals-enabled').checked = data.enabled || false;
                })
                .catch(err => {
                    console.error("Error loading Intervals.icu config:", err);
                });
        };
        window.showIntervalsConfigModal = showIntervalsConfigModal;

        const hideIntervalsConfigModal = () => {
            const modal = document.getElementById('intervals-config-modal');
            if (modal) modal.style.display = 'none';
        };
        window.hideIntervalsConfigModal = hideIntervalsConfigModal;

        const testIntervalsConnection = () => {
            const athleteId = document.getElementById('intervals-athlete-id').value.trim();
            const apiKey = document.getElementById('intervals-api-key').value.trim();
            const statusEl = document.getElementById('intervals-test-status');
            const btn = document.getElementById('btn-intervals-test');

            if (statusEl) {
                statusEl.style.display = 'block';
                statusEl.style.background = 'rgba(255, 255, 255, 0.05)';
                statusEl.style.color = 'var(--text-secondary)';
                statusEl.style.border = '1px solid var(--border-color)';
                statusEl.innerText = 'Testing connection...';
            }
            if (btn) btn.disabled = true;

            fetch('/api/intervals/test', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ athlete_id: athleteId, api_key: apiKey })
            })
            .then(r => {
                if (!r.ok) {
                    return r.json().then(data => { throw new Error(data.error || 'Connection failed'); });
                }
                return r.json();
            })
            .then(data => {
                if (statusEl) {
                    statusEl.style.background = 'rgba(46, 204, 113, 0.1)';
                    statusEl.style.color = '#2ecc71';
                    statusEl.style.border = '1px solid rgba(46, 204, 113, 0.2)';
                    statusEl.innerText = data.message || 'Connected successfully!';
                }
            })
            .catch(err => {
                if (statusEl) {
                    statusEl.style.background = 'rgba(231, 76, 60, 0.1)';
                    statusEl.style.color = '#e74c3c';
                    statusEl.style.border = '1px solid rgba(231, 76, 60, 0.2)';
                    statusEl.innerText = 'Error: ' + err.message;
                }
            })
            .finally(() => {
                if (btn) btn.disabled = false;
            });
        };
        window.testIntervalsConnection = testIntervalsConnection;

        const saveIntervalsConfig = () => {
            const athleteId = document.getElementById('intervals-athlete-id').value.trim();
            const apiKey = document.getElementById('intervals-api-key').value.trim();
            const enabled = document.getElementById('intervals-enabled').checked;
            const btn = document.getElementById('btn-intervals-save');

            if (btn) btn.disabled = true;

            fetch('/api/intervals/config', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ athlete_id: athleteId, api_key: apiKey, enabled: enabled })
            })
            .then(r => {
                if (!r.ok) throw new Error('Failed to save connection settings');
                return r.json();
            })
            .then(() => {
                alert('Intervals.icu connection settings saved successfully!');
                hideIntervalsConfigModal();
                updateIntervalsSyncUI();
            })
            .catch(err => {
                alert('Error: ' + err.message);
            })
            .finally(() => {
                if (btn) btn.disabled = false;
            });
        };
        window.saveIntervalsConfig = saveIntervalsConfig;

        const distillWorkoutStructure = async (title, description, oldStructure) => {
            const key = clientStorage.getItem('gemini_api_key');
            if (!key) return oldStructure;

            const modelSelect = document.getElementById('calendar-model-select');
            const model = modelSelect ? modelSelect.value : 'gemini-3.5-flash';
            const apiVersion = model.indexOf('gemini-3') === 0 ? 'v1beta' : 'v1';
            
            const prompt = "You are a workout structure converter. Convert the following workout details into the strict Intervals.icu plain-text workout formatting language.\n\n" +
                "Workout Name: " + title + "\n" +
                "Description: " + description + "\n" +
                "Current Text Structure: " + oldStructure + "\n\n" +
                "Strict Format Rules:\n" +
                "- Every interval step must start with a hyphen and a space (e.g. '- 10m ramp 50-75%')\n" +
                "- Use 'm' for minutes and 's' for seconds\n" +
                "- Target intensity must be either % of FTP (e.g. '85%'), target watts (e.g. '150w'), or recovery (e.g. '50% recovery')\n" +
                "- Repeats must use 'Nx' on a line, followed by indented steps on next lines (e.g. '3x\\n- 5m 85%\\n- 3m 50% recovery')\n" +
                "- Do not include any conversational text, headers (like 'Warm Up:'), or explanation. Return ONLY the formatted steps.\n\n" +
                "Example Format:\n" +
                "- 10m ramp 50-75%\n\n" +
                "3x\n" +
                "- 5m 140w 95-100rpm\n" +
                "- 3m 80w recovery\n\n" +
                "- 10m 50%";

            try {
                const url = 'https://generativelanguage.googleapis.com/' + apiVersion + '/models/' + model + ':generateContent?key=' + key;
                const response = await fetch(url, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        contents: [{
                            role: 'user',
                            parts: [{ text: prompt }]
                        }]
                    })
                });
                if (!response.ok) return oldStructure;
                const result = await response.json();
                let text = (result.candidates && result.candidates[0] && result.candidates[0].content && result.candidates[0].content.parts && result.candidates[0].content.parts[0]) ? result.candidates[0].content.parts[0].text : '';
                text = text.trim();
                var ticks = String.fromCharCode(96, 96, 96);
                if (text.indexOf(ticks) === 0) {
                    text = text.substring(3);
                    if (text.indexOf("json") === 0) {
                        text = text.substring(4);
                    }
                    if (text.lastIndexOf(ticks) === text.length - 3) {
                        text = text.substring(0, text.length - 3);
                    }
                }
                return text.trim() || oldStructure;
            } catch (err) {
                console.error("Error distilling workout structure:", err);
                return oldStructure;
            }
        };

        const exportCalendarToIntervals = async () => {
            if (!window.currentCalendarProgram) {
                alert('No training plan week active to export!');
                return;
            }
            const btn = document.getElementById('btn-intervals-export');
            if (btn) {
                btn.disabled = true;
                btn.innerHTML = '<span style="width:12px; height:12px; border:2px solid #fff; border-top:2px solid transparent; border-radius:50%; animation:spin 1s linear infinite; display:inline-block; margin-right:4px;"></span> Exporting...';
            }

            const isIntervalsFormat = (str) => {
                if (!str) return false;
                return /^\s*-\s+\d+/m.test(str) || /^\s*\d+x/m.test(str);
            };

            const plansByDate = JSON.parse(clientStorage.getItem('fit_training_plans_by_date') || '{}');
            
            // Ensure all workouts from current day forward exist
            const today = new Date();
            today.setHours(0,0,0,0);
            const existingWeeks = getWeeksWithPlans() || [];
            let maxDate = new Date(today);
            maxDate.setDate(maxDate.getDate() + 6); // default to end of current week
            
            existingWeeks.forEach(w => {
                const wStart = parseLocalDate(w.start_date);
                const wEnd = new Date(wStart);
                wEnd.setDate(wEnd.getDate() + 6);
                if (wEnd.getTime() > maxDate.getTime()) {
                    maxDate = wEnd;
                }
            });

            let storageChanged = false;
            for (let d = new Date(today); d.getTime() <= maxDate.getTime(); d.setDate(d.getDate() + 1)) {
                const key = formatLocalDateKey(d);
                if (!plansByDate[key]) {
                    plansByDate[key] = {
                        day: d.toLocaleDateString('en-US', { weekday: 'long' }),
                        date: key,
                        date_key: key,
                        workout_type: "No Plan",
                        title: "No Plan",
                        duration_mins: 0,
                        target_tss: 0,
                        target_if: 0,
                        description: "No training plan focus generated for this day.",
                        is_fallback: true
                    };
                    storageChanged = true;
                }
            }

            if (storageChanged) {
                clientStorage.setItem('fit_training_plans_by_date', JSON.stringify(plansByDate));
            }

            const activeMonday = getMonday(window.currentCalendarProgram.start_date);
            const activeMondayTime = activeMonday.getTime();

            // Find all weeks with plans from history starting from activeMonday onwards
            const weeksWithPlans = getWeeksWithPlans() || [];
            const weeksToSync = weeksWithPlans.filter(w => {
                const wStart = parseLocalDate(w.start_date);
                return wStart.getTime() >= activeMondayTime;
            });

            // Sort chronologically
            weeksToSync.sort((a, b) => parseLocalDate(a.start_date).getTime() - parseLocalDate(b.start_date).getTime());

            // Build full list of workouts for all these weeks
            const allWorkouts = [];
            for (const week of weeksToSync) {
                const weekMonday = getMonday(week.start_date);
                for (let i = 0; i < 7; i++) {
                    const d = new Date(weekMonday);
                    d.setDate(weekMonday.getDate() + i);
                    const key = formatLocalDateKey(d);
                    const dayPlan = plansByDate[key];
                    if (dayPlan) {
                        dayPlan.date_key = key;
                        if (!dayPlan.date) dayPlan.date = key;
                        allWorkouts.push(dayPlan);
                    } else {
                        allWorkouts.push({
                            day: d.toLocaleDateString('en-US', { weekday: 'long' }),
                            date: key,
                            date_key: key,
                            workout_type: "No Plan",
                            title: "No Plan",
                            duration_mins: 0,
                            target_tss: 0,
                            target_if: 0,
                            description: "No training plan focus generated for this day.",
                            is_fallback: true
                        });
                    }
                }
            }

            let updated = false;
            const workoutsCopy = JSON.parse(JSON.stringify(allWorkouts));

            for (let i = 0; i < workoutsCopy.length; i++) {
                const w = workoutsCopy[i];
                if (w.duration_mins > 0 && w.workout_type.toLowerCase() !== 'rest' && w.workout_type.toLowerCase() !== 'rest day' && w.workout_type.toLowerCase() !== 'no plan') {
                    if (!w.intervals_icu_structure || !isIntervalsFormat(w.intervals_icu_structure)) {
                        const sourceText = w.intervals_icu_structure || w.structure || '';
                        if (!isIntervalsFormat(sourceText)) {
                            if (btn) {
                                btn.innerHTML = '<span style="width:12px; height:12px; border:2px solid #fff; border-top:2px solid transparent; border-radius:50%; animation:spin 1s linear infinite; display:inline-block; margin-right:4px;"></span> Distilling ' + (w.date || w.day) + '...';
                            }
                            const distilled = await distillWorkoutStructure(w.title, w.description, sourceText);
                            w.intervals_icu_structure = distilled;
                            updated = true;
                        } else {
                            w.intervals_icu_structure = sourceText;
                            updated = true;
                        }
                    }
                }
            }

            if (updated) {
                // Save updated workouts back to plansByDate
                workoutsCopy.forEach(w => {
                    if (w.date_key) {
                        plansByDate[w.date_key] = w;
                    }
                });
                clientStorage.setItem('fit_training_plans_by_date', JSON.stringify(plansByDate));
                // Re-render
                const synthesizedWeek = getSynthesizedWeek(window.plannerCalendarWeekIndex);
                window.currentCalendarProgram = synthesizedWeek;
                renderTrainingCalendar(synthesizedWeek);
            }

            if (btn) {
                btn.innerHTML = '<span style="width:12px; height:12px; border:2px solid #fff; border-top:2px solid transparent; border-radius:50%; animation:spin 1s linear infinite; display:inline-block; margin-right:4px;"></span> Syncing...';
            }

            fetch('/api/intervals/export', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    start_date: window.currentCalendarProgram.start_date,
                    workouts: workoutsCopy
                })
            })
            .then(r => {
                if (!r.ok) {
                    return r.json().then(data => { throw new Error(data.error || 'Export failed'); });
                }
                return r.json();
            })
            .then(results => {
                var created = 0;
                var skipped = 0;
                var failed = 0;
                var failedMsgs = [];

                results.forEach(res => {
                    if (res.status === 'created') created++;
                    else if (res.status === 'skipped') skipped++;
                    else {
                        failed++;
                        failedMsgs.push(res.name + ': ' + res.status);
                    }
                });

                var msg = "Workouts Sync Results:\n";
                if (created > 0) msg += "• " + created + " workouts successfully synced (deleted old version if present)\n";
                if (skipped > 0) msg += "• " + skipped + " rest/empty days skipped\n";
                if (failed > 0) {
                    msg += "• " + failed + " exports failed:\n  " + failedMsgs.join('\n  ') + "\n";
                }

                alert(msg);
            })
            .catch(err => {
                alert('Export failed: ' + err.message);
            })
            .finally(() => {
                if (btn) {
                    btn.disabled = false;
                    btn.innerHTML = '📅 Add workouts to calendar';
                }
            });
        };
        window.exportCalendarToIntervals = exportCalendarToIntervals;

        // Initialize FTP input field and render lists
        const ftpInput = document.getElementById('ftp-input');
        if (ftpInput) {
            ftpInput.value = athleteFTP;
            ftpInput.addEventListener('input', (e) => {
                const val = parseInt(e.target.value);
                if (!isNaN(val) && val > 0) {
                    athleteFTP = val;
                    clientStorage.setItem('fit_athlete_ftp', athleteFTP);
                    if (window.updateIFDisplay) window.updateIFDisplay();
                    if (window.renderZones) window.renderZones();
                    renderFtpEstimates();
                }
            });
        }

        const maxHrInput = document.getElementById('max-hr-input');
        if (maxHrInput) {
            maxHrInput.value = athleteMaxHR;
            maxHrInput.addEventListener('input', (e) => {
                const val = parseInt(e.target.value);
                if (!isNaN(val) && val > 0) {
                    athleteMaxHR = val;
                    clientStorage.setItem('fit_athlete_max_hr', athleteMaxHR);
                    if (window.renderZones) window.renderZones();
                }
            });
        }
        renderFtpEstimates();
        try {
            renderDynamicCards(data);
        } catch(e) {
            console.error("Error rendering dynamic cards:", e);
        }
        } // End of renderDashboard function

        function renderDynamicCards(data) {
            const container = document.getElementById('dynamic-cards-container');
            if (!container) return;
            container.innerHTML = '';

            const savedCardsData = clientStorage.getItem('directeur_custom_cards');
            const savedCards = savedCardsData ? JSON.parse(savedCardsData) : [];

            savedCards.forEach(card => {
                const cardEl = document.createElement('div');
                cardEl.className = 'card';
                cardEl.id = 'dynamic-card-' + card.id;
                cardEl.style.marginTop = '1.5rem';

                const headerEl = document.createElement('div');
                headerEl.className = 'card-header';
                headerEl.style.display = 'flex';
                headerEl.style.justifyContent = 'space-between';
                headerEl.style.alignItems = 'center';

                const titleEl = document.createElement('div');
                titleEl.className = 'card-title';
                titleEl.textContent = card.title;

                const actionsEl = document.createElement('div');
                actionsEl.style.display = 'flex';
                actionsEl.style.alignItems = 'center';
                actionsEl.style.gap = '0.5rem';

                const collapseBtn = document.createElement('button');
                collapseBtn.className = 'btn-action';
                collapseBtn.style.fontSize = '0.75rem';
                collapseBtn.style.padding = '0.2rem 0.5rem';

                const refineBtn = document.createElement('button');
                refineBtn.className = 'btn-action';
                refineBtn.style.fontSize = '0.75rem';
                refineBtn.style.padding = '0.2rem 0.5rem';
                refineBtn.innerHTML = '✏️ Refine';

                const deleteBtn = document.createElement('button');
                deleteBtn.className = 'btn-action';
                deleteBtn.style.borderColor = 'rgba(231, 76, 60, 0.4)';
                deleteBtn.style.color = '#fc8181';
                deleteBtn.style.fontSize = '0.75rem';
                deleteBtn.style.padding = '0.2rem 0.5rem';
                deleteBtn.innerHTML = '🗑️ Delete';
                deleteBtn.addEventListener('click', (e) => {
                    e.stopPropagation();
                    if (confirm('Are you sure you want to delete this custom chart?')) {
                        deleteDynamicCard(card.id);
                    }
                });

                actionsEl.appendChild(collapseBtn);
                actionsEl.appendChild(refineBtn);
                actionsEl.appendChild(deleteBtn);
                headerEl.appendChild(titleEl);
                headerEl.appendChild(actionsEl);
                cardEl.appendChild(headerEl);

                // Inline Refinement Panel
                const refinePanel = document.createElement('div');
                refinePanel.id = 'refine-panel-' + card.id;
                refinePanel.style.display = 'none';
                refinePanel.style.flexDirection = 'column';
                refinePanel.style.gap = '0.5rem';
                refinePanel.style.padding = '0.75rem';
                refinePanel.style.marginTop = '0.5rem';
                refinePanel.style.background = 'var(--bg-tertiary)';
                refinePanel.style.border = '1px solid var(--border-color)';
                refinePanel.style.borderRadius = '8px';

                refinePanel.innerHTML = 
                    '<div style="font-size: 0.8rem; font-weight: 600; color: var(--accent); margin-bottom: 0.25rem;">🔧 Adjust Card Functionality</div>' +
                    '<textarea class="refine-feedback" placeholder="Describe the changes you want (e.g. change colors, fix layout alignment, or paste error messages to auto-heal)..." style="width: 100%; height: 65px; background: rgba(0,0,0,0.20); border: 1px solid var(--border-color); border-radius: 6px; color: #ffffff; padding: 0.5rem; font-family: inherit; font-size: 0.82rem; outline: none; resize: vertical; line-height: 1.4;"></textarea>' +
                    '<div style="display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.25rem;">' +
                        '<div class="refine-status" style="font-size: 0.75rem; color: var(--text-secondary); margin-right: auto; display: flex; align-items: center; gap: 0.4rem;"></div>' +
                        '<button class="btn-action btn-refine-cancel" style="padding: 0.2rem 0.6rem; font-size: 0.75rem;">Cancel</button>' +
                        '<button class="btn-action btn-refine-submit" style="padding: 0.2rem 0.6rem; font-size: 0.75rem; border-color: #2ecc71; color: #2ecc71;">⚡ Adjust</button>' +
                    '</div>';

                cardEl.appendChild(refinePanel);

                const bodyEl = document.createElement('div');
                bodyEl.style.padding = '1rem 0';
                bodyEl.style.minHeight = '200px';
                bodyEl.style.display = 'flex';
                bodyEl.style.flexDirection = 'column';
                bodyEl.style.gap = '1rem';
                cardEl.appendChild(bodyEl);
                container.appendChild(cardEl);

                const updateCollapseState = (collapsed) => {
                    if (collapsed) {
                        bodyEl.style.display = 'none';
                        refinePanel.style.display = 'none';
                        collapseBtn.innerHTML = '▼ Show';
                        clientStorage.setItem('directeur_card_collapsed_' + card.id, 'true');
                    } else {
                        bodyEl.style.display = 'flex';
                        collapseBtn.innerHTML = '▲ Collapse';
                        clientStorage.setItem('directeur_card_collapsed_' + card.id, 'false');
                        setTimeout(() => {
                            window.dispatchEvent(new Event('resize'));
                        }, 50);
                    }
                };

                collapseBtn.addEventListener('click', (e) => {
                    e.stopPropagation();
                    const currentlyCollapsed = clientStorage.getItem('directeur_card_collapsed_' + card.id) === 'true';
                    updateCollapseState(!currentlyCollapsed);
                });

                refineBtn.addEventListener('click', (e) => {
                    e.stopPropagation();
                    const currentlyCollapsed = clientStorage.getItem('directeur_card_collapsed_' + card.id) === 'true';
                    if (currentlyCollapsed) {
                        updateCollapseState(false);
                    }
                    const isHidden = refinePanel.style.display === 'none';
                    refinePanel.style.display = isHidden ? 'flex' : 'none';
                    if (isHidden) {
                        refinePanel.querySelector('.refine-feedback').focus();
                    }
                });

                refinePanel.querySelector('.btn-refine-cancel').addEventListener('click', (e) => {
                    e.stopPropagation();
                    refinePanel.style.display = 'none';
                });

                refinePanel.querySelector('.btn-refine-submit').addEventListener('click', (e) => {
                    e.stopPropagation();
                    const feedbackText = refinePanel.querySelector('.refine-feedback').value.trim();
                    if (!feedbackText) {
                        alert('Please describe what to adjust.');
                        return;
                    }
                    
                    const submitBtn = refinePanel.querySelector('.btn-refine-submit');
                    const cancelBtn = refinePanel.querySelector('.btn-refine-cancel');
                    const statusText = refinePanel.querySelector('.refine-status');
                    
                    submitBtn.disabled = true;
                    cancelBtn.disabled = true;
                    statusText.innerHTML = '<div style="width: 12px; height: 12px; border: 2px solid var(--border-color); border-top: 2px solid var(--accent); border-radius: 50%; animation: spin 1s linear infinite;"></div> Asking Gemini...';

                    const key = clientStorage.getItem('gemini_api_key');
                    if (!key) {
                        alert('Please configure your Gemini API Key first (at the bottom of the page or in Settings).');
                        submitBtn.disabled = false;
                        cancelBtn.disabled = false;
                        statusText.textContent = '';
                        return;
                    }

                    const modelSelect = document.getElementById('evolve-model-select');
                    const model = modelSelect ? modelSelect.value : 'gemini-3.5-flash';

                    let cardError = '';
                    const errorMsgEl = cardEl.querySelector('.card-error-msg');
                    if (errorMsgEl) {
                        cardError = errorMsgEl.textContent;
                    }

                    const bt = String.fromCharCode(96) + String.fromCharCode(96) + String.fromCharCode(96);
                    const prompt = "You are a professional cycling data analyst and expert JavaScript developer.\n" +
                        "You are tasked with ADJUSTING or FIXING an existing custom data analysis card in a cycling dashboard.\n\n" +
                        "Here is the schema of the global 'rideData' object available in the context:\n" +
                        "{\n" +
                        "  \"summary\": {\n" +
                        "    \"start_time\": \"2026-05-27T18:00:00Z\",\n" +
                        "    \"duration_seconds\": 3600,\n" +
                        "    \"distance_meters\": 32000,\n" +
                        "    \"average_power\": 210,\n" +
                        "    \"normalized_power\": 225,\n" +
                        "    \"tss\": 65,\n" +
                        "    \"if\": 0.85,\n" +
                        "    \"calories\": 750,\n" +
                        "    \"average_heartrate\": 145,\n" +
                        "    \"average_cadence\": 90,\n" +
                        "    \"source_file\": \"2026-05-27_18-00-00.fit\"\n" +
                        "  },\n" +
                        "  \"records\": [\n" +
                        "    { \"timestamp\": \"2026-05-27T18:00:00Z\", \"power\": 200, \"heart_rate\": 130, \"cadence\": 85, \"speed\": 8.5, \"altitude\": 120, \"front_gear_teeth\": 50, \"rear_gear_teeth\": 17 }\n" +
                        "  ],\n" +
                        "  \"gear_usage\": [\n" +
                        "    { \"combination\": \"50x17\", \"seconds\": 850, \"percentage\": 23.6 }\n" +
                        "  ]\n" +
                        "}\n\n" +
                        "Here is the existing JavaScript code for the card:\n" +
                        bt + "javascript\n" + card.jsCode + "\n" + bt + "\n\n" +
                        (cardError ? "When executing this code, it failed with the following error:\n" + cardError + "\n\n" : "") +
                        "The athlete has requested the following adjustments/fixes:\n" + feedbackText + "\n\n" +
                        "Your output MUST be a valid JSON object matching the following structure and no other text (do NOT wrap in markdown code blocks, do NOT write explanations, just return raw JSON):\n" +
                        "{\n" +
                        "  \"id\": \"" + card.id + "\",\n" +
                        "  \"title\": \"An updated title if needed, or keep the same: " + card.title.replace(/"/g, '\\"') + "\",\n" +
                        "  \"jsCode\": \"// Updated JavaScript code\"\n" +
                        "}\n\n" +
                        "Rules for 'jsCode':\n" +
                        "1. The code will be run as a function body with three parameters: 'data', 'container', and 'Chart'.\n" +
                        "2. It must render its visual content inside the 'container' element (which is a standard HTML div).\n" +
                        "3. If creating a chart, it must create a '<canvas>' element inside the 'container', style it to fit (e.g. style.height = '350px'), and instantiate a new Chart.js graph using the provided 'Chart' constructor.\n" +
                        "4. All HTML elements (like legends, stat boxes, tables) must be generated dynamically using standard JS DOM manipulation methods (e.g. document.createElement, appendChild, innerHTML) and appended to 'container'.\n" +
                        "5. Ensure the styling matches a premium dark mode theme (backgrounds should be transparent or dark, colors should be white/var(--text-primary)/var(--accent)/var(--text-secondary), borders should be var(--border-color)).\n" +
                        "6. Do NOT load external libraries, CSS, or make API calls. Access all ride metrics exclusively from the 'data' parameter.\n" +
                        "7. Calculate any required summaries from the 'data.records' array (e.g. averages, ranges, correlations, distributions) or retrieve them from 'data.summary'.\n" +
                        "8. Ensure all variable names are locally scoped (e.g., const, let) and do not clash with global namespaces.";

                    const apiVersion = model.indexOf('gemini-3') === 0 ? 'v1beta' : 'v1';
                    
                    const makeCall = (version) => {
                        const callUrl = 'https://generativelanguage.googleapis.com/' + version + '/models/' + model + ':generateContent?key=' + key;
                        return fetch(callUrl, {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({
                                contents: [{
                                    role: 'user',
                                    parts: [{ text: prompt }]
                                }]
                            })
                        })
                        .then(res => {
                            if (!res.ok) {
                                return res.json().then(errData => {
                                    const errMsg = errData.error?.message || ('HTTP ' + res.status);
                                    return { ok: false, status: res.status, message: errMsg };
                                });
                            }
                            return res.json().then(resData => ({ ok: true, data: resData }));
                        });
                    };

                    makeCall(apiVersion)
                        .then(result => {
                            if (result.ok) return result.data;
                            if (result.status === 404) {
                                if (statusText) statusText.innerHTML = '<div style="width: 12px; height: 12px; border: 2px solid var(--border-color); border-top: 2px solid var(--accent); border-radius: 50%; animation: spin 1s linear infinite;"></div> Retrying with v1beta...';
                                return makeCall('v1beta').then(betaResult => {
                                    if (betaResult.ok) return betaResult.data;
                                    throw new Error(betaResult.message);
                                });
                            }
                            throw new Error(result.message);
                        })
                        .then(resData => {
                            const text = resData.candidates?.[0]?.content?.parts?.[0]?.text;
                            if (!text) throw new Error("Empty response from Gemini API.");

                            if (statusText) statusText.textContent = 'Parsing response...';

                            let jsonText = text.trim();
                            const jsonStart = jsonText.indexOf('{');
                            const jsonEnd = jsonText.lastIndexOf('}');
                            if (jsonStart !== -1 && jsonEnd !== -1 && jsonEnd > jsonStart) {
                                jsonText = jsonText.substring(jsonStart, jsonEnd + 1);
                            }

                            const resultCard = JSON.parse(jsonText);
                            if (!resultCard.id || !resultCard.title || !resultCard.jsCode) {
                                throw new Error("Invalid response schema: missing id, title, or jsCode.");
                            }

                            try {
                                new Function('data', 'container', 'Chart', resultCard.jsCode);
                            } catch(compileErr) {
                                throw new Error("Syntax error in generated JavaScript: " + compileErr.message);
                            }

                            const savedCardsData = clientStorage.getItem('directeur_custom_cards');
                            let savedCards = savedCardsData ? JSON.parse(savedCardsData) : [];

                            savedCards = savedCards.filter(c => c.id !== resultCard.id);
                            savedCards.push(resultCard);
                            clientStorage.setItem('directeur_custom_cards', JSON.stringify(savedCards));

                            alert('Card adjusted successfully!');
                            
                            if (rideData) {
                                renderDashboard(rideData);
                            }
                        })
                        .catch(err => {
                            console.error("Card adjustment error:", err);
                            alert("Failed to adjust card: " + err.message);
                        })
                        .finally(() => {
                            submitBtn.disabled = false;
                            cancelBtn.disabled = false;
                            statusText.textContent = '';
                        });
                });

                try {
                    const renderFunc = new Function('data', 'container', 'Chart', card.jsCode);
                    renderFunc(data, bodyEl, Chart);
                } catch(err) {
                    console.error("Error executing dynamic card " + card.id + ":", err);
                    bodyEl.innerHTML = '<div class="card-error-msg" style="color: #ff6b6b; font-size: 0.85rem; padding: 1rem; border-left: 3px solid #ff6b6b; background: rgba(255, 107, 107, 0.05); border-radius: 4px;">' +
                        '<strong>Render Error:</strong> ' + err.message + '<br>' +
                        '<pre style="font-size:0.75rem; margin-top:0.5rem; overflow-x:auto; color:rgba(255,255,255,0.7);">' + err.stack + '</pre>' +
                        '</div>';
                }

                const isCollapsed = clientStorage.getItem('directeur_card_collapsed_' + card.id) === 'true';
                updateCollapseState(isCollapsed);
            });
        }
        window.renderDynamicCards = renderDynamicCards;

        function deleteDynamicCard(id) {
            const savedCardsData = clientStorage.getItem('directeur_custom_cards');
            let savedCards = savedCardsData ? JSON.parse(savedCardsData) : [];
            savedCards = savedCards.filter(c => c.id !== id);
            clientStorage.setItem('directeur_custom_cards', JSON.stringify(savedCards));
            if (rideData) {
                renderDashboard(rideData);
            }
        }
        window.deleteDynamicCard = deleteDynamicCard;

        function initializeStaticCardsCollapse() {
            const dashboardCards = document.querySelectorAll('#dashboard-view .card');
            dashboardCards.forEach(card => {
                // Skip the evolve-control-panel and dynamic-cards-container cards
                if (card.id === 'evolve-control-panel' || card.closest('#dynamic-cards-container')) return;
                
                const header = card.querySelector('.card-header');
                if (!header) return;

                const titleEl = header.querySelector('.card-title');
                if (!titleEl) return;
                
                const titleText = titleEl.textContent.trim();
                const storageKey = 'directeur_static_card_collapsed_' + titleText.replace(/\s+/g, '_');

                // Add collapse button to card header
                let collapseBtn = header.querySelector('.btn-collapse-static-card');
                if (!collapseBtn) {
                    collapseBtn = document.createElement('button');
                    collapseBtn.className = 'btn-action btn-collapse-static-card';
                    collapseBtn.style.fontSize = '0.75rem';
                    collapseBtn.style.padding = '0.2rem 0.5rem';
                    collapseBtn.style.marginLeft = 'auto'; // push to the right
                    
                    header.style.display = 'flex';
                    header.style.alignItems = 'center';
                    
                    header.appendChild(collapseBtn);
                }

                const updateStaticCollapseState = (collapsed) => {
                    const childrenToToggle = Array.from(card.children).filter(el => el !== header);
                    if (collapsed) {
                        childrenToToggle.forEach(el => {
                            el.style.display = 'none';
                        });
                        collapseBtn.innerHTML = '▼ Show';
                        clientStorage.setItem(storageKey, 'true');
                    } else {
                        childrenToToggle.forEach(el => {
                            el.style.display = '';
                        });
                        collapseBtn.innerHTML = '▲ Collapse';
                        clientStorage.setItem(storageKey, 'false');
                        // Dispatch resize and Leaflet map size invalidation
                        setTimeout(() => {
                            window.dispatchEvent(new Event('resize'));
                            if (typeof leafletMap !== 'undefined' && leafletMap) {
                                leafletMap.invalidateSize();
                            }
                        }, 50);
                    }
                };

                collapseBtn.addEventListener('click', (e) => {
                    e.stopPropagation();
                    const currentlyCollapsed = clientStorage.getItem(storageKey) === 'true';
                    updateStaticCollapseState(!currentlyCollapsed);
                });

                // Apply initial state
                const isCollapsed = clientStorage.getItem(storageKey) === 'true';
                updateStaticCollapseState(isCollapsed);
            });
        }
        window.initializeStaticCardsCollapse = initializeStaticCardsCollapse;

        function showDashboardView() {
            document.getElementById('landing-view').style.display = 'none';
            document.getElementById('calendar-view').style.display = 'none';
            document.getElementById('dashboard-view').style.display = 'block';
            window.dispatchEvent(new Event('resize'));
            
            // Fix Leaflet map sizing/zooming when switching from hidden to visible
            if (leafletMap) {
                leafletMap.invalidateSize();
                if (routePolyline) {
                    leafletMap.fitBounds(routePolyline.getBounds(), { padding: [20, 20] });
                }
            }
        };
        window.showDashboardView = showDashboardView;

        function updateIntervalsSyncUI() {
            const badge = document.getElementById('intervals-status-badge');
            const exportBtn = document.getElementById('btn-intervals-export');
            if (!badge && !exportBtn) return;

            fetch('/api/intervals/config')
                .then(r => r.json())
                .then(data => {
                    if (data.enabled && data.has_api_key) {
                        if (badge) {
                            badge.style.background = 'rgba(46, 204, 113, 0.15)';
                            badge.style.color = '#2ecc71';
                            badge.style.border = '1px solid rgba(46, 204, 113, 0.3)';
                            badge.innerText = 'Connected';
                        }
                        if (exportBtn) {
                            exportBtn.removeAttribute('disabled');
                        }
                    } else {
                        if (badge) {
                            badge.style.background = 'rgba(255,255,255,0.08)';
                            badge.style.color = 'var(--text-secondary)';
                            badge.style.border = '1px solid var(--border-color)';
                            badge.innerText = data.enabled ? 'Missing Key' : 'Not Connected';
                        }
                        if (exportBtn) {
                            exportBtn.setAttribute('disabled', 'true');
                        }
                    }
                })
                .catch(err => {
                    console.error("Error checking Intervals.icu sync status:", err);
                });
        };
        window.updateIntervalsSyncUI = updateIntervalsSyncUI;

        function generateCustomChart() {
            const promptInput = document.getElementById('evolve-prompt');
            const evolvePrompt = promptInput ? promptInput.value.trim() : '';
            if (!evolvePrompt) {
                alert('Please enter a description of the chart or analysis you want to generate.');
                return;
            }

            const key = clientStorage.getItem('gemini_api_key');
            if (!key) {
                alert('Gemini API Key missing! Please configure your API key first.');
                return;
            }

            const modelSelect = document.getElementById('evolve-model-select');
            const model = modelSelect ? modelSelect.value : 'gemini-3.5-flash';

            const evolveLoading = document.getElementById('evolve-loading');
            const statusText = document.getElementById('evolve-status-text');
            const btnEvolve = document.getElementById('btn-evolve-dashboard');

            if (evolveLoading) evolveLoading.style.display = 'flex';
            if (statusText) statusText.textContent = 'Contacting Gemini API...';
            if (btnEvolve) btnEvolve.disabled = true;

            const systemPrompt = "You are a professional cycling data analyst and expert JavaScript developer.\n" +
                "You are tasked with generating a custom data analysis dashboard card for a cycling application.\n\n" +
                "The application has access to a global variable 'rideData' which has the following structure:\n" +
                "{\n" +
                "  \"summary\": {\n" +
                "    \"start_time\": \"2026-06-18T10:00:00Z\",\n" +
                "    \"duration_seconds\": 3600,\n" +
                "    \"distance_meters\": 32000,\n" +
                "    \"average_power\": 210,\n" +
                "    \"max_power\": 850,\n" +
                "    \"normalized_power\": 230,\n" +
                "    \"average_heart_rate\": 145,\n" +
                "    \"max_heart_rate\": 178,\n" +
                "    \"average_cadence\": 85,\n" +
                "    \"max_cadence\": 110,\n" +
                "    \"total_elevation_gain_meters\": 450,\n" +
                "    \"total_shifts\": 210,\n" +
                "    \"total_front_shifts\": 12,\n" +
                "    \"total_rear_shifts\": 198,\n" +
                "    \"power_curve\": { \"1s\": 850, \"5s\": 720, \"1m\": 450, \"5m\": 310, \"20m\": 260 }\n" +
                "  },\n" +
                "  \"records\": [\n" +
                "    { \"time\": 0, \"power\": 150, \"heart_rate\": 110, \"cadence\": 0, \"speed\": 4.5, \"altitude\": 100, \"gear_ratio\": 2.3, \"latitude\": 37.77, \"longitude\": -122.41 },\n" +
                "    { \"time\": 1, \"power\": 210, \"heart_rate\": 112, \"cadence\": 80, \"speed\": 6.2, \"altitude\": 100.1, \"gear_ratio\": 2.3, \"latitude\": 37.771, \"longitude\": -122.411 }\n" +
                "  ],\n" +
                "  \"gear_usage\": [\n" +
                "    { \"combination\": \"50x17\", \"seconds\": 850, \"percentage\": 23.6 }\n" +
                "  ]\n" +
                "}\n\n" +
                "Your output MUST be a valid JSON object matching the following structure and no other text (do NOT wrap in markdown code blocks, do NOT write explanations, just return raw JSON):\n" +
                "{\n" +
                "  \"id\": \"unique-kebab-case-identifier\",\n" +
                "  \"title\": \"A concise title for the card\",\n" +
                "  \"jsCode\": \"// Pure Javascript code here\"\n" +
                "}\n\n" +
                "Rules for 'jsCode':\n" +
                "1. The code will be run as a function body with three parameters: 'data', 'container', and 'Chart'.\n" +
                "2. It must render its visual content inside the 'container' element (which is a standard HTML div).\n" +
                "3. If creating a chart, it must create a '<canvas>' element inside the 'container', style it to fit (e.g. style.height = '350px'), and instantiate a new Chart.js graph using the provided 'Chart' constructor.\n" +
                "4. All HTML elements (like legends, stat boxes, tables) must be generated dynamically using standard JS DOM manipulation methods (e.g. document.createElement, appendChild, innerHTML) and appended to 'container'.\n" +
                "5. Ensure the styling matches a premium dark mode theme (backgrounds should be transparent or dark, colors should be white/var(--text-primary)/var(--accent)/var(--text-secondary), borders should be var(--border-color)).\n" +
                "6. Do NOT load external libraries, CSS, or make API calls. Access all ride metrics exclusively from the 'data' parameter.\n" +
                "7. Calculate any required summaries from the 'data.records' array (e.g. averages, ranges, correlations, distributions) or retrieve them from 'data.summary'.\n" +
                "8. Ensure all variable names are locally scoped (e.g., const, let) and do not clash with global namespaces.\n\n" +
                "Athlete request:\n" + evolvePrompt;

            const apiVersion = model.indexOf('gemini-3') === 0 ? 'v1beta' : 'v1';

            const makeCall = (version) => {
                const callUrl = 'https://generativelanguage.googleapis.com/' + version + '/models/' + model + ':generateContent?key=' + key;
                return fetch(callUrl, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        contents: [{
                            role: 'user',
                            parts: [{ text: systemPrompt }]
                        }]
                    })
                })
                .then(res => {
                    if (!res.ok) {
                        return res.json().then(errData => {
                            const errMsg = errData.error?.message || ('HTTP ' + res.status);
                            return { ok: false, status: res.status, message: errMsg };
                        });
                    }
                    return res.json().then(data => ({ ok: true, data }));
                });
            };

            makeCall(apiVersion)
                .then(result => {
                    if (result.ok) return result.data;
                    if (result.status === 404) {
                        if (statusText) statusText.textContent = 'Retrying with v1beta...';
                        return makeCall('v1beta').then(betaResult => {
                            if (betaResult.ok) return betaResult.data;
                            throw new Error(betaResult.message);
                        });
                    }
                    throw new Error(result.message);
                })
                .then(data => {
                    const text = data.candidates?.[0]?.content?.parts?.[0]?.text;
                    if (!text) throw new Error("Empty response from Gemini API.");

                    if (statusText) statusText.textContent = 'Parsing response...';

                    let jsonText = text.trim();
                    const jsonStart = jsonText.indexOf('{');
                    const jsonEnd = jsonText.lastIndexOf('}');
                    if (jsonStart !== -1 && jsonEnd !== -1 && jsonEnd > jsonStart) {
                        jsonText = jsonText.substring(jsonStart, jsonEnd + 1);
                    }

                    const resultCard = JSON.parse(jsonText);
                    if (!resultCard.id || !resultCard.title || !resultCard.jsCode) {
                        throw new Error("Invalid response schema: missing id, title, or jsCode.");
                    }

                    try {
                        new Function('data', 'container', 'Chart', resultCard.jsCode);
                    } catch(compileErr) {
                        throw new Error("Syntax error in generated JavaScript: " + compileErr.message);
                    }

                    const savedCardsData = clientStorage.getItem('directeur_custom_cards');
                    let savedCards = savedCardsData ? JSON.parse(savedCardsData) : [];

                    savedCards = savedCards.filter(c => c.id !== resultCard.id);
                    savedCards.push(resultCard);
                    clientStorage.setItem('directeur_custom_cards', JSON.stringify(savedCards));

                    if (promptInput) promptInput.value = '';
                    alert('Dashboard evolved successfully! Added card: "' + resultCard.title + '".');
                    
                    if (rideData) {
                        renderDashboard(rideData);
                    }
                })
                .catch(err => {
                    console.error("Dashboard evolution error:", err);
                    alert("Failed to evolve dashboard: " + err.message);
                })
                .finally(() => {
                    if (evolveLoading) evolveLoading.style.display = 'none';
                    if (btnEvolve) btnEvolve.disabled = false;
                });
        }
        window.generateCustomChart = generateCustomChart;

        // Prepare JSON and Schema strings

        // Modal Logic for JSON Viewer
        const jsonModal = document.getElementById('json-modal');
        const btnCopyJson = document.getElementById('btn-copy-json');
        const modalCloseBtn = document.getElementById('modal-close-btn');
        const modalCopyBtn = document.getElementById('modal-copy-btn');
        const modalDownloadBtn = document.getElementById('modal-download-btn');
        const jsonTextarea = document.getElementById('json-textarea');

        if (jsonTextarea) {
            const jsonLines = fullJSONString.split('\n');
            const jsonPreview = jsonLines.slice(0, 100).join('\n') + 
                '\n\n... [Telemetry records truncated for performance. Download the full JSON file or copy it below] ...';
            jsonTextarea.value = jsonPreview;
        }

        if (btnCopyJson) {
            btnCopyJson.addEventListener('click', () => {
                if (jsonModal) jsonModal.style.display = 'flex';
            });
        }

        if (modalCloseBtn) {
            modalCloseBtn.addEventListener('click', () => {
                if (jsonModal) jsonModal.style.display = 'none';
            });
        }

        if (jsonModal) {
            jsonModal.addEventListener('click', (e) => {
                if (e.target === jsonModal) {
                    jsonModal.style.display = 'none';
                }
            });
        }

        if (modalCopyBtn) {
            modalCopyBtn.addEventListener('click', () => {
                navigator.clipboard.writeText(fullJSONString).then(() => {
                    modalCopyBtn.innerText = '✔ Copied!';
                    modalCopyBtn.style.backgroundColor = 'rgba(46, 204, 113, 0.2)';
                    modalCopyBtn.style.borderColor = '#2ecc71';
                    modalCopyBtn.style.color = '#2ecc71';
                    setTimeout(() => {
                        modalCopyBtn.innerText = '📋 Copy Entire JSON';
                        modalCopyBtn.style.backgroundColor = '';
                        modalCopyBtn.style.borderColor = '';
                        modalCopyBtn.style.color = '';
                    }, 2000);
                }).catch(err => {
                    console.error('Could not copy text: ', err);
                    alert('Copying failed, please download the file directly.');
                });
            });
        }

        const downloadJSON = () => {
            const blob = new Blob([fullJSONString], { type: 'application/json' });
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = 'ride_analysis.json';
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);
        };

        if (modalDownloadBtn) {
            modalDownloadBtn.addEventListener('click', downloadJSON);
        }
        const btnDlJson = document.getElementById('btn-download-json');
        if (btnDlJson) {
            btnDlJson.addEventListener('click', downloadJSON);
        }

        // Modal Logic for Schema Viewer
        const schemaModal = document.getElementById('schema-modal');
        const btnViewSchema = document.getElementById('btn-view-schema');
        const schemaCloseBtn = document.getElementById('schema-close-btn');
        const schemaCopyBtn = document.getElementById('schema-copy-btn');
        const schemaDownloadBtn = document.getElementById('schema-download-btn');
        const schemaTextarea = document.getElementById('schema-textarea');

        if (schemaTextarea) {
            schemaTextarea.value = fullSchemaString;
        }

        if (btnViewSchema) {
            btnViewSchema.addEventListener('click', () => {
                if (schemaModal) schemaModal.style.display = 'flex';
            });
        }

        if (schemaCloseBtn) {
            schemaCloseBtn.addEventListener('click', () => {
                if (schemaModal) schemaModal.style.display = 'none';
            });
        }

        if (schemaModal) {
            schemaModal.addEventListener('click', (e) => {
                if (e.target === schemaModal) {
                    schemaModal.style.display = 'none';
                }
            });
        }

        if (schemaCopyBtn) {
            schemaCopyBtn.addEventListener('click', () => {
                navigator.clipboard.writeText(fullSchemaString).then(() => {
                    schemaCopyBtn.innerText = '✔ Copied!';
                    schemaCopyBtn.style.backgroundColor = 'rgba(46, 204, 113, 0.2)';
                    schemaCopyBtn.style.borderColor = '#2ecc71';
                    schemaCopyBtn.style.color = '#2ecc71';
                    setTimeout(() => {
                        schemaCopyBtn.innerText = '📋 Copy Schema';
                        schemaCopyBtn.style.backgroundColor = '';
                        schemaCopyBtn.style.borderColor = '';
                        schemaCopyBtn.style.color = '';
                    }, 2000);
                }).catch(err => {
                    console.error('Could not copy schema: ', err);
                    alert('Copying failed.');
                });
            });
        }

        if (schemaDownloadBtn) {
            schemaDownloadBtn.addEventListener('click', () => {
                const blob = new Blob([fullSchemaString], { type: 'application/json' });
                const url = URL.createObjectURL(blob);
                const a = document.createElement('a');
                a.href = url;
                a.download = 'schema.json';
                document.body.appendChild(a);
                a.click();
                document.body.removeChild(a);
                URL.revokeObjectURL(url);
            });
        }

        // Dropdown Toggle Logic
        const dataDropdown = document.getElementById('data-dropdown');
        const btnDataDropdown = document.getElementById('btn-data-dropdown');
        const dropdownArrow = document.getElementById('dropdown-arrow');

        if (btnDataDropdown && dataDropdown) {
            btnDataDropdown.addEventListener('click', (e) => {
                e.stopPropagation();
                dataDropdown.classList.toggle('active');
                if (dropdownArrow) {
                    if (dataDropdown.classList.contains('active')) {
                        dropdownArrow.style.transform = 'rotate(180deg)';
                    } else {
                        dropdownArrow.style.transform = 'rotate(0deg)';
                    }
                }
            });
        }

        if (dataDropdown) {
            const dropdownItems = dataDropdown.querySelectorAll('.dropdown-item');
            dropdownItems.forEach(item => {
                item.addEventListener('click', () => {
                    dataDropdown.classList.remove('active');
                    if (dropdownArrow) dropdownArrow.style.transform = 'rotate(0deg)';
                });
            });
        }

        window.addEventListener('click', (e) => {
            if (dataDropdown && !dataDropdown.contains(e.target)) {
                dataDropdown.classList.remove('active');
                if (dropdownArrow) dropdownArrow.style.transform = 'rotate(0deg)';
            }
        });

        // ==========================================
        // Saved Data Manager Integration
        // ==========================================
        const savedDataModal = document.getElementById('saved-data-modal');
        const btnShowSavedData = document.getElementById('btn-show-saved-data');
        const savedDataCloseBtn = document.getElementById('saved-data-close-btn');
        const savedDataContent = document.getElementById('saved-data-content');
        const savedDataClearAllBtn = document.getElementById('saved-data-clear-all-btn');
        const savedDataExportBtn = document.getElementById('saved-data-export-btn');
        const savedDataImportBtn = document.getElementById('saved-data-import-btn');
        const savedDataImportFile = document.getElementById('saved-data-import-file');
        const btnExportBackup = document.getElementById('btn-export-backup');
        const btnImportBackup = document.getElementById('btn-import-backup');

        // ==========================================
        // Gemini AI Coach Integration
        // ==========================================
        const coachModal = document.getElementById('coach-modal');
        const btnGeminiCoach = document.getElementById('btn-gemini-coach');
        const coachCloseBtn = document.getElementById('coach-close-btn');
        const coachKeyPanel = document.getElementById('coach-key-panel');
        const coachAnalysisPanel = document.getElementById('coach-analysis-panel');
        const coachKeyInput = document.getElementById('coach-key-input');
        const coachSaveKeyBtn = document.getElementById('coach-save-key-btn');
        const coachClearKeyBtn = document.getElementById('coach-clear-key-btn');
        const coachModelSelect = document.getElementById('coach-model-select');
        let forceSetupView = false;
        let coachChatHistory = [];
        
        const coachGenerateView = document.getElementById('coach-generate-view');
        const coachLoadingView = document.getElementById('coach-loading-view');
        const coachReportView = document.getElementById('coach-report-view');
        const coachRunBtn = document.getElementById('coach-run-btn');
        const coachRegenerateBtn = document.getElementById('coach-regenerate-btn');
        const coachReportContent = document.getElementById('coach-report-content');
        const coachModelUsed = document.getElementById('coach-model-used');
        const coachLoadingStatus = document.getElementById('coach-loading-status');
        const coachChatInput = document.getElementById('coach-chat-input');
        const coachChatSendBtn = document.getElementById('coach-chat-send-btn');

        const formatDuration = (secs) => {
            const roundedSecs = Math.round(secs);
            const h = Math.floor(roundedSecs / 3600);
            const m = Math.floor((roundedSecs % 3600) / 60);
            const s = roundedSecs % 60;
            return h + 'h ' + m + 'm ' + s + 's';
        };

        const formatMarkdown = (text) => {
            if (typeof marked !== 'undefined' && typeof marked.parse === 'function') {
                return marked.parse(text);
            }

            let codeBlocks = [];
            // Extract code blocks to avoid line break transformations inside them
            let tempText = text.replace(/` + "`" + `{3}([\s\S]*?)` + "`" + `{3}/g, (match, code) => {
                codeBlocks.push(code);
                return "__CODE_BLOCK_" + (codeBlocks.length - 1) + "__";
            });

            // Escaping HTML characters in remaining text
            let html = tempText
                .replace(/&/g, "&amp;")
                .replace(/</g, "&lt;")
                .replace(/>/g, "&gt;");

            let lines = html.split(/\r?\n/);
            let htmlResult = [];
            let inList = false;

            for (let i = 0; i < lines.length; i++) {
                let line = lines[i];

                // Check if line is a horizontal rule (3 or more hyphens, asterisks, or underscores)
                let hrMatch = line.match(/^\s*[-*_]{3,}\s*$/);
                if (hrMatch) {
                    if (inList) {
                        htmlResult.push('</ul>');
                        inList = false;
                    }
                    htmlResult.push('<hr style="border: none; border-top: 1px solid rgba(255, 255, 255, 0.1); margin: 1.5rem 0;">');
                    continue;
                }

                // Check if line is a bullet point (starts with '-' or '*' or '+')
                let listMatch = line.match(/^\s*[-*+]\s+(.*)$/);
                if (listMatch) {
                    if (!inList) {
                        inList = true;
                        htmlResult.push('<ul style="margin-bottom: 1rem; padding-left: 1.5rem; list-style-type: disc;">');
                    }
                    let itemContent = listMatch[1];
                    itemContent = itemContent.replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>');
                    itemContent = itemContent.replace(/\*(.*?)\*/g, '<em>$1</em>');
                    htmlResult.push('<li style="margin-bottom: 0.35rem; line-height: 1.5;">' + itemContent + '</li>');
                    continue;
                }

                if (inList) {
                    htmlResult.push('</ul>');
                    inList = false;
                }

                // Check for headers
                let headerMatch = line.match(/^(#{1,6})\s+(.*)$/);
                if (headerMatch) {
                    let level = headerMatch[1].length;
                    let content = headerMatch[2];
                    content = content.replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>');
                    content = content.replace(/\*(.*?)\*/g, '<em>$1</em>');
                    if (level === 1) {
                        htmlResult.push('<h2 style="color: #ffffff; margin-top: 2rem; margin-bottom: 1rem; font-family: \'Outfit\'; font-weight: 800;">' + content + '</h2>');
                    } else if (level === 2) {
                        htmlResult.push('<h3 style="color: var(--accent); margin-top: 1.75rem; margin-bottom: 0.75rem; border-bottom: 1px solid rgba(255,255,255,0.05); padding-bottom: 0.25rem; font-family: \'Outfit\'; font-weight: 700;">' + content + '</h3>');
                    } else {
                        htmlResult.push('<h4 style="color: var(--accent); margin-top: 1.5rem; margin-bottom: 0.5rem; font-family: \'Outfit\'; font-weight: 600;">' + content + '</h4>');
                    }
                    continue;
                }

                // Check if line is empty (paragraph separator)
                if (line.trim() === '') {
                    htmlResult.push('');
                    continue;
                }

                // Regular line of text
                let content = line
                    .replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>')
                    .replace(/\*(.*?)\*/g, '<em>$1</em>');
                htmlResult.push(content);
            }

            if (inList) {
                htmlResult.push('</ul>');
            }

            // Reconstruct paragraphs and block elements
            let finalHtml = [];
            let currentParagraph = [];

            const flushParagraph = () => {
                if (currentParagraph.length > 0) {
                    finalHtml.push('<p style="margin-bottom: 1rem; line-height: 1.6;">' + currentParagraph.join('<br>') + '</p>');
                    currentParagraph = [];
                }
            };

            for (let i = 0; i < htmlResult.length; i++) {
                let item = htmlResult[i];
                if (item === '') {
                    flushParagraph();
                } else if (item.startsWith('<ul') || item.startsWith('</ul>') || item.startsWith('<li') || item.startsWith('<h') || item.startsWith('<hr')) {
                    flushParagraph();
                    finalHtml.push(item);
                } else if (item.startsWith('__CODE_BLOCK_') && item.endsWith('__')) {
                    flushParagraph();
                    finalHtml.push(item);
                } else {
                    currentParagraph.push(item);
                }
            }
            flushParagraph();

            let output = finalHtml.join('\n');

            // Restore code blocks, escaping their content so it doesn't break the HTML
            codeBlocks.forEach((code, idx) => {
                let cleanCode = code
                    .replace(/&/g, "&amp;")
                    .replace(/</g, "&lt;")
                    .replace(/>/g, "&gt;")
                    .trim();
                
                const blockHtml = '<pre style="background: rgba(0,0,0,0.4); border: 1px solid var(--border-color); border-radius: 8px; padding: 1rem; font-family: monospace; font-size: 0.85rem; overflow-x: auto; margin-top: 0.5rem; margin-bottom: 1rem; color: #e2e8f0; line-height: 1.4; white-space: pre;"><code>' + cleanCode + '</code></pre>';
                output = output.replace("__CODE_BLOCK_" + idx + "__", blockHtml);
            });

            return output;
        };

        const loadChatHistoryFromRide = (ride) => {
            if (ride.chatHistory && ride.chatHistory.length > 0) {
                coachChatHistory = JSON.parse(JSON.stringify(ride.chatHistory));
            } else {
                coachChatHistory = [
                    { role: 'user', parts: [{ text: "Analyze my ride telemetry data." }] },
                    { role: 'model', parts: [{ text: ride.report }] }
                ];
            }
        };

        const renderChatHistory = () => {
            if (!coachChatHistory || coachChatHistory.length === 0) {
                coachReportContent.innerHTML = '<div style="font-style: italic; color: var(--text-secondary); text-align: center;">No messages yet.</div>';
                return;
            }

            coachReportContent.innerHTML = '';
            
            const chatListContainer = document.createElement('div');
            chatListContainer.style.display = 'flex';
            chatListContainer.style.flexDirection = 'column';
            chatListContainer.style.gap = '1.25rem';
            chatListContainer.style.width = '100%';
            
            for (let i = 1; i < coachChatHistory.length; i++) {
                const msg = coachChatHistory[i];
                if (!msg || !msg.parts || msg.parts.length === 0) continue;
                const text = msg.parts[0].text;
                const isUser = msg.role === 'user';
                
                const msgWrapper = document.createElement('div');
                msgWrapper.style.display = 'flex';
                msgWrapper.style.flexDirection = 'column';
                msgWrapper.style.width = '100%';
                if (isUser) {
                    msgWrapper.style.alignItems = 'flex-end';
                } else {
                    msgWrapper.style.alignItems = 'flex-start';
                }
                
                const senderLabel = document.createElement('div');
                senderLabel.style.fontSize = '0.75rem';
                senderLabel.style.fontWeight = '600';
                senderLabel.style.marginBottom = '0.25rem';
                senderLabel.style.color = isUser ? 'var(--accent)' : '#9b59b6';
                senderLabel.innerText = isUser ? '👤 You' : '🚴‍♂️ AI Cycling Coach';
                msgWrapper.appendChild(senderLabel);

                const bubble = document.createElement('div');
                bubble.style.maxWidth = '85%';
                bubble.style.padding = '0.85rem 1.1rem';
                bubble.style.borderRadius = '12px';
                bubble.style.fontSize = '0.92rem';
                bubble.style.lineHeight = '1.5';
                
                if (isUser) {
                    bubble.style.background = 'rgba(255, 255, 255, 0.05)';
                    bubble.style.border = '1px solid var(--border-color)';
                    bubble.style.color = '#ffffff';
                    bubble.style.borderRadius = '12px 0px 12px 12px';
                    bubble.innerHTML = formatMarkdown(text);
                } else {
                    bubble.style.background = 'rgba(255, 255, 255, 0.015)';
                    bubble.style.border = '1px solid var(--border-color)';
                    bubble.style.color = '#e2e8f0';
                    bubble.style.borderRadius = '0px 12px 12px 12px';
                    bubble.innerHTML = formatMarkdown(text);
                    if (typeof renderMathInElement === 'function') {
                        renderMathInElement(bubble, {
                            delimiters: [
                                {left: '$$', right: '$$', display: true},
                                {left: '$', right: '$', display: false},
                                {left: '\\(', right: '\\)', display: false},
                                {left: '\\[', right: '\\]', display: true}
                            ],
                            throwOnError: false
                        });
                    }
                }
                
                msgWrapper.appendChild(bubble);
                chatListContainer.appendChild(msgWrapper);
            }
            
            coachReportContent.appendChild(chatListContainer);
            
            setTimeout(() => {
                coachReportContent.scrollTop = coachReportContent.scrollHeight;
            }, 50);
        };

        const setCoachReportContent = (markdownText) => {
            coachReportContent.innerHTML = formatMarkdown(markdownText);
            if (typeof renderMathInElement === 'function') {
                renderMathInElement(coachReportContent, {
                    delimiters: [
                        {left: '$$', right: '$$', display: true},
                        {left: '$', right: '$', display: false},
                        {left: '\\(', right: '\\)', display: false},
                        {left: '\\[', right: '\\]', display: true}
                    ],
                    throwOnError: false
                });
            }
        };

        const renderHistory = () => {
            const historyList = document.getElementById('coach-history-list');
            const clearHistoryBtn = document.getElementById('coach-clear-history-btn');
            if (!historyList) return;
            const historyData = clientStorage.getItem('fit_ride_history');
            
            if (!historyData) {
                historyList.innerHTML = '<div style="font-style: italic; text-align: center; padding-top: 1rem; color: var(--text-secondary);">No previous ride analyses stored. Your first analysis will be saved here automatically.</div>';
                if (clearHistoryBtn) clearHistoryBtn.style.display = 'none';
                return;
            }
            
            try {
                const history = JSON.parse(historyData);
                if (history.length === 0) {
                    historyList.innerHTML = '<div style="font-style: italic; text-align: center; padding-top: 1rem; color: var(--text-secondary);">No previous ride analyses stored. Your first analysis will be saved here automatically.</div>';
                    if (clearHistoryBtn) clearHistoryBtn.style.display = 'none';
                    return;
                }
                
                if (clearHistoryBtn) clearHistoryBtn.style.display = 'inline-block';
                
                // Show most recent first
                const sortedHistory = [...history].reverse();
                
                historyList.innerHTML = sortedHistory.map(ride => {
                    const rideUrl = getRideQueryString(ride.source || 'local', ride.param || '', ride.param2 || '');
                    const onclickAttr = 'onclick="event.preventDefault(); window.viewRideAnalysis(\'' + ride.id + '\')"';
                    return '<a href="' + rideUrl + '" ' + onclickAttr + ' style="display: block; text-decoration: none; background: rgba(255,255,255,0.03); border: 1px solid rgba(255,255,255,0.05); border-radius: 8px; padding: 0.6rem; font-family: sans-serif; line-height: 1.4; text-align: left; cursor: pointer; transition: all 0.2s; margin-bottom: 0.5rem;" onmouseover="this.style.background=\'rgba(255,255,255,0.08)\'; this.style.borderColor=\'var(--accent)\'" onmouseout="this.style.background=\'rgba(255,255,255,0.03)\'; this.style.borderColor=\'rgba(255,255,255,0.05)\'">' +
                        '<div style="display: flex; justify-content: space-between; font-weight: 600; color: #ffffff; font-size: 0.8rem; margin-bottom: 0.25rem;">' +
                            '<span style="color: #ffffff;">📅 ' + ride.date + '</span>' +
                            '<span style="color: var(--accent);">' + ride.distance_km + ' km</span>' +
                        '</div>' +
                        '<div style="font-size: 0.72rem; color: #a0aec0; margin-bottom: 0.35rem;">' +
                            'NP: ' + ride.np + 'W | Avg HR: ' + ride.avg_hr + 'bpm | Gain: ' + ride.elevation_gain + 'm' +
                        '</div>' +
                        '<div style="font-size: 0.72rem; color: #e2e8f0; font-style: italic; border-left: 2px solid rgba(155, 89, 182, 0.5); padding-left: 0.4rem; margin-top: 0.25rem;">' +
                            ride.summary +
                        '</div>' +
                    '</a>';
                }).join('');
            } catch (e) {
                console.error("Error rendering history:", e);
                historyList.innerHTML = '<div style="color: #fc8181; font-size: 0.75rem;">Error loading history.</div>';
                if (clearHistoryBtn) clearHistoryBtn.style.display = 'none';
            }
        };

        const checkCachedReport = (autoNavigate = false) => {
            const currentPlan = clientStorage.getItem('fit_athlete_training_plan') || '';
            const currentModel = coachModelSelect.value;
            const rideId = rideData.summary.start_time;
            const currentNotes = document.getElementById('coach-ride-notes') ? document.getElementById('coach-ride-notes').value.trim() : '';
            const cacheStatus = document.getElementById('coach-cache-status');
            
            const historyData = clientStorage.getItem('fit_ride_history');
            if (historyData) {
                try {
                    const history = JSON.parse(historyData);
                    const existingRide = history.find(r => r.id === rideId);
                    
                    if (existingRide && existingRide.report) {
                        const planMatches = (existingRide.plan || '') === currentPlan;
                        const modelMatches = existingRide.model === currentModel;
                        const notesMatch = (existingRide.notes || '') === currentNotes;
                        
                        if (planMatches && modelMatches && notesMatch) {
                            if (autoNavigate && !forceSetupView) {
                                // Instantly show cached report
                                loadChatHistoryFromRide(existingRide);
                                renderChatHistory();
                                coachModelUsed.innerText = existingRide.model + ' (Cached)';
                                coachGenerateView.style.display = 'none';
                                coachLoadingView.style.display = 'none';
                                coachReportView.style.display = 'flex';
                                return true;
                            } else {
                                // Just show the notice on setup screen
                                if (cacheStatus) {
                                    cacheStatus.innerHTML = 'ℹ️ A report for this ride already exists with these exact goals. <a href="#" id="coach-view-cached-link" style="color: var(--accent); text-decoration: underline; font-weight: 600;">Click here to view it</a>.';
                                    cacheStatus.style.display = 'block';
                                    
                                    document.getElementById('coach-view-cached-link').addEventListener('click', (e) => {
                                        e.preventDefault();
                                        loadChatHistoryFromRide(existingRide);
                                        renderChatHistory();
                                        coachModelUsed.innerText = existingRide.model + ' (Cached)';
                                        coachGenerateView.style.display = 'none';
                                        coachLoadingView.style.display = 'none';
                                        coachReportView.style.display = 'flex';
                                        forceSetupView = false;
                                    });
                                }
                                return true;
                            }
                        } else {
                            if (cacheStatus) {
                                cacheStatus.innerHTML = 'ℹ️ A previous report exists with different goals/model. <a href="#" id="coach-view-cached-link" style="color: #a0aec0; text-decoration: underline;">View previous report</a>.';
                                cacheStatus.style.display = 'block';
                                
                                document.getElementById('coach-view-cached-link').addEventListener('click', (e) => {
                                    e.preventDefault();
                                    loadChatHistoryFromRide(existingRide);
                                    renderChatHistory();
                                    coachModelUsed.innerText = existingRide.model + ' (Old Goals)';
                                    coachGenerateView.style.display = 'none';
                                    coachLoadingView.style.display = 'none';
                                    coachReportView.style.display = 'flex';
                                    forceSetupView = false;
                                });
                            }
                            return false;
                        }
                    }
                } catch (e) {
                    console.error("Error checking cached report:", e);
                }
            }
            
            if (cacheStatus) cacheStatus.style.display = 'none';
            return false;
        };

        // Open Modal and Check Key
        btnGeminiCoach.addEventListener('click', () => {
            coachModal.style.display = 'flex';
            forceSetupView = false;
            const savedKey = clientStorage.getItem('gemini_api_key');
            if (savedKey) {
                coachKeyPanel.style.display = 'none';
                coachAnalysisPanel.style.display = 'flex';
                coachClearKeyBtn.style.display = 'inline-block';
                
                // Load Training Plan and History
                const planInput = document.getElementById('coach-plan-input');
                if (planInput) {
                    planInput.value = clientStorage.getItem('fit_athlete_training_plan') || '';
                }
                
                // Load Ride Notes (keyed by ride start time)
                const rideNotesInput = document.getElementById('coach-ride-notes');
                if (rideNotesInput && rideData && rideData.summary) {
                    const noteKey = 'fit_ride_notes_' + rideData.summary.start_time;
                    rideNotesInput.value = clientStorage.getItem(noteKey) || '';
                    const savedBadge = document.getElementById('coach-notes-saved-badge');
                    if (rideNotesInput.value && savedBadge) {
                        savedBadge.style.display = 'inline';
                    }
                }

                renderHistory();
                checkCachedReport(true);
            } else {
                coachKeyPanel.style.display = 'flex';
                coachAnalysisPanel.style.display = 'none';
                coachClearKeyBtn.style.display = 'none';
            }
        });

        coachCloseBtn.addEventListener('click', () => {
            coachModal.style.display = 'none';
        });

        coachModal.addEventListener('click', (e) => {
            if (e.target === coachModal) {
                coachModal.style.display = 'none';
            }
        });

        // Register plan and history context listeners
        const planInput = document.getElementById('coach-plan-input');
        if (planInput) {
            planInput.addEventListener('input', (e) => {
                clientStorage.setItem('fit_athlete_training_plan', e.target.value);
            });
            planInput.addEventListener('blur', () => {
                checkCachedReport(false);
            });
        }

        // Register ride notes auto-save listener
        const rideNotesInput = document.getElementById('coach-ride-notes');
        if (rideNotesInput) {
            let notesSaveTimeout = null;
            rideNotesInput.addEventListener('input', () => {
                const savedBadge = document.getElementById('coach-notes-saved-badge');
                if (savedBadge) savedBadge.style.display = 'none';
                checkCachedReport(false);
                
                clearTimeout(notesSaveTimeout);
                notesSaveTimeout = setTimeout(() => {
                    if (rideData && rideData.summary) {
                        const noteKey = 'fit_ride_notes_' + rideData.summary.start_time;
                        clientStorage.setItem(noteKey, rideNotesInput.value);
                        if (savedBadge) {
                            savedBadge.style.display = 'inline';
                            setTimeout(() => { savedBadge.style.display = 'none'; }, 2000);
                        }
                    }
                }, 500);
            });
        }

        coachModelSelect.addEventListener('change', () => {
            checkCachedReport(false);
        });

        const clearHistoryBtn = document.getElementById('coach-clear-history-btn');
        if (clearHistoryBtn) {
            clearHistoryBtn.addEventListener('click', () => {
                if (confirm('Are you sure you want to clear your analyzed rides history? This will delete all past coaching summaries from this browser.')) {
                    clientStorage.removeItem('fit_ride_history');
                    renderHistory();
                    checkCachedReport(false);
                }
            });
        }

        // Save & Continue
        coachSaveKeyBtn.addEventListener('click', () => {
            const key = coachKeyInput.value.trim();
            if (key) {
                clientStorage.setItem('gemini_api_key', key);
                coachKeyPanel.style.display = 'none';
                coachAnalysisPanel.style.display = 'flex';
                coachClearKeyBtn.style.display = 'inline-block';
                coachKeyInput.value = '';
            } else {
                alert('Please enter a valid API key.');
            }
        });

        // Clear key
        coachClearKeyBtn.addEventListener('click', () => {
            if (confirm('Clear saved API key?')) {
                clientStorage.removeItem('gemini_api_key');
                coachKeyPanel.style.display = 'flex';
                coachAnalysisPanel.style.display = 'none';
                coachClearKeyBtn.style.display = 'none';
                coachGenerateView.style.display = 'flex';
                coachLoadingView.style.display = 'none';
                coachReportView.style.display = 'none';
            }
        });

        // API Call & Processing
        const setStep = (id, status) => {
            const el = document.getElementById(id);
            if (!el) return;
            const icon = el.querySelector('.step-icon');
            if (status === 'active') {
                el.style.color = '#ffffff';
                if (icon) {
                    icon.innerText = '⚡';
                    icon.style.color = '#9b59b6';
                }
            } else if (status === 'done') {
                el.style.color = '#a0aec0';
                if (icon) {
                    icon.innerText = '✔';
                    icon.style.color = '#2ecc71';
                }
            } else {
                el.style.color = '#4a5568';
                if (icon) {
                    icon.innerText = '○';
                    icon.style.color = '#4a5568';
                }
            }
        };

        const runCoachingAnalysis = () => {
            const key = clientStorage.getItem('gemini_api_key');
            if (!key) {
                alert('API key missing!');
                return;
            }

            const progressBar = document.getElementById('coach-progress-bar');
            progressBar.style.width = '0%';
            setStep('step-downsample', 'waiting');
            setStep('step-prompt', 'waiting');
            setStep('step-api', 'waiting');
            setStep('step-analyze', 'waiting');
            setStep('step-render', 'waiting');

            coachGenerateView.style.display = 'none';
            coachReportView.style.display = 'none';
            coachLoadingView.style.display = 'flex';
            
            // Step 1: Downsampling
            setStep('step-downsample', 'active');
            progressBar.style.width = '10%';

            const rawRecords = rideData.records || [];
            const downsampled = [];
            const interval = Math.max(1, Math.floor(rawRecords.length / 150));
            
            for (let i = 0; i < rawRecords.length; i += interval) {
                const r = rawRecords[i];
                downsampled.push({
                    t_sec: r.elapsed_time_seconds,
                    pwr: r.power,
                    hr: r.heart_rate,
                    cad: r.cadence,
                    spd: parseFloat(r.speed_kmh.toFixed(1)),
                    alt: parseFloat(r.altitude_meters.toFixed(1)),
                    gear: r.front_gear_teeth + 'x' + r.rear_gear_teeth
                });
            }

            // Calculate Drivetrain & Cross-Chaining Analysis for Gemini Coach
            const coachFrontTeethSet = new Set();
            const coachRearTeethSet = new Set();
            rawRecords.forEach(r => {
                if (r.front_gear_teeth > 0) coachFrontTeethSet.add(r.front_gear_teeth);
                if (r.rear_gear_teeth > 0) coachRearTeethSet.add(r.rear_gear_teeth);
            });
            const coachFrontGears = Array.from(coachFrontTeethSet).sort((a, b) => a - b);
            const coachRearGears = Array.from(coachRearTeethSet).sort((a, b) => a - b);
            const coachDrivetrain = {
                frontGears: coachFrontGears,
                rearGears: coachRearGears,
                isDouble: coachFrontGears.length >= 2,
                bigRing: coachFrontGears[coachFrontGears.length - 1] || 0,
                smallRing: coachFrontGears[0] || 0,
                largestRearCogs: coachRearGears.slice(-2),
                smallestRearCogs: coachRearGears.slice(0, 2)
            };

            // Rolling Incline Calculation for prompt context
            const coachGrades = new Array(rawRecords.length).fill(0);
            const coachWindow = 10;
            for (let i = 0; i < rawRecords.length; i++) {
                let prevIdx = Math.max(0, i - coachWindow);
                if (i === prevIdx) {
                    coachGrades[i] = 0;
                    continue;
                }
                const distDiff = rawRecords[i].distance_meters - rawRecords[prevIdx].distance_meters;
                const altDiff = rawRecords[i].altitude_meters - rawRecords[prevIdx].altitude_meters;
                if (distDiff > 0.5) {
                    coachGrades[i] = (altDiff / distDiff) * 100;
                } else {
                    coachGrades[i] = i > 0 ? coachGrades[i - 1] : 0;
                }
            }

            let coachBigBigSeconds = 0;
            let coachSmallSmallSeconds = 0;
            let coachBigBigPowerSum = 0, coachBigBigPowerCount = 0;
            let coachBigBigCadenceSum = 0, coachBigBigCadenceCount = 0;
            let coachBigBigGradeSum = 0, coachBigBigGradeCount = 0;
            
            let coachSmallSmallPowerSum = 0, coachSmallSmallPowerCount = 0;
            let coachSmallSmallCadenceSum = 0, coachSmallSmallCadenceCount = 0;
            let coachSmallSmallGradeSum = 0, coachSmallSmallGradeCount = 0;

            rawRecords.forEach((r, idx) => {
                if (!coachDrivetrain.isDouble) return;
                const isBigBig = r.front_gear_teeth === coachDrivetrain.bigRing && coachDrivetrain.largestRearCogs.includes(r.rear_gear_teeth);
                const isSmallSmall = r.front_gear_teeth === coachDrivetrain.smallRing && coachDrivetrain.smallestRearCogs.includes(r.rear_gear_teeth);
                const curGrade = coachGrades[idx] || 0;

                if (isBigBig) {
                    coachBigBigSeconds++;
                    if (r.power > 0) {
                        coachBigBigPowerSum += r.power;
                        coachBigBigPowerCount++;
                    }
                    if (r.cadence > 0) {
                        coachBigBigCadenceSum += r.cadence;
                        coachBigBigCadenceCount++;
                    }
                    coachBigBigGradeSum += curGrade;
                    coachBigBigGradeCount++;
                } else if (isSmallSmall) {
                    coachSmallSmallSeconds++;
                    if (r.power > 0) {
                        coachSmallSmallPowerSum += r.power;
                        coachSmallSmallPowerCount++;
                    }
                    if (r.cadence > 0) {
                        coachSmallSmallCadenceSum += r.cadence;
                        coachSmallSmallCadenceCount++;
                    }
                    coachSmallSmallGradeSum += curGrade;
                    coachSmallSmallGradeCount++;
                }
            });

            const coachTotalRideSeconds = rideData.summary.duration_seconds || 1;
            const coachBigBigPct = (coachBigBigSeconds / coachTotalRideSeconds) * 100;
            const coachSmallSmallPct = (coachSmallSmallSeconds / coachTotalRideSeconds) * 100;
            const coachAvgBigBigPower = coachBigBigPowerCount > 0 ? Math.round(coachBigBigPowerSum / coachBigBigPowerCount) : 0;
            const coachAvgBigBigCadence = coachBigBigCadenceCount > 0 ? Math.round(coachBigBigCadenceSum / coachBigBigCadenceCount) : 0;
            const coachAvgBigBigGrade = coachBigBigGradeCount > 0 ? (coachBigBigGradeSum / coachBigBigGradeCount) : 0;
            const coachAvgSmallSmallPower = coachSmallSmallPowerCount > 0 ? Math.round(coachSmallSmallPowerSum / coachSmallSmallPowerCount) : 0;
            const coachAvgSmallSmallCadence = coachSmallSmallCadenceCount > 0 ? Math.round(coachSmallSmallCadenceSum / coachSmallSmallCadenceCount) : 0;
            const coachAvgSmallSmallGrade = coachSmallSmallGradeCount > 0 ? (coachSmallSmallGradeSum / coachSmallSmallGradeCount) : 0;

            // Step 2: Packaging
            setStep('step-downsample', 'done');
            setStep('step-prompt', 'active');
            progressBar.style.width = '30%';

            const model = coachModelSelect.value;
            const ftp = athleteFTP;
            const intensityFactor = (rideData.summary.normalized_power / ftp).toFixed(2);
            const tssVal = Math.round((rideData.summary.duration_seconds * rideData.summary.normalized_power * (rideData.summary.normalized_power / ftp)) / (ftp * 36));
            
            // Build Plan and History Context
            let planContext = "";
            const planText = document.getElementById('coach-plan-input') ? document.getElementById('coach-plan-input').value.trim() : "";
            if (planText) {
                planContext = "### Athlete's Training Plan & Goals:\n" + planText + "\n\n";
            }
            
            let historyContext = "";
            const historyData = clientStorage.getItem('fit_ride_history');
            if (historyData) {
                try {
                    const parsedHistory = JSON.parse(historyData);
                    // Filter out current ride to avoid comparison to itself
                    const pastRides = parsedHistory.filter(r => r.id !== rideData.summary.start_time).slice(-5); // get last 5
                    if (pastRides.length > 0) {
                        historyContext = "### Athlete's History (Summaries of Past 5 Analyzed Rides):\n" + 
                            pastRides.map(r => "- Date: " + r.date + " | Distance: " + r.distance_km + " km | NP: " + r.np + " W | Avg HR: " + r.avg_hr + " bpm\n  Coach Recommendation Summary: " + r.summary).join('\n') + '\n\n';
                    }
                } catch (e) {
                    console.error("Error formatting history context:", e);
                }
            }

            // Build Ride Notes context
            let rideNotesContext = "";
            const rideNotesText = document.getElementById('coach-ride-notes') ? document.getElementById('coach-ride-notes').value.trim() : "";
            if (rideNotesText) {
                rideNotesContext = "### Rider's Notes for This Ride:\nThe athlete has provided the following subjective notes about this ride. Consider these when forming your analysis:\n" + rideNotesText + "\n\n";
            }

            const selectedBike = document.getElementById('bike-selector') ? document.getElementById('bike-selector').value : '';
            const bikeLine = selectedBike ? ('- Bike Ridden: ' + selectedBike + '\n') : '- Bike Ridden: Default Gears / Standard Setup\n';

            // Calculate Quadrant Analysis stats for Gemini Coach
            const crankLength = 0.1725;
            const activeRecordsForQuad = (rideData.records || []).filter(r => (r.cadence || 0) > 0 && (r.power || 0) > 0);
            let quadContext = "";
            
            if (activeRecordsForQuad.length > 0) {
                let totalCpv = 0;
                let totalAepf = 0;
                let totalPower = 0;
                let totalCadence = 0;

                const processedPoints = activeRecordsForQuad.map(r => {
                    const cpv = (r.cadence * 2 * Math.PI * crankLength) / 60;
                    const aepf = r.power / cpv;
                    totalCpv += cpv;
                    totalAepf += aepf;
                    totalPower += r.power;
                    totalCadence += r.cadence;
                    return { cpv, aepf };
                });

                const meanCpv = totalCpv / activeRecordsForQuad.length;
                const meanAepf = totalAepf / activeRecordsForQuad.length;
                const meanPower = totalPower / activeRecordsForQuad.length;
                const meanCadence = totalCadence / activeRecordsForQuad.length;

                let q1 = 0, q2 = 0, q3 = 0, q4 = 0;
                processedPoints.forEach(p => {
                    const isHighForce = p.aepf >= meanAepf;
                    const isHighVelocity = p.cpv >= meanCpv;
                    if (isHighForce && isHighVelocity) q1++;
                    else if (isHighForce && !isHighVelocity) q2++;
                    else if (!isHighForce && !isHighVelocity) q3++;
                    else q4++;
                });

                const totalPoints = activeRecordsForQuad.length;
                const pct1 = (q1 / totalPoints) * 100;
                const pct2 = (q2 / totalPoints) * 100;
                const pct3 = (q3 / totalPoints) * 100;
                const pct4 = (q4 / totalPoints) * 100;

                quadContext = "### Neuromuscular vs. Aerobic Load (Quadrant Analysis):\n" +
                    "- Total Active Pedaling Time: " + formatDuration(totalPoints) + "\n" +
                    "- Mean Active Power: " + Math.round(meanPower) + " W\n" +
                    "- Mean Active Cadence: " + Math.round(meanCadence) + " rpm\n" +
                    "- Mean Pedal Velocity (CPV): " + meanCpv.toFixed(2) + " m/s\n" +
                    "- Mean Pedal Force (AEPF): " + Math.round(meanAepf) + " N\n" +
                    "- Quadrant Time Distribution:\n" +
                    "  * QI (High Force / High Velocity - Sprinting, Attacking): " + pct1.toFixed(1) + "%\n" +
                    "  * QII (High Force / Low Velocity - Climbing, Mashing): " + pct2.toFixed(1) + "%\n" +
                    "  * QIII (Low Force / Low Velocity - Recovery, Cruising): " + pct3.toFixed(1) + "%\n" +
                    "  * QIV (Low Force / High Velocity - High-cadence Spinning): " + pct4.toFixed(1) + "%\n\n";
            }

            // Calculate Normalised Cadence stats for Gemini
            const activeCadences = (rideData.records || []).map(r => r.cadence || 0).filter(c => c > 0);
            let normCad = 0;
            let pedMin = 0;
            let pedMax = 0;
            let cadStDev = 0;
            let pedPct = 0;
            
            if (activeCadences.length > 0) {
                const sum = activeCadences.reduce((a, b) => a + b, 0);
                normCad = sum / activeCadences.length;
                pedMin = Math.min(...activeCadences);
                pedMax = Math.max(...activeCadences);
                
                const mean = normCad;
                const squareDiffs = activeCadences.map(c => (c - mean) * (c - mean));
                const avgSquareDiff = squareDiffs.reduce((a, b) => a + b, 0) / squareDiffs.length;
                cadStDev = Math.sqrt(avgSquareDiff);
                pedPct = (activeCadences.length / rideData.records.length) * 100;
            }

            const prompt = 'You are an elite cycling coach. Analyze this ride telemetry data and provide a detailed, constructive, and highly actionable coaching report.\n\n' +
                (planText ? 'CRITICAL: The athlete has provided their specific Training Plan and Goals below. You MUST evaluate this ride directly against their plan, assessing how well they followed it and whether their performance aligns with their goals.\n\n' : '') +
                (historyContext ? 'CRITICAL: You are also provided with a summary of the athlete\'s past rides. Compare their performance on this ride to their previous efforts, highlighting progress, trends, or areas needing attention.\n\n' : '') +
                (rideNotesText ? 'IMPORTANT: The athlete has provided personal notes about this ride. Factor these subjective observations into your analysis and address them directly in your report.\n\n' : '') +
                planContext +
                historyContext +
                rideNotesContext +
                quadContext +
                (rideData.summary.is_zwift ? 'IMPORTANT: This is a Zwift virtual indoor ride. In your analysis, note that shifting is simulated/virtual or done on a stationary smart trainer/smart bike. Do not focus on physical real-world mechanical chain wear or derailleur strain, but analyze shifting/cadence as it relates to keeping a steady virtual training pacing.\n\n' : '') +
                'Here is the telemetry data for the CURRENT ride:\n' +
                bikeLine +
                '- Ride Environment: ' + (rideData.summary.is_zwift ? 'Zwift Indoor Ride (Virtual Environment)' : 'Outdoor Ride') + '\n' +
                '- Start Time: ' + rideData.summary.start_time + '\n' +
                '- Total Distance: ' + (rideData.summary.distance_meters / 1000).toFixed(2) + ' km\n' +
                '- Total Duration: ' + formatDuration(rideData.summary.duration_seconds) + '\n' +
                '- Average Power: ' + Math.round(rideData.summary.average_power) + ' W\n' +
                '- Normalized Power (NP): ' + rideData.summary.normalized_power + ' W\n' +
                '- Intensity Factor (IF): ' + intensityFactor + ' (FTP ' + ftp + 'W)\n' +
                '- Athlete Max Heart Rate: ' + athleteMaxHR + ' bpm\n' +
                '- Training Stress Score (TSS): ' + (isNaN(tssVal) ? '0' : tssVal) + '\n' +
                '- Max Power: ' + rideData.summary.max_power + ' W\n' +
                '- Avg Heart Rate: ' + Math.round(rideData.summary.average_heart_rate) + ' bpm\n' +
                '- Max Heart Rate: ' + rideData.summary.max_heart_rate + ' bpm\n' +
                '- Standard Average Cadence: ' + Math.round(rideData.summary.average_cadence) + ' rpm (includes coasting/zeros)\n' +
                '- Normalised Cadence (Pedalling Only): ' + Math.round(normCad) + ' rpm (excludes coasting/zeros)\n' +
                '- Pedalling Time: ' + Math.round(pedPct) + '% of ride (' + formatDuration(activeCadences.length) + ')\n' +
                '- Pedalling Cadence Range: ' + pedMin + ' - ' + pedMax + ' rpm\n' +
                '- Pedalling Cadence Variability (Standard Deviation): ' + cadStDev.toFixed(1) + ' rpm\n' +
                '- Max Cadence: ' + rideData.summary.max_cadence + ' rpm\n' +
                '- Elevation Gain: ' + Math.round(rideData.summary.total_elevation_gain_meters) + ' m\n' +
                '- Total Shifts: ' + rideData.summary.total_shifts + ' (Front: ' + rideData.summary.total_front_shifts + ', Rear: ' + rideData.summary.total_rear_shifts + ')\n' +
                (coachDrivetrain.isDouble ? (
                    '- Big-Big Cross-Chaining: ' + coachBigBigSeconds + ' seconds (' + coachBigBigPct.toFixed(1) + '% of ride), Avg Power: ' + coachAvgBigBigPower + ' W, Avg Cadence: ' + coachAvgBigBigCadence + ' rpm, Avg Grade: ' + coachAvgBigBigGrade.toFixed(1) + '%\n' +
                    '- Small-Small Cross-Chaining: ' + coachSmallSmallSeconds + ' seconds (' + coachSmallSmallPct.toFixed(1) + '% of ride), Avg Power: ' + coachAvgSmallSmallPower + ' W, Avg Cadence: ' + coachAvgSmallSmallCadence + ' rpm, Avg Grade: ' + coachAvgSmallSmallGrade.toFixed(1) + '%\n'
                ) : '- Bike setup is a 1x single ring; cross-chaining is not applicable.\n') + '\n' +
                'Normalised Cadence Context:\n' +
                '- Normalised Cadence evaluates the athlete\'s cadence ONLY when they are actively pedalling (cadence > 0), filtering out descents, cornering, or coasting.\n' +
                '- Standard Average Cadence is ' + Math.round(rideData.summary.average_cadence) + ' rpm because it includes coasting periods. The difference between standard average cadence and Normalised Cadence shows the proportion of time spent coasting.\n' +
                '- Pedalling Cadence Standard Deviation (StDev) of ' + cadStDev.toFixed(1) + ' rpm tells you how steady their cadence was while pedalling. Low variability (<5 rpm) is indicative of a steady-state time-trialist, while high variability (>10 rpm) suggests poor shifting, erratic pacing, or heavy micro-intervals.\n\n' +
                'Peak Power Profile (Mean Maximal Power):\n' +
                Object.entries(rideData.summary.power_curve || {}).map(entry => '- ' + entry[0] + ': ' + entry[1] + ' W').join('\n') + '\n\n' +
                'Gear Combination Usage:\n' +
                rideData.gear_usage.map(g => '- ' + g.combination + ': ' + g.percentage.toFixed(1) + '% of ride (' + g.seconds + 's)').join('\n') + '\n\n' +
                'Downsampled Timeline (1 point per ' + interval + ' seconds):\n' +
                JSON.stringify(downsampled) + '\n\n' +
                'Please format your coaching report using standard markdown with clear headings, bullet points, and professional cycling terminology. Structure your response into these exact sections:\n' +
                '1. ## Ride Overview & Pacing (NP vs Avg Power, Efficiency Factor, pacing over climbs)\n' +
                (planText ? '2. ## Progress Against Training Plan (Direct comparison of current ride against the athlete\'s stated goals/plan)\n' : '') +
                (historyContext ? '3. ## Progression & Trends (Comparison to previous rides, fitness progression, shifting improvements)\n' : '') +
                '4. ## Cadence, Shifting & Quadrant Analysis (Analyze shift frequency, gear selections on ascents, cadence drops, and neuromuscular vs. aerobic load distribution from the quadrant analysis)\n' +
                '5. ## Cardiovascular vs Power Output (Cardiorespiratory efficiency, decoupling, max HR response)\n' +
                '6. ## Actionable Training Recommendations (Give 3 specific workouts or behavioral changes for future rides based on these findings)\n\n' +
                'Keep the tone supportive, direct, and elite.\n\n' +
                'IMPORTANT: At the very end of your response, add a section starting exactly with [HISTORY_SUMMARY_START] followed by a 2-3 sentence concise summary of this ride\'s performance and key recommendations for the athlete\'s training log, and end it with [HISTORY_SUMMARY_END]. Do not include any formatting or other text inside those tags.';

            coachChatHistory = [
                {
                    role: 'user',
                    parts: [{ text: prompt }]
                }
            ];

            // Step 3: Contacting API
            setStep('step-prompt', 'done');
            setStep('step-api', 'active');
            progressBar.style.width = '50%';

            const callGeminiAPI = (apiVersion) => {
                const url = 'https://generativelanguage.googleapis.com/' + apiVersion + '/models/' + model + ':generateContent?key=' + key;
                return fetch(url, {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify({
                        contents: coachChatHistory
                    })
                })
                .then(res => {
                    if (!res.ok) {
                        return res.json().then(errData => {
                            const errMsg = errData.error?.message || ('HTTP ' + res.status);
                            return { ok: false, status: res.status, message: errMsg };
                        });
                    }
                    return res.json().then(data => ({ ok: true, data }));
                });
            };

            // Set up simulated progress for Steps 4 and 5 while fetch runs
            const step4Timeout = setTimeout(() => {
                setStep('step-api', 'done');
                setStep('step-analyze', 'active');
                progressBar.style.width = '70%';
            }, 1800);

            const step5Timeout = setTimeout(() => {
                setStep('step-analyze', 'done');
                setStep('step-render', 'active');
                progressBar.style.width = '90%';
            }, 4000);

            callGeminiAPI('v1')
                .then(result => {
                    if (result.ok) {
                        return result.data;
                    }
                    if (result.status === 404) {
                        console.log('Model not found on v1 endpoint, trying v1beta...');
                        return callGeminiAPI('v1beta').then(betaResult => {
                            if (betaResult.ok) {
                                return betaResult.data;
                            }
                            throw new Error(betaResult.message);
                        });
                    }
                    throw new Error(result.message);
                })
                .then(data => {
                    let responseText = data.candidates?.[0]?.content?.parts?.[0]?.text;
                    if (!responseText) {
                        throw new Error('Empty response from Gemini API.');
                    }

                    // Clear pending timeouts
                    clearTimeout(step4Timeout);
                    clearTimeout(step5Timeout);

                    // Instantly complete all steps
                    setStep('step-api', 'done');
                    setStep('step-analyze', 'done');
                    setStep('step-render', 'done');
                    progressBar.style.width = '100%';

                    // Extract history summary if present
                    let summaryText = "";
                    const startToken = "[HISTORY_SUMMARY_START]";
                    const endToken = "[HISTORY_SUMMARY_END]";
                    const startIdx = responseText.indexOf(startToken);
                    const endIdx = responseText.indexOf(endToken);
                    
                    if (startIdx !== -1 && endIdx !== -1 && endIdx > startIdx) {
                        summaryText = responseText.substring(startIdx + startToken.length, endIdx).trim();
                        // Remove the summary block from the responseText rendered to the user
                        responseText = responseText.substring(0, startIdx) + responseText.substring(endIdx + endToken.length);
                    } else {
                        // Fallback: extract the first two sentences
                        const cleanText = responseText.replace(/^[#\s\-*]+/gm, '');
                        const sentences = cleanText.split(/[.!?]+/);
                        summaryText = (sentences[0] || "Ride analyzed successfully.").trim() + ". " + (sentences[1] || "").trim();
                        if (summaryText.length > 150) {
                            summaryText = summaryText.substring(0, 147) + "...";
                        }
                    }

                    // Push model response to chat history
                    coachChatHistory.push({
                        role: 'model',
                        parts: [{ text: responseText }]
                    });

                    // Save this ride analysis to local history
                    try {
                        const historyData = clientStorage.getItem('fit_ride_history');
                        let history = [];
                        if (historyData) {
                            history = JSON.parse(historyData);
                        }
                        
                        const rideId = rideData.summary.start_time;
                        const rideDate = new Date(rideId);
                        const existingIdx = history.findIndex(r => {
                            if (r.id === rideId) return true;
                            const d1 = new Date(r.id);
                            const d2 = rideDate;
                            if (isNaN(d1.getTime()) || isNaN(d2.getTime())) return false;
                            return Math.abs(d1.getTime() - d2.getTime()) <= 300000;
                        });
                        
                        const newRecord = {
                            id: rideId,
                            date: new Date(rideId).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' }),
                            distance_km: parseFloat((rideData.summary.distance_meters / 1000).toFixed(2)),
                            duration: formatDuration(rideData.summary.duration_seconds),
                            avg_power: Math.round(rideData.summary.average_power),
                            np: rideData.summary.normalized_power,
                            avg_hr: Math.round(rideData.summary.average_heart_rate),
                            max_hr: rideData.summary.max_heart_rate,
                            elevation_gain: Math.round(rideData.summary.total_elevation_gain_meters),
                            summary: summaryText.trim(),
                            report: responseText,
                            chatHistory: coachChatHistory,
                            plan: planText,
                            notes: rideNotesText,
                            model: model,
                            power_curve: rideData.summary.power_curve,
                            source: currentRideSource || '',
                            param: currentRideParam || '',
                            param2: currentRideParam2 || '',
                            bike: document.getElementById('bike-selector') ? document.getElementById('bike-selector').value : ''
                        };
                        
                        if (existingIdx !== -1) {
                            history[existingIdx] = newRecord;
                        } else {
                            history.push(newRecord);
                        }
                        
                        clientStorage.setItem('fit_ride_history', JSON.stringify(history));
                        try {
                            renderRidesCalendar();
                        } catch(e) {
                            console.error("Error rendering rides calendar after save:", e);
                        }
                    } catch (e) {
                        console.error("Failed to save ride history:", e);
                    }

                    // Short delay to let the user see the 100% complete bar
                    setTimeout(() => {
                        coachChatInput.value = '';
                        renderChatHistory();
                        coachModelUsed.innerText = model;
                        coachLoadingView.style.display = 'none';
                        coachReportView.style.display = 'flex';
                    }, 400);
                })
                .catch(err => {
                    clearTimeout(step4Timeout);
                    clearTimeout(step5Timeout);
                    console.error(err);
                    alert('Error generating coach report: ' + err.message);
                    coachLoadingView.style.display = 'none';
                    coachGenerateView.style.display = 'flex';
                });
        };

        coachRunBtn.addEventListener('click', runCoachingAnalysis);
        
        // When clicking Re-analyze/Edit goals, go back to setup screen
        coachRegenerateBtn.addEventListener('click', () => {
            coachReportView.style.display = 'none';
            coachGenerateView.style.display = 'flex';
        });

        // Follow-up Conversation Chat Handler
        const sendFollowUpMessage = () => {
            const userMsg = coachChatInput.value.trim();
            if (!userMsg) return;

            const key = clientStorage.getItem('gemini_api_key');
            if (!key) {
                alert('API key missing!');
                return;
            }

            const model = coachModelSelect.value;

            // 1. Add message to chat history
            coachChatHistory.push({
                role: 'user',
                parts: [{ text: userMsg }]
            });

            // Clear input and render immediately
            coachChatInput.value = '';
            renderChatHistory();

            // Disable input and button while loading
            coachChatInput.disabled = true;
            coachChatSendBtn.disabled = true;
            
            // Append a temporary loading bubble
            const loadingBubbleWrapper = document.createElement('div');
            loadingBubbleWrapper.id = 'coach-chat-loading-bubble';
            loadingBubbleWrapper.style.display = 'flex';
            loadingBubbleWrapper.style.flexDirection = 'column';
            loadingBubbleWrapper.style.alignItems = 'flex-start';
            loadingBubbleWrapper.style.width = '100%';
            
            const label = document.createElement('div');
            label.style.fontSize = '0.75rem';
            label.style.fontWeight = '600';
            label.style.marginBottom = '0.25rem';
            label.style.color = '#9b59b6';
            label.innerText = '🚴‍♂️ AI Cycling Coach';
            
            const bubble = document.createElement('div');
            bubble.style.maxWidth = '85%';
            bubble.style.padding = '0.85rem 1.1rem';
            bubble.style.borderRadius = '0px 12px 12px 12px';
            bubble.style.fontSize = '0.92rem';
            bubble.style.background = 'rgba(255, 255, 255, 0.015)';
            bubble.style.border = '1px solid var(--border-color)';
            bubble.style.color = 'var(--text-secondary)';
            bubble.innerHTML = '<span style="display: inline-flex; align-items: center; gap: 0.5rem; animation: pulse 1.5s infinite;">⚡ Coach is thinking...</span>';
            
            loadingBubbleWrapper.appendChild(label);
            loadingBubbleWrapper.appendChild(bubble);
            
            const listContainer = coachReportContent.querySelector('div');
            if (listContainer) {
                listContainer.appendChild(loadingBubbleWrapper);
                coachReportContent.scrollTop = coachReportContent.scrollHeight;
            }

            const callGeminiChatAPI = (apiVersion) => {
                const url = 'https://generativelanguage.googleapis.com/' + apiVersion + '/models/' + model + ':generateContent?key=' + key;
                return fetch(url, {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify({
                        contents: coachChatHistory
                    })
                })
                .then(res => {
                    if (!res.ok) {
                        return res.json().then(errData => {
                            const errMsg = errData.error?.message || ('HTTP ' + res.status);
                            return { ok: false, status: res.status, message: errMsg };
                        });
                    }
                    return res.json().then(data => ({ ok: true, data }));
                });
            };

            callGeminiChatAPI('v1')
                .then(result => {
                    if (result.ok) return result.data;
                    if (result.status === 404) {
                        return callGeminiChatAPI('v1beta').then(betaResult => {
                            if (betaResult.ok) return betaResult.data;
                            throw new Error(betaResult.message);
                        });
                    }
                    throw new Error(result.message);
                })
                .then(data => {
                    let responseText = data.candidates?.[0]?.content?.parts?.[0]?.text;
                    if (!responseText) {
                        throw new Error('Empty response from Gemini API.');
                    }

                    // Remove loading bubble
                    const loadingBubble = document.getElementById('coach-chat-loading-bubble');
                    if (loadingBubble) loadingBubble.remove();

                    // Enable inputs
                    coachChatInput.disabled = false;
                    coachChatSendBtn.disabled = false;

                    // Push answer to history
                    coachChatHistory.push({
                        role: 'model',
                        parts: [{ text: responseText }]
                    });

                    // Re-save entire history list to localStorage
                    try {
                        const historyData = clientStorage.getItem('fit_ride_history');
                        if (historyData) {
                            const history = JSON.parse(historyData);
                            const rideId = rideData.summary.start_time;
                            const rideDate = new Date(rideId);
                            const idx = history.findIndex(r => {
                                if (r.id === rideId) return true;
                                const d1 = new Date(r.id);
                                const d2 = rideDate;
                                if (isNaN(d1.getTime()) || isNaN(d2.getTime())) return false;
                                return Math.abs(d1.getTime() - d2.getTime()) <= 300000;
                            });
                            if (idx !== -1) {
                                history[idx].chatHistory = coachChatHistory;
                                clientStorage.setItem('fit_ride_history', JSON.stringify(history));
                            }
                        }
                    } catch (e) {
                        console.error('Failed to update chat history in localStorage:', e);
                    }

                    renderChatHistory();
                    coachChatInput.focus();
                })
                .catch(err => {
                    const loadingBubble = document.getElementById('coach-chat-loading-bubble');
                    if (loadingBubble) loadingBubble.remove();

                    coachChatInput.disabled = false;
                    coachChatSendBtn.disabled = false;

                    coachChatHistory.pop();
                    
                    console.error(err);
                    alert('Error from coach: ' + err.message);
                });
        };

        coachChatSendBtn.addEventListener('click', sendFollowUpMessage);
        coachChatInput.addEventListener('keydown', (e) => {
            if (e.key === 'Enter') {
                sendFollowUpMessage();
            }
        });

        // ==========================================
        // Select Ride Logic
        // ==========================================
        const selectRideModal = document.getElementById('select-ride-modal');
        const btnSelectRide = document.getElementById('btn-select-ride');
        const selectRideCloseBtn = document.getElementById('select-ride-close-btn');
        const tabLocal = document.getElementById('tab-local');
        const tabHammerhead = document.getElementById('tab-hammerhead');
        const tabWahoo = document.getElementById('tab-wahoo');
        const tabIntervals = document.getElementById('tab-intervals');
        const listLocalContainer = document.getElementById('list-local-container');
        const listHammerheadContainer = document.getElementById('list-hammerhead-container');
        const listWahooContainer = document.getElementById('list-wahoo-container');
        const listIntervalsContainer = document.getElementById('list-intervals-container');
        const selectRideLoading = document.getElementById('select-ride-loading');
        const selectRideEmpty = document.getElementById('select-ride-empty');
        const analysisLoadingOverlay = document.getElementById('analysis-loading-overlay');

        let selectRideActiveTab = 'local';

        const updateTabUI = () => {
            // Reset all tabs
            tabLocal.style.color = 'var(--text-secondary)';
            tabLocal.style.borderBottomColor = 'transparent';
            tabLocal.style.fontWeight = '500';

            tabHammerhead.style.color = 'var(--text-secondary)';
            tabHammerhead.style.borderBottomColor = 'transparent';
            tabHammerhead.style.fontWeight = '500';

            tabWahoo.style.color = 'var(--text-secondary)';
            tabWahoo.style.borderBottomColor = 'transparent';
            tabWahoo.style.fontWeight = '500';

            tabIntervals.style.color = 'var(--text-secondary)';
            tabIntervals.style.borderBottomColor = 'transparent';
            tabIntervals.style.fontWeight = '500';

            listLocalContainer.style.display = 'none';
            listHammerheadContainer.style.display = 'none';
            listWahooContainer.style.display = 'none';
            listIntervalsContainer.style.display = 'none';

            if (selectRideActiveTab === 'local') {
                tabLocal.style.color = 'var(--accent)';
                tabLocal.style.borderBottomColor = 'var(--accent)';
                tabLocal.style.fontWeight = '600';
                listLocalContainer.style.display = 'flex';
            } else if (selectRideActiveTab === 'hammerhead') {
                tabHammerhead.style.color = 'var(--accent)';
                tabHammerhead.style.borderBottomColor = 'var(--accent)';
                tabHammerhead.style.fontWeight = '600';
                listHammerheadContainer.style.display = 'flex';
            } else if (selectRideActiveTab === 'wahoo') {
                tabWahoo.style.color = 'var(--accent)';
                tabWahoo.style.borderBottomColor = 'var(--accent)';
                tabWahoo.style.fontWeight = '600';
                listWahooContainer.style.display = 'flex';
            } else if (selectRideActiveTab === 'intervals') {
                tabIntervals.style.color = 'var(--accent)';
                tabIntervals.style.borderBottomColor = 'var(--accent)';
                tabIntervals.style.fontWeight = '600';
                listIntervalsContainer.style.display = 'flex';
            }
        };

        tabLocal.addEventListener('click', () => {
            selectRideActiveTab = 'local';
            updateTabUI();
            checkEmptyState();
        });

        tabHammerhead.addEventListener('click', () => {
            selectRideActiveTab = 'hammerhead';
            updateTabUI();
            checkEmptyState();
        });

        tabWahoo.addEventListener('click', () => {
            selectRideActiveTab = 'wahoo';
            updateTabUI();
            checkEmptyState();
        });

        tabIntervals.addEventListener('click', () => {
            selectRideActiveTab = 'intervals';
            updateTabUI();
            checkEmptyState();
        });

        const checkEmptyState = () => {
            const hasLocal = listLocalContainer.children.length > 0;
            const hasHammerhead = listHammerheadContainer.children.length > 0;
            const hasWahoo = listWahooContainer.children.length > 0;
            const hasIntervals = listIntervalsContainer.children.length > 0;
            if (selectRideActiveTab === 'local') {
                selectRideEmpty.style.display = hasLocal ? 'none' : 'block';
            } else if (selectRideActiveTab === 'hammerhead') {
                selectRideEmpty.style.display = hasHammerhead ? 'none' : 'block';
            } else if (selectRideActiveTab === 'wahoo') {
                selectRideEmpty.style.display = hasWahoo ? 'none' : 'block';
            } else if (selectRideActiveTab === 'intervals') {
                selectRideEmpty.style.display = hasIntervals ? 'none' : 'block';
            }
        };

        function loadRideData(source, param, param2, pushToHistory = true, force = false) {
            selectRideModal.style.display = 'none';
            analysisLoadingOverlay.style.display = 'flex';

            currentRideSource = source;
            currentRideParam = param;
            currentRideParam2 = param2;

            let url = '/api/analyze?source=' + source;
            if (source === 'local') {
                url += '&file=' + encodeURIComponent(param);
            } else if (source === 'hammerhead') {
                url += '&id=' + encodeURIComponent(param);
            } else if (source === 'wahoo') {
                url += '&id=' + encodeURIComponent(param) + '&url=' + encodeURIComponent(param2);
            } else if (source === 'intervals') {
                url += '&id=' + encodeURIComponent(param);
            }

            if (force) {
                url += '&force=true';
            }

            const bikeSelector = document.getElementById('bike-selector');
            const selectedBike = bikeSelector ? bikeSelector.value : '';
            if (selectedBike) {
                url += '&bike=' + encodeURIComponent(selectedBike);
            }

            fetch(url)
                .then(res => {
                    if (!res.ok) {
                        return res.json().then(errData => {
                            throw new Error(errData.error || ('HTTP error ' + res.status));
                        });
                    }
                    return res.json();
                })
                .then(newData => {
                    renderDashboard(newData);

                    if (pushToHistory && typeof getRideQueryString === 'function') {
                        const q = getRideQueryString(source, param, param2);
                        window.history.pushState({source, param, param2}, '', q);
                    }

                    const reparseBtn = document.getElementById('btn-reparse-ride');
                    if (reparseBtn) {
                        reparseBtn.style.display = 'flex';
                    }
                    
                    forceSetupView = true;
                    if (document.getElementById('coach-plan-input')) {
                        document.getElementById('coach-plan-input').value = clientStorage.getItem('fit_athlete_training_plan') || '';
                    }
                    coachGenerateView.style.display = 'flex';
                    coachLoadingView.style.display = 'none';
                    coachReportView.style.display = 'none';
                    renderHistory();
                    checkCachedReport(true);
                })
                .catch(err => {
                    console.error("Failed to analyze ride:", err);
                    alert("Analysis failed: " + err.message);
                })
                .finally(() => {
                    analysisLoadingOverlay.style.display = 'none';
                });
        };

        function reparseCurrentRide() {
            if (!currentRideSource || !currentRideParam) {
                alert("No ride loaded to reparse.");
                return;
            }
            const confirmMsg = "Are you sure you want to force reparse this ride? " + 
                (currentRideSource === 'local' ? 
                 "This will perform a clean reload and reparse of the local FIT file." : 
                 "This will delete the local cached FIT file and download it fresh from the API source.");
            if (confirm(confirmMsg)) {
                loadRideData(currentRideSource, currentRideParam, currentRideParam2, false, true);
            }
        }
        window.reparseCurrentRide = reparseCurrentRide;

        const populateRideLists = (hhPage = 1, wahooPage = 1) => {
            selectRideLoading.style.display = 'flex';
            listLocalContainer.innerHTML = '';
            listHammerheadContainer.innerHTML = '';
            listWahooContainer.innerHTML = '';
            listIntervalsContainer.innerHTML = '';
            selectRideEmpty.style.display = 'none';

            fetch('/api/rides?hh_page=' + hhPage + '&wahoo_page=' + wahooPage)
                .then(res => {
                    if (!res.ok) throw new Error('HTTP error ' + res.status);
                    return res.json();
                })
                .then(data => {
                    window.allRidesData = data;
                    try {
                        renderRidesCalendar();
                    } catch(e) {
                        console.error("Error rendering rides calendar on refresh:", e);
                    }
                    selectRideLoading.style.display = 'none';

                    // Toggle connection error banner
                    const errBanner = document.getElementById('connection-error-banner');
                    if (data.hammerhead_error) {
                        const errMessage = document.getElementById('connection-error-message');
                        const reauthLink = document.getElementById('btn-reauth-banner');
                        if (errBanner && errMessage && reauthLink) {
                            errMessage.textContent = data.hammerhead_error;
                            const authUrl = 'https://api.hammerhead.io/v1/auth/oauth/authorize?client_id=' + encodeURIComponent(data.client_id) + '&redirect_uri=' + encodeURIComponent(window.location.origin + '/callback') + '&response_type=code&scope=activity:read%20route:write&state=directeur';
                            reauthLink.href = authUrl;
                            errBanner.style.display = 'block';
                        }
                    } else if (errBanner) {
                        errBanner.style.display = 'none';
                    }

                    if (data.local && data.local.length > 0) {
                        data.local.forEach(file => {
                            const dateStr = new Date(file.mod_time).toLocaleString();
                            
                            let lengthStr = '';
                            if (file.distance_meters) {
                                lengthStr += (file.distance_meters / 1000).toFixed(1) + ' km';
                            }
                            if (file.duration_seconds) {
                                if (lengthStr) lengthStr += ' | ';
                                lengthStr += formatDuration(file.duration_seconds);
                            }
                            if (!lengthStr) {
                                lengthStr = (file.size_bytes / 1024).toFixed(1) + ' KB';
                            }

                            const item = document.createElement('div');
                            item.className = 'ride-list-item';
                            item.innerHTML = '<div>' +
                                '<div style="font-weight: 600; color: #ffffff; font-size: 0.95rem; margin-bottom: 0.2rem;">' + file.filename + '</div>' +
                                '<div style="font-size: 0.8rem; color: var(--text-secondary);">Modified: ' + dateStr + '</div>' +
                                '</div>' +
                                '<div style="display: flex; align-items: center; gap: 1rem;">' +
                                '<span style="font-size: 0.85rem; color: var(--text-secondary); font-weight: 500;">' + lengthStr + '</span>' +
                                '<span class="badge" style="font-size: 0.7rem; padding: 0.25rem 0.5rem;">FIT</span>' +
                                '</div>';
                            item.addEventListener('click', () => {
                                loadRideData('local', file.filename);
                            });
                            listLocalContainer.appendChild(item);
                        });
                    }

                    // ==========================================
                    // Hammerhead List Rendering
                    // ==========================================
                    if (!data.hammerhead_configured && !data.hammerhead_linked) {
                        const promptCard = document.createElement('div');
                        promptCard.style.background = 'rgba(255, 255, 255, 0.02)';
                        promptCard.style.border = '1px solid var(--border-color)';
                        promptCard.style.borderRadius = '16px';
                        promptCard.style.padding = '1.5rem';
                        promptCard.style.color = 'var(--text-secondary)';
                        promptCard.style.lineHeight = '1.6';
                        promptCard.style.fontSize = '0.9rem';
                        promptCard.style.boxShadow = '0 4px 20px rgba(0, 0, 0, 0.2)';
                        
                        promptCard.innerHTML = '<div style="display: flex; align-items: center; gap: 0.6rem; margin-bottom: 0.75rem; color: var(--accent); font-weight: 700; font-family: \'Outfit\'; font-size: 1.05rem;">' +
                            '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" style="flex-shrink: 0;"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>' +
                            'Hammerhead API Not Configured' +
                            '</div>' +
                            '<p style="margin: 0 0 0.75rem 0; font-size: 0.85rem; color: #ffffff; font-weight: 600;">Method A: OAuth Connection (Recommended & Auto-Refreshing)</p>' +
                            '<ol style="margin: 0 1.25rem 1.25rem 1.25rem; padding-left: 1.25rem; font-size: 0.82rem; display: flex; flex-direction: column; gap: 0.4rem; color: var(--text-secondary);">' +
                            '<li>Log in to the <a href="https://dashboard.hammerhead.io/" target="_blank" style="color: var(--accent); text-decoration: none; font-weight: 600; border-bottom: 1px dotted var(--accent);">Hammerhead Dashboard</a>.</li>' +
                            '<li>Navigate to settings and register a developer application.</li>' +
                            '<li>Add <code>' + window.location.origin + '/callback</code> as a callback URL.</li>' +
                            '<li>Add the generated <code>client_id</code> and <code>client_secret</code> to <code>{{.ConfigPath}}</code> under <code>"hammerhead_api"</code> and restart the server.</li>' +
                            '</ol>' +
                            '<p style="margin: 0 0 0.75rem 0; font-size: 0.85rem; color: #ffffff; font-weight: 600;">Method B: Manual Session Token (Expires after 1 hour)</p>' +
                            '<ol style="margin: 0; padding-left: 1.25rem; font-size: 0.82rem; display: flex; flex-direction: column; gap: 0.4rem; color: var(--text-secondary);">' +
                            '<li>Log in to the <a href="https://dashboard.hammerhead.io/" target="_blank" style="color: var(--accent); text-decoration: none; font-weight: 600; border-bottom: 1px dotted var(--accent);">Hammerhead Dashboard</a>.</li>' +
                            '<li>Open Developer Tools (press <kbd style="background: rgba(255, 255, 255, 0.1); border: 1px solid rgba(255, 255, 255, 0.15); border-radius: 4px; padding: 1px 5px; font-family: monospace; font-size: 0.75rem; color: #ffffff;">F12</kbd>).</li>' +
                            '<li>Switch to the <strong>Network</strong> tab, refresh, and filter requests by <code>activities</code>.</li>' +
                            '<li>Select the request, copy the token string after <code>Bearer </code> in the <code>Authorization</code> header.</li>' +
                            '<li>Paste it into <code>{{.ConfigPath}}</code> under <code>"auth_token"</code>, set <code>"enabled": true</code>, and restart the server.</li>' +
                            '</ol>';
                        listHammerheadContainer.appendChild(promptCard);
                    } else if (data.hammerhead_configured && !data.hammerhead_linked) {
                        const linkCard = document.createElement('div');
                        linkCard.style.background = 'rgba(255, 255, 255, 0.02)';
                        linkCard.style.border = '1px solid var(--border-color)';
                        linkCard.style.borderRadius = '16px';
                        linkCard.style.padding = '2.5rem 1.5rem';
                        linkCard.style.color = 'var(--text-secondary)';
                        linkCard.style.lineHeight = '1.6';
                        linkCard.style.fontSize = '0.9rem';
                        linkCard.style.textAlign = 'center';
                        linkCard.style.display = 'flex';
                        linkCard.style.flexDirection = 'column';
                        linkCard.style.alignItems = 'center';
                        linkCard.style.gap = '1rem';
                        linkCard.style.boxShadow = '0 4px 20px rgba(0, 0, 0, 0.2)';
                        
                        const authUrl = 'https://api.hammerhead.io/v1/auth/oauth/authorize?client_id=' + encodeURIComponent(data.client_id) + '&redirect_uri=' + encodeURIComponent(window.location.origin + '/callback') + '&response_type=code&scope=activity:read%20route:write&state=directeur';
                        
                        linkCard.innerHTML = '<div style="font-size: 1.15rem; font-weight: 700; color: #ffffff; font-family: \'Outfit\';">Link Hammerhead Account</div>' +
                            '<p style="margin: 0; font-size: 0.85rem; color: var(--text-secondary); max-width: 400px;">Connect your Hammerhead account to directeurAI to view your Karoo activities and download telemetry logs automatically.</p>' +
                            '<a href="' + authUrl + '" class="btn-action" style="margin-top: 0.5rem; text-decoration: none; display: inline-flex; align-items: center; gap: 0.5rem; font-weight: 600; padding: 0.75rem 2rem; background: linear-gradient(135deg, var(--accent), #f1c40f); border: none; color: #ffffff; border-radius: 12px; box-shadow: 0 4px 15px var(--accent-glow); transition: transform 0.2s;">' +
                            '🔗 Authorize directeurAI' +
                            '</a>';
                        listHammerheadContainer.appendChild(linkCard);
                    } else if (data.hammerhead_error) {
                        const errorCard = document.createElement('div');
                        errorCard.style.background = 'rgba(231, 76, 60, 0.05)';
                        errorCard.style.border = '1px solid #e74c3c';
                        errorCard.style.borderRadius = '16px';
                        errorCard.style.padding = '1.5rem';
                        errorCard.style.color = 'var(--text-secondary)';
                        errorCard.style.lineHeight = '1.6';
                        errorCard.style.fontSize = '0.9rem';
                        errorCard.style.boxShadow = '0 4px 20px rgba(231, 76, 60, 0.1)';
                        
                        let reAuthHtml = '';
                        if (data.hammerhead_configured) {
                            const authUrl = 'https://api.hammerhead.io/v1/auth/oauth/authorize?client_id=' + encodeURIComponent(data.client_id) + '&redirect_uri=' + encodeURIComponent(window.location.origin + '/callback') + '&response_type=code&scope=activity:read%20route:write&state=directeur';
                            reAuthHtml = '<div style="margin-top: 1.25rem; border-top: 1px solid rgba(231, 76, 60, 0.15); padding-top: 1.25rem; text-align: center;">' +
                                '<a href="' + authUrl + '" class="btn-action" style="text-decoration: none; display: inline-flex; align-items: center; gap: 0.5rem; font-weight: 600; padding: 0.6rem 1.5rem; background: rgba(231, 76, 60, 0.15); border: 1px solid #e74c3c; color: #ffffff; border-radius: 10px; font-size: 0.8rem; transition: background 0.2s;">' +
                                '🔗 Re-authorize Account' +
                                '</a>' +
                                '</div>';
                        }
                        
                        errorCard.innerHTML = '<div style="display: flex; align-items: center; gap: 0.6rem; margin-bottom: 0.75rem; color: #e74c3c; font-weight: 700; font-family: \'Outfit\'; font-size: 1.05rem;">' +
                            '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" style="flex-shrink: 0;"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="8" x2="12" y2="12"></line><line x1="12" y1="16" x2="12.01" y2="16"></line></svg>' +
                            'Failed to Fetch Hammerhead Activities' +
                            '</div>' +
                            '<p style="margin: 0 0 1rem 0; font-size: 0.85rem; color: #ffffff;">The Hammerhead API returned an error. This usually indicates that your authentication key is invalid or has expired.</p>' +
                            '<div style="background: rgba(0, 0, 0, 0.3); padding: 0.75rem 1rem; border-radius: 8px; font-family: monospace; font-size: 0.8rem; color: #e74c3c; word-break: break-all; margin-bottom: 1.25rem; border: 1px solid rgba(231, 76, 60, 0.2);">' +
                            data.hammerhead_error +
                            '</div>' +
                            '<p style="margin: 0 0 1rem 0; font-size: 0.85rem; color: #ffffff;">Please follow the setup instructions below to retrieve a fresh token:</p>' +
                            '<ol style="margin: 0; padding-left: 1.25rem; font-size: 0.82rem; display: flex; flex-direction: column; gap: 0.6rem; color: var(--text-secondary);">' +
                            '<li>Log in to the <a href="https://dashboard.hammerhead.io/" target="_blank" style="color: var(--accent); text-decoration: none; font-weight: 600; border-bottom: 1px dotted var(--accent); transition: color 0.2s;">Hammerhead Dashboard</a>.</li>' +
                            '<li>Open Developer Tools (press <kbd style="background: rgba(255, 255, 255, 0.1); border: 1px solid rgba(255, 255, 255, 0.15); border-radius: 4px; padding: 1px 5px; font-family: monospace; font-size: 0.75rem; color: #ffffff; box-shadow: 0 1px 2px rgba(0,0,0,0.4);">F12</kbd> or <kbd style="background: rgba(255, 255, 255, 0.1); border: 1px solid rgba(255, 255, 255, 0.15); border-radius: 4px; padding: 1px 5px; font-family: monospace; font-size: 0.75rem; color: #ffffff; box-shadow: 0 1px 2px rgba(0,0,0,0.4);">Cmd+Opt+I</kbd>).</li>' +
                            '<li>Switch to the <strong>Network</strong> tab.</li>' +
                            '<li>Refresh the page or click "Activities" on the dashboard.</li>' +
                            '<li>Filter/search requests by <code>activities</code>.</li>' +
                            '<li>Select the request and find the <strong>Request Headers</strong>.</li>' +
                            '<li>Copy the token string after <code>Bearer </code> in the <code>Authorization</code> header.</li>' +
                            '<li>Paste it into <code>{{.ConfigPath}}</code> under <code>"hammerhead_api"</code> &rarr; <code>"auth_token"</code>, set <code>"enabled": true</code>, and restart the server.</li>' +
                            '</ol>' +
                            reAuthHtml;
                        listHammerheadContainer.appendChild(errorCard);
                    } else {
                        const manageHeader = document.createElement('div');
                        manageHeader.style.display = 'flex';
                        manageHeader.style.justifyContent = 'space-between';
                        manageHeader.style.alignItems = 'center';
                        manageHeader.style.marginBottom = '1rem';
                        manageHeader.style.padding = '0.5rem 1rem';
                        manageHeader.style.background = 'rgba(255, 255, 255, 0.02)';
                        manageHeader.style.borderRadius = '12px';
                        manageHeader.style.border = '1px solid var(--border-color)';
                        
                        const titleSpan = document.createElement('span');
                        titleSpan.innerText = 'Account Linked';
                        titleSpan.style.color = 'var(--text-secondary)';
                        titleSpan.style.fontSize = '0.85rem';
                        titleSpan.style.fontWeight = '600';
                        
                        const unlinkBtn = document.createElement('button');
                        unlinkBtn.innerText = 'Unlink Account';
                        unlinkBtn.className = 'btn-action';
                        unlinkBtn.style.padding = '0.3rem 0.8rem';
                        unlinkBtn.style.fontSize = '0.75rem';
                        unlinkBtn.style.background = 'rgba(231, 76, 60, 0.15)';
                        unlinkBtn.style.border = '1px solid #e74c3c';
                        unlinkBtn.style.color = '#e74c3c';
                        unlinkBtn.style.borderRadius = '6px';
                        unlinkBtn.addEventListener('click', (e) => {
                            e.stopPropagation();
                            if (confirm('Are you sure you want to unlink your Hammerhead account?')) {
                                fetch('/api/hammerhead/unlink', { method: 'POST' })
                                    .then(res => res.json())
                                    .then(() => {
                                        populateRideLists(1, 1);
                                    })
                                    .catch(err => alert('Error unlinking account: ' + err));
                            }
                        });
                        
                        manageHeader.appendChild(titleSpan);
                        manageHeader.appendChild(unlinkBtn);
                        listHammerheadContainer.appendChild(manageHeader);

                        if (data.hammerhead && data.hammerhead.length > 0) {
                            data.hammerhead.forEach(act => {
                                const dateStr = act.startTime ? new Date(act.startTime).toLocaleString() : 'N/A';
                                const distStr = (act.distance / 1000).toFixed(2) + ' km';
                                const durStr = formatDuration(act.duration);
                                const item = document.createElement('div');
                                item.className = 'ride-list-item';
                                item.innerHTML = '<div>' +
                                    '<div style="font-weight: 600; color: #ffffff; font-size: 0.95rem; margin-bottom: 0.2rem;">' + (act.name || 'Unnamed Activity') + '</div>' +
                                    '<div style="font-size: 0.8rem; color: var(--text-secondary);">Date: ' + dateStr + '</div>' +
                                    '</div>' +
                                    '<div style="display: flex; align-items: center; gap: 1rem;">' +
                                    '<div style="text-align: right;">' +
                                    '<div style="font-weight: 600; color: var(--accent); font-size: 0.9rem;">' + distStr + '</div>' +
                                    '<div style="font-size: 0.75rem; color: var(--text-secondary);">' + durStr + '</div>' +
                                    '</div>' +
                                    '<span class="badge" style="font-size: 0.7rem; padding: 0.25rem 0.5rem; background: linear-gradient(135deg, rgba(245, 196, 0, 0.2), rgba(230, 126, 34, 0.2)); border-color: #f1c40f; color: #f1c40f;">KAROO</span>' +
                                    '</div>';
                                item.addEventListener('click', () => {
                                    loadRideData('hammerhead', act.id);
                                });
                                listHammerheadContainer.appendChild(item);
                            });

                            if (data.total_pages > 1) {
                                const paginationDiv = document.createElement('div');
                                paginationDiv.style.display = 'flex';
                                paginationDiv.style.justifyContent = 'center';
                                paginationDiv.style.alignItems = 'center';
                                paginationDiv.style.gap = '1rem';
                                paginationDiv.style.marginTop = '1rem';
                                paginationDiv.style.padding = '0.75rem 0 0 0';
                                paginationDiv.style.borderTop = '1px solid var(--border-color)';
                                paginationDiv.style.width = '100%';
                                
                                const prevBtn = document.createElement('button');
                                prevBtn.innerText = '◀ Prev';
                                prevBtn.className = 'btn-action';
                                prevBtn.style.padding = '0.35rem 0.8rem';
                                prevBtn.style.fontSize = '0.78rem';
                                prevBtn.style.borderRadius = '6px';
                                prevBtn.disabled = data.current_page <= 1;
                                if (prevBtn.disabled) {
                                    prevBtn.style.opacity = '0.3';
                                    prevBtn.style.cursor = 'not-allowed';
                                } else {
                                    prevBtn.addEventListener('click', (e) => {
                                        e.stopPropagation();
                                        populateRideLists(data.current_page - 1, wahooPage);
                                    });
                                }
                                
                                const pageInfo = document.createElement('span');
                                pageInfo.innerText = 'Page ' + data.current_page + ' / ' + data.total_pages;
                                pageInfo.style.fontSize = '0.8rem';
                                pageInfo.style.color = 'var(--text-secondary)';
                                pageInfo.style.fontWeight = '500';
                                
                                const nextBtn = document.createElement('button');
                                nextBtn.innerText = 'Next ▶';
                                nextBtn.className = 'btn-action';
                                nextBtn.style.padding = '0.35rem 0.8rem';
                                nextBtn.style.fontSize = '0.78rem';
                                nextBtn.style.borderRadius = '6px';
                                nextBtn.disabled = data.current_page >= data.total_pages;
                                if (nextBtn.disabled) {
                                    nextBtn.style.opacity = '0.3';
                                    nextBtn.style.cursor = 'not-allowed';
                                } else {
                                    nextBtn.addEventListener('click', (e) => {
                                        e.stopPropagation();
                                        populateRideLists(data.current_page + 1, wahooPage);
                                    });
                                }
                                
                                paginationDiv.appendChild(prevBtn);
                                paginationDiv.appendChild(pageInfo);
                                paginationDiv.appendChild(nextBtn);
                                listHammerheadContainer.appendChild(paginationDiv);
                            }
                        } else {
                            const emptyItem = document.createElement('div');
                            emptyItem.style.textAlign = 'center';
                            emptyItem.style.color = 'var(--text-secondary)';
                            emptyItem.style.padding = '3rem 0';
                            emptyItem.style.fontStyle = 'italic';
                            emptyItem.style.fontSize = '0.9rem';
                            emptyItem.innerText = 'No Hammerhead activities found.';
                            listHammerheadContainer.appendChild(emptyItem);
                        }
                    }

                    // ==========================================
                    // Wahoo List Rendering
                    // ==========================================
                    if (!data.wahoo_configured && !data.wahoo_linked) {
                        const promptCard = document.createElement('div');
                        promptCard.style.background = 'rgba(255, 255, 255, 0.02)';
                        promptCard.style.border = '1px solid var(--border-color)';
                        promptCard.style.borderRadius = '16px';
                        promptCard.style.padding = '1.5rem';
                        promptCard.style.color = 'var(--text-secondary)';
                        promptCard.style.lineHeight = '1.6';
                        promptCard.style.fontSize = '0.9rem';
                        promptCard.style.boxShadow = '0 4px 20px rgba(0, 0, 0, 0.2)';
                        
                        promptCard.innerHTML = '<div style="display: flex; align-items: center; gap: 0.6rem; margin-bottom: 0.75rem; color: #9b59b6; font-weight: 700; font-family: \'Outfit\'; font-size: 1.05rem;">' +
                            '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" style="flex-shrink: 0;"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>' +
                            'Wahoo API Not Configured' +
                            '</div>' +
                            '<p style="margin: 0 0 0.75rem 0; font-size: 0.85rem; color: #ffffff; font-weight: 600;">How to Connect Wahoo Fitness API:</p>' +
                            '<ol style="margin: 0; padding-left: 1.25rem; font-size: 0.82rem; display: flex; flex-direction: column; gap: 0.4rem; color: var(--text-secondary);">' +
                            '<li>Log in to the <a href="https://developers.wahooligan.com/" target="_blank" style="color: #9b59b6; text-decoration: none; font-weight: 600; border-bottom: 1px dotted #9b59b6;">Wahoo Developer Portal</a>.</li>' +
                            '<li>Register a developer application.</li>' +
                            '<li>Add <code>' + window.location.origin + '/wahoo-callback</code> as a callback URL.</li>' +
                            '<li>Add the generated <code>client_id</code> and <code>client_secret</code> to <code>{{.ConfigPath}}</code> under <code>"wahoo_api"</code> and restart the server.</li>' +
                            '</ol>';
                        listWahooContainer.appendChild(promptCard);
                    } else if (data.wahoo_configured && !data.wahoo_linked) {
                        const linkCard = document.createElement('div');
                        linkCard.style.background = 'rgba(255, 255, 255, 0.02)';
                        linkCard.style.border = '1px solid var(--border-color)';
                        linkCard.style.borderRadius = '16px';
                        linkCard.style.padding = '2.5rem 1.5rem';
                        linkCard.style.color = 'var(--text-secondary)';
                        linkCard.style.lineHeight = '1.6';
                        linkCard.style.fontSize = '0.9rem';
                        linkCard.style.textAlign = 'center';
                        linkCard.style.display = 'flex';
                        linkCard.style.flexDirection = 'column';
                        linkCard.style.alignItems = 'center';
                        linkCard.style.gap = '1rem';
                        linkCard.style.boxShadow = '0 4px 20px rgba(0, 0, 0, 0.2)';
                        
                        const authUrl = 'https://api.wahooligan.com/oauth/authorize?client_id=' + encodeURIComponent(data.wahoo_client_id) + '&redirect_uri=' + encodeURIComponent(window.location.origin + '/wahoo-callback') + '&response_type=code&scope=workouts_read&state=directeur';
                        
                        linkCard.innerHTML = '<div style="font-size: 1.15rem; font-weight: 700; color: #ffffff; font-family: \'Outfit\';">Link Wahoo Account</div>' +
                            '<p style="margin: 0; font-size: 0.85rem; color: var(--text-secondary); max-width: 400px;">Connect your Wahoo Fitness account to directeurAI to view and import your activities automatically.</p>' +
                            '<a href="' + authUrl + '" class="btn-action" style="margin-top: 0.5rem; text-decoration: none; display: inline-flex; align-items: center; gap: 0.5rem; font-weight: 600; padding: 0.75rem 2rem; background: linear-gradient(135deg, #9b59b6, #3498db); border: none; color: #ffffff; border-radius: 12px; box-shadow: 0 4px 15px rgba(155, 89, 182, 0.4); transition: transform 0.2s;">' +
                            '🔗 Authorize Wahoo Fitness' +
                            '</a>';
                        listWahooContainer.appendChild(linkCard);
                    } else if (data.wahoo_error) {
                        const errorCard = document.createElement('div');
                        errorCard.style.background = 'rgba(231, 76, 60, 0.05)';
                        errorCard.style.border = '1px solid #e74c3c';
                        errorCard.style.borderRadius = '16px';
                        errorCard.style.padding = '1.5rem';
                        errorCard.style.color = 'var(--text-secondary)';
                        errorCard.style.lineHeight = '1.6';
                        errorCard.style.fontSize = '0.9rem';
                        errorCard.style.boxShadow = '0 4px 20px rgba(231, 76, 60, 0.1)';
                        
                        let reAuthHtml = '';
                        if (data.wahoo_configured) {
                            const authUrl = 'https://api.wahooligan.com/oauth/authorize?client_id=' + encodeURIComponent(data.wahoo_client_id) + '&redirect_uri=' + encodeURIComponent(window.location.origin + '/wahoo-callback') + '&response_type=code&scope=workouts_read&state=directeur';
                            reAuthHtml = '<div style="margin-top: 1.25rem; border-top: 1px solid rgba(231, 76, 60, 0.15); padding-top: 1.25rem; text-align: center;">' +
                                '<a href="' + authUrl + '" class="btn-action" style="text-decoration: none; display: inline-flex; align-items: center; gap: 0.5rem; font-weight: 600; padding: 0.6rem 1.5rem; background: rgba(231, 76, 60, 0.15); border: 1px solid #e74c3c; color: #ffffff; border-radius: 10px; font-size: 0.8rem; transition: background 0.2s;">' +
                                '🔗 Re-authorize Account' +
                                '</a>' +
                                '</div>';
                        }
                        
                        errorCard.innerHTML = '<div style="display: flex; align-items: center; gap: 0.6rem; margin-bottom: 0.75rem; color: #e74c3c; font-weight: 700; font-family: \'Outfit\'; font-size: 1.05rem;">' +
                            '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" style="flex-shrink: 0;"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="8" x2="12" y2="12"></line><line x1="12" y1="16" x2="12.01" y2="16"></line></svg>' +
                            'Failed to Fetch Wahoo Workouts' +
                            '</div>' +
                            '<p style="margin: 0 0 1rem 0; font-size: 0.85rem; color: #ffffff;">The Wahoo API returned an error. This usually indicates that your authentication key is invalid or has expired.</p>' +
                            '<div style="background: rgba(0, 0, 0, 0.3); padding: 0.75rem 1rem; border-radius: 8px; font-family: monospace; font-size: 0.8rem; color: #e74c3c; word-break: break-all; margin-bottom: 1.25rem; border: 1px solid rgba(231, 76, 60, 0.2);">' +
                            data.wahoo_error +
                            '</div>' +
                            reAuthHtml;
                        listWahooContainer.appendChild(errorCard);
                    } else {
                        if (data.wahoo && data.wahoo.length > 0) {
                            data.wahoo.forEach(act => {
                                const dateStr = act.starts ? new Date(act.starts).toLocaleString() : 'N/A';
                                const distStr = (act.distance / 1000).toFixed(2) + ' km';
                                const durStr = formatDuration(act.duration_active);
                                const item = document.createElement('div');
                                item.className = 'ride-list-item';
                                item.innerHTML = '<div>' +
                                    '<div style="font-weight: 600; color: #ffffff; font-size: 0.95rem; margin-bottom: 0.2rem;">' + (act.name || 'Unnamed Activity') + '</div>' +
                                    '<div style="font-size: 0.8rem; color: var(--text-secondary);">Date: ' + dateStr + '</div>' +
                                    '</div>' +
                                    '<div style="display: flex; align-items: center; gap: 1rem;">' +
                                    '<div style="text-align: right;">' +
                                    '<div style="font-weight: 600; color: var(--accent); font-size: 0.9rem;">' + distStr + '</div>' +
                                    '<div style="font-size: 0.75rem; color: var(--text-secondary);">' + durStr + '</div>' +
                                    '</div>' +
                                    '<span class="badge" style="font-size: 0.7rem; padding: 0.25rem 0.5rem; background: linear-gradient(135deg, rgba(155, 89, 182, 0.2), rgba(52, 152, 219, 0.2)); border-color: #9b59b6; color: #e0aaff;">WAHOO</span>' +
                                    '</div>';
                                item.addEventListener('click', () => {
                                    loadRideData('wahoo', act.id, act.file.url);
                                });
                                listWahooContainer.appendChild(item);
                            });

                            if (data.wahoo_total_pages > 1) {
                                const paginationDiv = document.createElement('div');
                                paginationDiv.style.display = 'flex';
                                paginationDiv.style.justifyContent = 'center';
                                paginationDiv.style.alignItems = 'center';
                                paginationDiv.style.gap = '1rem';
                                paginationDiv.style.marginTop = '1rem';
                                paginationDiv.style.padding = '0.75rem 0 0 0';
                                paginationDiv.style.borderTop = '1px solid var(--border-color)';
                                paginationDiv.style.width = '100%';
                                
                                const prevBtn = document.createElement('button');
                                prevBtn.innerText = '◀ Prev';
                                prevBtn.className = 'btn-action';
                                prevBtn.style.padding = '0.35rem 0.8rem';
                                prevBtn.style.fontSize = '0.78rem';
                                prevBtn.style.borderRadius = '6px';
                                prevBtn.disabled = data.wahoo_current_page <= 1;
                                if (prevBtn.disabled) {
                                    prevBtn.style.opacity = '0.3';
                                    prevBtn.style.cursor = 'not-allowed';
                                } else {
                                    prevBtn.addEventListener('click', (e) => {
                                        e.stopPropagation();
                                        populateRideLists(hhPage, data.wahoo_current_page - 1);
                                    });
                                }
                                
                                const pageInfo = document.createElement('span');
                                pageInfo.innerText = 'Page ' + data.wahoo_current_page + ' / ' + data.wahoo_total_pages;
                                pageInfo.style.fontSize = '0.8rem';
                                pageInfo.style.color = 'var(--text-secondary)';
                                pageInfo.style.fontWeight = '500';
                                
                                const nextBtn = document.createElement('button');
                                nextBtn.innerText = 'Next ▶';
                                nextBtn.className = 'btn-action';
                                nextBtn.style.padding = '0.35rem 0.8rem';
                                nextBtn.style.fontSize = '0.78rem';
                                nextBtn.style.borderRadius = '6px';
                                nextBtn.disabled = data.wahoo_current_page >= data.wahoo_total_pages;
                                if (nextBtn.disabled) {
                                    nextBtn.style.opacity = '0.3';
                                    nextBtn.style.cursor = 'not-allowed';
                                } else {
                                    nextBtn.addEventListener('click', (e) => {
                                        e.stopPropagation();
                                        populateRideLists(hhPage, data.wahoo_current_page + 1);
                                    });
                                }
                                
                                paginationDiv.appendChild(prevBtn);
                                paginationDiv.appendChild(pageInfo);
                                paginationDiv.appendChild(nextBtn);
                                listWahooContainer.appendChild(paginationDiv);
                            }
                        } else {
                            const emptyItem = document.createElement('div');
                            emptyItem.style.textAlign = 'center';
                            emptyItem.style.color = 'var(--text-secondary)';
                            emptyItem.style.padding = '3rem 0';
                            emptyItem.style.fontStyle = 'italic';
                            emptyItem.style.fontSize = '0.9rem';
                            emptyItem.innerText = 'No Wahoo activities found.';
                            listWahooContainer.appendChild(emptyItem);
                        }
                    }

                    // ==========================================
                    // Intervals List Rendering
                    // ==========================================
                    if (!data.intervals_configured) {
                        const promptCard = document.createElement('div');
                        promptCard.style.background = 'rgba(255, 255, 255, 0.02)';
                        promptCard.style.border = '1px solid var(--border-color)';
                        promptCard.style.borderRadius = '16px';
                        promptCard.style.padding = '1.5rem';
                        promptCard.style.color = 'var(--text-secondary)';
                        promptCard.style.lineHeight = '1.6';
                        promptCard.style.fontSize = '0.9rem';
                        promptCard.style.boxShadow = '0 4px 20px rgba(0, 0, 0, 0.2)';
                        
                        promptCard.innerHTML = '<div style="display: flex; align-items: center; gap: 0.6rem; margin-bottom: 0.75rem; color: var(--accent); font-weight: 700; font-family: \'Outfit\'; font-size: 1.05rem;">' +
                            '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" style="flex-shrink: 0;"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>' +
                            'Intervals.icu Connection Not Configured' +
                            '</div>' +
                            '<p style="margin: 0; font-size: 0.85rem; color: var(--text-secondary);">Connect your Intervals.icu account to directeurAI to view and import your activities. Go to the Settings panel to configure your credentials.</p>';
                        listIntervalsContainer.appendChild(promptCard);
                    } else {
                        if (data.intervals && data.intervals.length > 0) {
                            data.intervals.forEach(act => {
                                const dateStr = act.start_time ? new Date(act.start_time).toLocaleString() : 'N/A';
                                const distStr = act.distance_km + ' km';
                                const durStr = act.duration || 'N/A';
                                const item = document.createElement('div');
                                item.className = 'ride-list-item';
                                item.innerHTML = '<div>' +
                                    '<div style="font-weight: 600; color: #ffffff; font-size: 0.95rem; margin-bottom: 0.2rem;">' + (act.name || 'Unnamed Activity') + '</div>' +
                                    '<div style="font-size: 0.8rem; color: var(--text-secondary);">Date: ' + dateStr + '</div>' +
                                    '</div>' +
                                    '<div style="display: flex; align-items: center; gap: 1rem;">' +
                                    '<div style="text-align: right;">' +
                                    '<div style="font-weight: 600; color: var(--accent); font-size: 0.9rem;">' + distStr + '</div>' +
                                    '<div style="font-size: 0.75rem; color: var(--text-secondary);">' + durStr + '</div>' +
                                    '</div>' +
                                    '<span class="badge" style="font-size: 0.7rem; padding: 0.25rem 0.5rem; background: rgba(228, 92, 134, 0.15); border-color: var(--accent); color: var(--accent);">INTERVALS</span>' +
                                    '</div>';
                                item.addEventListener('click', () => {
                                    loadRideData('intervals', act.id);
                                });
                                listIntervalsContainer.appendChild(item);
                            });
                        } else {
                            const emptyItem = document.createElement('div');
                            emptyItem.style.textAlign = 'center';
                            emptyItem.style.color = 'var(--text-secondary)';
                            emptyItem.style.padding = '3rem 0';
                            emptyItem.style.fontStyle = 'italic';
                            emptyItem.style.fontSize = '0.9rem';
                            emptyItem.innerText = 'No Intervals.icu activities found.';
                            listIntervalsContainer.appendChild(emptyItem);
                        }
                    }

                    if (data.bikes && data.bikes.length > 0) {
                        const bikeSelector = document.getElementById('bike-selector');
                        if (bikeSelector) {
                            const currentVal = bikeSelector.value;
                            bikeSelector.innerHTML = '<option value="">⚙️ Default Gears</option>';
                            data.bikes.forEach(bike => {
                                const opt = document.createElement('option');
                                opt.value = bike.name;
                                opt.textContent = '🚲 ' + bike.name;
                                bikeSelector.appendChild(opt);
                            });
                            bikeSelector.value = currentVal;
                            if (bikeSelector.value !== currentVal) {
                                bikeSelector.value = '';
                            }
                            bikeSelector.style.display = 'block';
                        }
                    }

                    checkEmptyState();
                })
                .catch(err => {
                    console.error("Failed to load rides list:", err);
                    selectRideLoading.style.display = 'none';
                    selectRideEmpty.innerText = "Error loading activities: " + err.message;
                    selectRideEmpty.style.display = 'block';
                });
        };

        btnSelectRide.addEventListener('click', () => {
            selectRideModal.style.display = 'flex';
            populateRideLists();
        });

        selectRideCloseBtn.addEventListener('click', () => {
            selectRideModal.style.display = 'none';
        });

        selectRideModal.addEventListener('click', (e) => {
            if (e.target === selectRideModal) {
                selectRideModal.style.display = 'none';
            }
        });

        // ==========================================
        // Saved Data Manager Logic
        // ==========================================
        if (btnShowSavedData) {
            btnShowSavedData.addEventListener('click', () => {
                if (savedDataModal) savedDataModal.style.display = 'flex';
                populateSavedDataModal();
            });
        }

        savedDataCloseBtn.addEventListener('click', () => {
            savedDataModal.style.display = 'none';
        });

        savedDataModal.addEventListener('click', (e) => {
            if (e.target === savedDataModal) {
                savedDataModal.style.display = 'none';
            }
        });

        const populateSavedDataModal = () => {
            savedDataContent.innerHTML = '';

            // Category 1: Settings & Credentials
            const savedKey = clientStorage.getItem('gemini_api_key') || '';
            const maskedKey = savedKey ? (savedKey.substring(0, 6) + '...' + savedKey.substring(savedKey.length - 4)) : 'Not Configured';
            const planVal = clientStorage.getItem('fit_athlete_training_plan') || '';
            const planPreview = planVal ? (planVal.length > 50 ? planVal.substring(0, 50) + '...' : planVal) : 'Not Configured';
            const selectedBikeVal = clientStorage.getItem('directeur_selected_bike') || '';
            const bikePreview = selectedBikeVal ? ('🚲 ' + selectedBikeVal) : 'Default Gears';

            const settingsSection = document.createElement('div');
            settingsSection.style.background = 'rgba(255,255,255,0.02)';
            settingsSection.style.border = '1px solid rgba(255,255,255,0.05)';
            settingsSection.style.borderRadius = '12px';
            settingsSection.style.padding = '1.25rem';
            settingsSection.innerHTML = '<h4 style="margin: 0 0 1rem 0; font-family: \'Outfit\'; color: var(--accent); font-weight: 700; border-bottom: 1px solid rgba(255,255,255,0.05); padding-bottom: 0.5rem; display: flex; align-items: center; gap: 0.5rem;">⚙️ Settings & Credentials</h4>' +
                '<div style="display: flex; flex-direction: column; gap: 0.75rem;">' +
                    '<div style="display: flex; justify-content: space-between; align-items: center; background: rgba(0,0,0,0.15); padding: 0.5rem 0.75rem; border-radius: 8px; border: 1px solid var(--border-color);">' +
                        '<div>' +
                            '<div style="font-weight: 600; font-size: 0.85rem; color: #ffffff;">Gemini API Key</div>' +
                            '<div style="font-size: 0.75rem; color: var(--text-secondary); font-family: monospace;">' + maskedKey + '</div>' +
                        '</div>' +
                        (savedKey ? '<button class="btn-action" id="sd-clear-key-btn" style="padding: 0.2rem 0.6rem; font-size: 0.75rem; border-color: rgba(231,76,60,0.3); color: #fc8181;">Clear</button>' : '') +
                    '</div>' +
                    '<div style="display: flex; justify-content: space-between; align-items: center; background: rgba(0,0,0,0.15); padding: 0.5rem 0.75rem; border-radius: 8px; border: 1px solid var(--border-color);">' +
                        '<div style="flex: 1; margin-right: 1rem;">' +
                            '<div style="font-weight: 600; font-size: 0.85rem; color: #ffffff;">My Training Plan & Goals</div>' +
                            '<div style="font-size: 0.75rem; color: var(--text-secondary); text-overflow: ellipsis; overflow: hidden; white-space: nowrap;">' + planPreview + '</div>' +
                        '</div>' +
                        (planVal ? '<button class="btn-action" id="sd-clear-plan-btn" style="padding: 0.2rem 0.6rem; font-size: 0.75rem; border-color: rgba(231,76,60,0.3); color: #fc8181;">Clear</button>' : '') +
                    '</div>' +
                    '<div style="display: flex; justify-content: space-between; align-items: center; background: rgba(0,0,0,0.15); padding: 0.5rem 0.75rem; border-radius: 8px; border: 1px solid var(--border-color);">' +
                        '<div>' +
                            '<div style="font-weight: 600; font-size: 0.85rem; color: #ffffff;">Default Bike Selection</div>' +
                            '<div style="font-size: 0.75rem; color: var(--text-secondary);">' + bikePreview + '</div>' +
                        '</div>' +
                        (selectedBikeVal ? '<button class="btn-action" id="sd-clear-bike-btn" style="padding: 0.2rem 0.6rem; font-size: 0.75rem; border-color: rgba(231,76,60,0.3); color: #fc8181;">Clear</button>' : '') +
                    '</div>' +
                '</div>';
            savedDataContent.appendChild(settingsSection);

            const sdClearKeyBtn = document.getElementById('sd-clear-key-btn');
            if (sdClearKeyBtn) {
                sdClearKeyBtn.addEventListener('click', () => {
                    if (confirm('Are you sure you want to clear your saved Gemini API Key?')) {
                        clientStorage.removeItem('gemini_api_key');
                        const coachKeyPanel = document.getElementById('coach-key-panel');
                        const coachAnalysisPanel = document.getElementById('coach-analysis-panel');
                        const coachClearKeyBtn = document.getElementById('coach-clear-key-btn');
                        if (coachKeyPanel) coachKeyPanel.style.display = 'flex';
                        if (coachAnalysisPanel) coachAnalysisPanel.style.display = 'none';
                        if (coachClearKeyBtn) coachClearKeyBtn.style.display = 'none';
                        populateSavedDataModal();
                    }
                });
            }

            const sdClearPlanBtn = document.getElementById('sd-clear-plan-btn');
            if (sdClearPlanBtn) {
                sdClearPlanBtn.addEventListener('click', () => {
                    if (confirm('Are you sure you want to clear your Training Plan & Goals?')) {
                        clientStorage.removeItem('fit_athlete_training_plan');
                        const planInput = document.getElementById('coach-plan-input');
                        if (planInput) planInput.value = '';
                        checkCachedReport(false);
                        populateSavedDataModal();
                    }
                });
            }

            const sdClearBikeBtn = document.getElementById('sd-clear-bike-btn');
            if (sdClearBikeBtn) {
                sdClearBikeBtn.addEventListener('click', () => {
                    if (confirm('Are you sure you want to reset your default bike to Default Gears?')) {
                        clientStorage.removeItem('directeur_selected_bike');
                        const bikeSelector = document.getElementById('bike-selector');
                        if (bikeSelector) {
                            bikeSelector.value = '';
                            recalculateGearsClientSide('');
                        }
                        populateSavedDataModal();
                    }
                });
            }

            // Category 2: Coaching Reports & Chat History
            const historySection = document.createElement('div');
            historySection.style.background = 'rgba(255,255,255,0.02)';
            historySection.style.border = '1px solid rgba(255,255,255,0.05)';
            historySection.style.borderRadius = '12px';
            historySection.style.padding = '1.25rem';
            
            let historyHtml = '<h4 style="margin: 0 0 1rem 0; font-family: \'Outfit\'; color: var(--accent); font-weight: 700; border-bottom: 1px solid rgba(255,255,255,0.05); padding-bottom: 0.5rem; display: flex; align-items: center; gap: 0.5rem;">📋 Analyzed Ride History</h4>';
            
            const historyData = clientStorage.getItem('fit_ride_history');
            let history = [];
            if (historyData) {
                try {
                    history = JSON.parse(historyData);
                } catch(e) {
                    console.error("Error parsing historyData:", e);
                }
            }

            if (history.length === 0) {
                historyHtml += '<div style="font-size: 0.8rem; color: var(--text-secondary); font-style: italic; padding: 0.5rem 0;">No analyzed rides in cache.</div>';
                historySection.innerHTML = historyHtml;
            } else {
                historyHtml += '<div style="display: flex; flex-direction: column; gap: 0.75rem;">';
                history.forEach((ride, index) => {
                    const chatMsgCount = (ride.chatHistory ? ride.chatHistory.length : 0);
                    const messagesText = chatMsgCount > 0 ? (chatMsgCount - 1) + ' chat follow-up(s)' : 'Initial report only';
                    historyHtml += '<div style="display: flex; justify-content: space-between; align-items: center; background: rgba(0,0,0,0.15); padding: 0.5rem 0.75rem; border-radius: 8px; border: 1px solid var(--border-color);">' +
                        '<div>' +
                            '<div style="font-weight: 600; font-size: 0.85rem; color: #ffffff;">📅 ' + ride.date + ' (' + ride.distance_km + ' km)</div>' +
                            '<div style="font-size: 0.75rem; color: var(--text-secondary);">' + ride.model + ' | ' + messagesText + '</div>' +
                        '</div>' +
                        '<button class="btn-action sd-delete-ride-btn" data-id="' + ride.id + '" style="padding: 0.2rem 0.6rem; font-size: 0.75rem; border-color: rgba(231,76,60,0.3); color: #fc8181;">Delete</button>' +
                    '</div>';
                });
                historyHtml += '</div>';
                historySection.innerHTML = historyHtml;
            }
            savedDataContent.appendChild(historySection);

            const deleteRideBtns = historySection.querySelectorAll('.sd-delete-ride-btn');
            deleteRideBtns.forEach(btn => {
                btn.addEventListener('click', (e) => {
                    const rideId = e.target.getAttribute('data-id');
                    if (confirm('Are you sure you want to delete this ride coaching report and chat history from this browser?')) {
                        const updatedHistory = history.filter(r => r.id !== rideId);
                        clientStorage.setItem('fit_ride_history', JSON.stringify(updatedHistory));
                        try {
                            renderRidesCalendar();
                        } catch(e) {
                            console.error("Error rendering rides calendar after delete:", e);
                        }
                        renderHistory();
                        checkCachedReport(false);
                        populateSavedDataModal();
                    }
                });
            });

            // Category 3: Subjective Ride Notes
            const notesSection = document.createElement('div');
            notesSection.style.background = 'rgba(255,255,255,0.02)';
            notesSection.style.border = '1px solid rgba(255,255,255,0.05)';
            notesSection.style.borderRadius = '12px';
            notesSection.style.padding = '1.25rem';
            
            let notesHtml = '<h4 style="margin: 0 0 1rem 0; font-family: \'Outfit\'; color: var(--accent); font-weight: 700; border-bottom: 1px solid rgba(255,255,255,0.05); padding-bottom: 0.5rem; display: flex; align-items: center; gap: 0.5rem;">💬 Subjective Ride Notes</h4>';
            
            const notesKeys = [];
            for (const k of Object.keys(clientStorage.cache)) {
                if (k && k.startsWith('fit_ride_notes_')) {
                    notesKeys.push(k);
                }
            }

            if (notesKeys.length === 0) {
                notesHtml += '<div style="font-size: 0.8rem; color: var(--text-secondary); font-style: italic; padding: 0.5rem 0;">No subjective ride notes found.</div>';
                notesSection.innerHTML = notesHtml;
            } else {
                notesHtml += '<div style="display: flex; flex-direction: column; gap: 0.75rem;">';
                notesKeys.forEach(k => {
                    const timestamp = k.replace('fit_ride_notes_', '');
                    let formattedDate = timestamp;
                    try {
                        formattedDate = new Date(timestamp).toLocaleString(undefined, { month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit' });
                    } catch(e) {}
                    
                    const noteContent = clientStorage.getItem(k) || '';
                    const notePreview = noteContent.length > 60 ? noteContent.substring(0, 60) + '...' : noteContent;

                    notesHtml += '<div style="display: flex; justify-content: space-between; align-items: center; background: rgba(0,0,0,0.15); padding: 0.5rem 0.75rem; border-radius: 8px; border: 1px solid var(--border-color);">' +
                        '<div style="flex: 1; margin-right: 1rem;">' +
                            '<div style="font-weight: 600; font-size: 0.85rem; color: #ffffff;">Ride: ' + formattedDate + '</div>' +
                            '<div style="font-size: 0.75rem; color: var(--text-secondary); font-style: italic;">"' + notePreview + '"</div>' +
                        '</div>' +
                        '<button class="btn-action sd-delete-note-btn" data-key="' + k + '" style="padding: 0.2rem 0.6rem; font-size: 0.75rem; border-color: rgba(231,76,60,0.3); color: #fc8181;">Delete</button>' +
                    '</div>';
                });
                notesHtml += '</div>';
                notesSection.innerHTML = notesHtml;
            }
            savedDataContent.appendChild(notesSection);

            const deleteNoteBtns = notesSection.querySelectorAll('.sd-delete-note-btn');
            deleteNoteBtns.forEach(btn => {
                btn.addEventListener('click', (e) => {
                    const noteKey = e.target.getAttribute('data-key');
                    if (confirm('Are you sure you want to delete these subjective ride notes?')) {
                        clientStorage.removeItem(noteKey);
                        if (rideData && rideData.summary && noteKey === 'fit_ride_notes_' + rideData.summary.start_time) {
                            const rideNotesInput = document.getElementById('coach-ride-notes');
                            if (rideNotesInput) rideNotesInput.value = '';
                            const savedBadge = document.getElementById('coach-notes-saved-badge');
                            if (savedBadge) savedBadge.style.display = 'none';
                        }
                        checkCachedReport(false);
                        populateSavedDataModal();
                    }
                });
            });
        };

        savedDataClearAllBtn.addEventListener('click', () => {
            if (confirm('⚠️ WARNING: This will permanently delete ALL analyzed rides history, chat logs, training plan details, default bike settings, and API keys from this browser.\n\nAre you sure you want to delete everything?')) {
                if (confirm('CONFIRM IRREVERSIBLE OPERATION:\nAre you absolutely sure? This cannot be undone.')) {
                    clientStorage.clear();
                    
                    const coachKeyPanel = document.getElementById('coach-key-panel');
                    const coachAnalysisPanel = document.getElementById('coach-analysis-panel');
                    const coachClearKeyBtn = document.getElementById('coach-clear-key-btn');
                    if (coachKeyPanel) coachKeyPanel.style.display = 'flex';
                    if (coachAnalysisPanel) coachAnalysisPanel.style.display = 'none';
                    if (coachClearKeyBtn) coachClearKeyBtn.style.display = 'none';

                    const planInput = document.getElementById('coach-plan-input');
                    if (planInput) planInput.value = '';

                    const rideNotesInput = document.getElementById('coach-ride-notes');
                    if (rideNotesInput) rideNotesInput.value = '';

                    const savedBadge = document.getElementById('coach-notes-saved-badge');
                    if (savedBadge) savedBadge.style.display = 'none';

                    const bikeSelector = document.getElementById('bike-selector');
                    if (bikeSelector) {
                        bikeSelector.value = '';
                        recalculateGearsClientSide('');
                    }

                    renderHistory();
                    renderPlannerHistory();
                    checkCachedReport(false);
                    savedDataModal.style.display = 'none';
                    alert('All browser local storage data wiped successfully.');
                }
            }
        });

        // Backup Export (Exports everything in local storage)
        savedDataExportBtn.addEventListener('click', () => {
            const backup = {};
            for (const k of Object.keys(clientStorage.cache)) {
                if (k) {
                    backup[k] = clientStorage.getItem(k);
                }
            }
            
            const jsonString = JSON.stringify(backup, null, 2);
            const blob = new Blob([jsonString], { type: 'application/json' });
            const url = URL.createObjectURL(blob);
            
            const a = document.createElement('a');
            a.href = url;
            a.download = 'directeurAI_backup_' + new Date().toISOString().split('T')[0] + '.json';
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);
        });

        // Backup Import (Imports everything in local storage)
        savedDataImportBtn.addEventListener('click', () => {
            savedDataImportFile.click();
        });

        savedDataImportFile.addEventListener('change', (e) => {
            const file = e.target.files[0];
            if (!file) return;

            const reader = new FileReader();
            reader.onload = (event) => {
                try {
                    const importedData = JSON.parse(event.target.result);
                    
                    if (typeof importedData !== 'object' || importedData === null) {
                        throw new Error('Invalid backup file format.');
                    }

                    let count = 0;
                    Object.keys(importedData).forEach(k => {
                        clientStorage.setItem(k, importedData[k]);
                        count++;
                    });

                    if (count === 0) {
                        alert('No data items found in the backup file.');
                        return;
                    }

                    const planInput = document.getElementById('coach-plan-input');
                    if (planInput) {
                        planInput.value = clientStorage.getItem('fit_athlete_training_plan') || '';
                    }
                    
                    const savedKey = clientStorage.getItem('gemini_api_key');
                    const coachKeyPanel = document.getElementById('coach-key-panel');
                    const coachAnalysisPanel = document.getElementById('coach-analysis-panel');
                    const coachClearKeyBtn = document.getElementById('coach-clear-key-btn');
                    if (savedKey) {
                        if (coachKeyPanel) coachKeyPanel.style.display = 'none';
                        if (coachAnalysisPanel) coachAnalysisPanel.style.display = 'flex';
                        if (coachClearKeyBtn) coachClearKeyBtn.style.display = 'inline-block';
                    } else {
                        if (coachKeyPanel) coachKeyPanel.style.display = 'flex';
                        if (coachAnalysisPanel) coachAnalysisPanel.style.display = 'none';
                        if (coachClearKeyBtn) coachClearKeyBtn.style.display = 'none';
                    }

                    const noteKey = 'fit_ride_notes_' + rideData.summary.start_time;
                    const rideNotesInput = document.getElementById('coach-ride-notes');
                    if (rideNotesInput) {
                        rideNotesInput.value = clientStorage.getItem(noteKey) || '';
                    }
                    const savedBadge = document.getElementById('coach-notes-saved-badge');
                    if (savedBadge) {
                        savedBadge.style.display = (rideNotesInput && rideNotesInput.value) ? 'inline' : 'none';
                    }

                    const bikeSelector = document.getElementById('bike-selector');
                    if (bikeSelector) {
                        const initialBike = clientStorage.getItem('directeur_selected_bike');
                        if (initialBike) {
                            bikeSelector.value = initialBike;
                            recalculateGearsClientSide(initialBike);
                        } else {
                            bikeSelector.value = '';
                            recalculateGearsClientSide('');
                        }
                    }

                    renderHistory();
                    renderPlannerHistory();
                    checkCachedReport(true);
                    populateSavedDataModal();
                    
                    // Reload active view to display imported data
                    const activeView = clientStorage.getItem('directeur_active_view');
                    if (activeView === 'calendar' && typeof showCalendarView === 'function') {
                        showCalendarView();
                    } else if (typeof showDashboardView === 'function') {
                        showDashboardView();
                    }

                    alert('Successfully imported ' + count + ' data items from backup file.');
                } catch(err) {
                    alert('Failed to parse backup file: ' + err.message);
                }
                savedDataImportFile.value = '';
            };
            reader.readAsText(file);
        });

        // Dropdown Menu Backup Export
        if (btnExportBackup) {
            btnExportBackup.addEventListener('click', () => {
                if (savedDataExportBtn) savedDataExportBtn.click();
            });
        }

        // Dropdown Menu Backup Import
        if (btnImportBackup) {
            btnImportBackup.addEventListener('click', () => {
                if (savedDataImportBtn) savedDataImportBtn.click();
            });
        }

        // Route & Schedule Planner Integration
        window.activeRouteDateKey = null;
        window.selectedStartCoords = null;
        window.selectedEndCoords = null;
        window.routePlanGeometry = null;
        window.routePlanName = "";
        window.routePlanDistance = 0;
        window.routePlannerMarkers = [];
        window.routePlannerStartMarker = null;
        window.routePlannerEndMarker = null;
        window.routePlannerPolyline = null;
        window.routePlannerMap = null;

        const addMinutesToTimeString = (timeStr, mins) => {
            if (!timeStr) return "";
            const parts = timeStr.split(":");
            if (parts.length < 2) return "";
            let h = parseInt(parts[0]);
            let m = parseInt(parts[1]);
            m += mins;
            h += Math.floor(m / 60);
            m = m % 60;
            h = h % 24;
            return String(h).padStart(2, "0") + ":" + String(m).padStart(2, "0");
        };

        window.updateTargetDistance = () => {
            const speed = parseFloat(document.getElementById("route-avg-speed").value);
            const dateKey = window.activeRouteDateKey;
            if (!dateKey) return;
            const plansByDate = JSON.parse(clientStorage.getItem("fit_training_plans_by_date") || "{}");
            const d = plansByDate[dateKey];
            if (d && d.duration_mins && !isNaN(speed)) {
                const targetDist = ((d.duration_mins / 60) * speed).toFixed(1);
                document.getElementById("route-target-dist").value = targetDist;
            }
        };

        window.updateFinishTime = () => {
            const startTime = document.getElementById("route-start-time").value;
            const dateKey = window.activeRouteDateKey;
            if (!dateKey) return;
            const plansByDate = JSON.parse(clientStorage.getItem("fit_training_plans_by_date") || "{}");
            const d = plansByDate[dateKey];
            if (d && d.duration_mins && startTime) {
                document.getElementById("route-finish-time").value = addMinutesToTimeString(startTime, d.duration_mins);
            }
        };

        window.calculateHistoricalAvgSpeed = () => {
            const activities = JSON.parse(clientStorage.getItem("fit_activities") || "[]");
            if (activities.length > 0) {
                let totalDist = 0;
                let totalTime = 0;
                let validCount = 0;
                activities.forEach(act => {
                    if (act.distance_meters && act.duration_seconds) {
                        totalDist += act.distance_meters / 1000;
                        totalTime += act.duration_seconds / 3600;
                        validCount++;
                    }
                });
                if (validCount > 0 && totalTime > 0) {
                    return (totalDist / totalTime).toFixed(1);
                }
            }
            return "25.0";
        };

        window.destinationPoint = (lat, lon, distanceKm, bearingDegrees) => {
            const R = 6371;
            const d = distanceKm;
            const brng = (bearingDegrees * Math.PI) / 180;
            const lat1 = (lat * Math.PI) / 180;
            const lon1 = (lon * Math.PI) / 180;

            const lat2 = Math.asin(
                Math.sin(lat1) * Math.cos(d / R) +
                Math.cos(lat1) * Math.sin(d / R) * Math.cos(brng)
            );
            const lon2 = lon1 + Math.atan2(
                Math.sin(brng) * Math.sin(d / R) * Math.cos(lat1),
                Math.cos(d / R) - Math.sin(lat1) * Math.sin(lat2)
            );

            return {
                lat: (lat2 * 180) / Math.PI,
                lon: (lon2 * 180) / Math.PI
            };
        };

        window.generateLoopWaypoints = async (lat, lon, distKm, directionOrBearing) => {
            let baseBearing = 0;
            if (typeof directionOrBearing === "number") {
                baseBearing = directionOrBearing;
            } else {
                const dirMap = {
                    "north": 0, "n": 0,
                    "northeast": 45, "ne": 45,
                    "east": 90, "e": 90,
                    "southeast": 135, "se": 135,
                    "south": 180, "s": 180,
                    "southwest": 225, "sw": 225,
                    "west": 270, "w": 270,
                    "northwest": 315, "nw": 315
                };
                const dirClean = (directionOrBearing || "").toLowerCase().trim();
                for (const key in dirMap) {
                    if (dirClean.includes(key)) {
                        baseBearing = dirMap[key];
                        break;
                    }
                }
            }

            const legDist = distKm / 3.2;
            const wp1 = window.destinationPoint(lat, lon, legDist, baseBearing - 30);
            const wp2 = window.destinationPoint(lat, lon, legDist, baseBearing + 30);
            return [wp1, wp2];
        };

        window.geocodeLocation = async (query) => {
            const coordsRegex = /^(-?\d+(?:\.\d+)?)\s*,\s*(-?\d+(?:\.\d+)?)$/;
            const match = query.match(coordsRegex);
            if (match) {
                return { lat: parseFloat(match[1]), lon: parseFloat(match[2]) };
            }
            console.log("Geocoding query:", query);
            const url = "/api/geocode?q=" + encodeURIComponent(query);
            const res = await fetch(url);
            console.log("Geocode response status:", res.status);
            const text = await res.text();
            console.log("Geocode raw body:", text);
            if (!res.ok) throw new Error("Geocoding service unavailable: status " + res.status);
            
            let data;
            try {
                data = JSON.parse(text);
            } catch (e) {
                console.error("Failed to parse Geocode JSON:", e);
                throw new Error("Invalid JSON response from geocoding service: " + e.message);
            }
            
            if (data.length === 0) throw new Error("Location not found");
            return { lat: parseFloat(data[0].lat), lon: parseFloat(data[0].lon) };
        };

        window.calculateRoute = async () => {
            const statusEl = document.getElementById("route-planner-status");
            const summaryEl = document.getElementById("route-summary-info");
            const startLocStr = document.getElementById("route-start-location").value.trim();
            const endLocStr = document.getElementById("route-end-location").value.trim();
            const towardsStr = document.getElementById("route-towards").value.trim();
            const distVal = parseFloat(document.getElementById("route-target-dist").value);

            if (!startLocStr || isNaN(distVal) || distVal <= 0) {
                statusEl.style.display = "block";
                statusEl.style.background = "rgba(231, 76, 60, 0.1)";
                statusEl.style.borderColor = "#e74c3c";
                statusEl.style.color = "#e74c3c";
                statusEl.innerText = "Error: Please specify a starting location and a target distance.";
                return;
            }

            statusEl.style.display = "block";
            statusEl.style.background = "rgba(52, 152, 219, 0.1)";
            statusEl.style.borderColor = "#3498db";
            statusEl.style.color = "#3498db";
            statusEl.innerText = "Geocoding start location...";

            try {
                let startCoords = window.selectedStartCoords;
                if (!startCoords) {
                    const geocodeResult = await window.geocodeLocation(startLocStr);
                    startCoords = { lat: geocodeResult.lat, lon: geocodeResult.lon };
                    window.selectedStartCoords = startCoords;
                }

                let endCoords = null;
                if (endLocStr) {
                    statusEl.innerText = "Geocoding end location...";
                    endCoords = window.selectedEndCoords;
                    if (!endCoords) {
                        const geocodeResult = await window.geocodeLocation(endLocStr);
                        endCoords = { lat: geocodeResult.lat, lon: geocodeResult.lon };
                        window.selectedEndCoords = endCoords;
                    }
                }

                let finalRoute = null;
                let finalWaypoints = null;
                let finalBearing = 0;
                let routeType = "loop";

                if (endCoords) {
                    routeType = "point-to-point";
                    
                    const latDiff = Math.abs(startCoords.lat - endCoords.lat);
                    const lonDiff = Math.abs(startCoords.lon - endCoords.lon);
                    if (latDiff < 0.0001 && lonDiff < 0.0001) {
                        throw new Error("Start and end locations are identical. Please choose a different destination, or use the Loop Route option.");
                    }

                    statusEl.innerText = "Requesting point-to-point route...";
                    const coordsString = [
                        startCoords.lon + "," + startCoords.lat,
                        endCoords.lon + "," + endCoords.lat
                    ].join("|");

                    const brouterUrl = "/api/brouter?lonlats=" + coordsString + "&profile=trekking&alternativeidx=0&format=geojson";
                    console.log("Point-to-point BRouter URL:", brouterUrl);
                    const res = await fetch(brouterUrl);
                    console.log("BRouter point-to-point response status:", res.status);
                    let text = await res.text();
                    console.log("BRouter point-to-point raw body:", text);
                    if (!res.ok) throw new Error("BRouter failed to calculate point-to-point route: status " + res.status);
                    
                    // Fix BRouter JSON syntax error when coordinates is empty or similar layout missing commas
                    text = text.replace(/"type"\s*:\s*"LineString"\s*\n?\s*"coordinates"/g, '"type": "LineString", "coordinates"');
                    
                    let data;
                    try {
                        data = JSON.parse(text);
                    } catch (e) {
                        console.error("Failed to parse BRouter JSON:", e);
                        throw new Error("Invalid JSON response from routing service: " + e.message);
                    }
                    if (!data.features || data.features.length === 0) throw new Error("No route found between start and end locations.");
                    
                    const route = data.features[0];
                    let hasFerry = false;
                    if (route.properties && route.properties.messages) {
                        const msgs = route.properties.messages;
                        for (let i = 1; i < msgs.length; i++) {
                            const wayTags = msgs[i][9] || "";
                            if (wayTags.includes("route=ferry")) {
                                hasFerry = true;
                                break;
                            }
                        }
                    }
                    if (hasFerry) throw new Error("The route uses a ferry.");
                    finalRoute = route;
                    finalWaypoints = [];
                } else {
                    let startBearing = 0;
                    let preferredDirectionUsed = false;
                    if (towardsStr) {
                        const dirMap = {
                            "north": 0, "n": 0,
                            "northeast": 45, "ne": 45,
                            "east": 90, "e": 90,
                            "southeast": 135, "se": 135,
                            "south": 180, "s": 180,
                            "southwest": 225, "sw": 225,
                            "west": 270, "w": 270,
                            "northwest": 315, "nw": 315
                        };
                        const dirClean = towardsStr.toLowerCase().trim();
                        for (const key in dirMap) {
                            if (dirClean.includes(key)) {
                                startBearing = dirMap[key];
                                preferredDirectionUsed = true;
                                break;
                            }
                        }
                    }

                    const candidateBearings = [startBearing];
                    const allBearings = [180, 225, 270, 135, 90, 315, 45, 0];
                    for (const b of allBearings) {
                        if (!candidateBearings.includes(b)) {
                            candidateBearings.push(b);
                        }
                    }

                    const bearingNames = {
                        0: "North", 45: "Northeast", 90: "East", 135: "Southeast",
                        180: "South", 225: "Southwest", 270: "West", 315: "Northwest"
                    };

                    let errors = [];
                    for (const bearing of candidateBearings) {
                        try {
                            statusEl.innerText = "Requesting loop route for bearing " + bearing + "°...";
                            const waypoints = await window.generateLoopWaypoints(startCoords.lat, startCoords.lon, distVal, bearing);
                            const coordsString = [
                                startCoords.lon + "," + startCoords.lat,
                                waypoints[0].lon + "," + waypoints[0].lat,
                                waypoints[1].lon + "," + waypoints[1].lat,
                                startCoords.lon + "," + startCoords.lat
                            ].join("|");

                            const brouterUrl = "/api/brouter?lonlats=" + coordsString + "&profile=trekking&alternativeidx=0&format=geojson";
                            console.log("Loop BRouter URL:", brouterUrl);
                            const res = await fetch(brouterUrl);
                            console.log("BRouter loop response status:", res.status);
                            const text = await res.text();
                            console.log("BRouter loop raw body:", text);
                            if (!res.ok) throw new Error("BRouter failed: status " + res.status);
                            
                            let data;
                            try {
                                data = JSON.parse(text);
                            } catch (e) {
                                console.error("Failed to parse BRouter loop JSON:", e);
                                throw new Error("Invalid JSON response from routing service: " + e.message);
                            }
                            if (!data.features || data.features.length === 0) throw new Error("No route");

                            const route = data.features[0];
                            let hasFerry = false;
                            if (route.properties && route.properties.messages) {
                                const msgs = route.properties.messages;
                                for (let i = 1; i < msgs.length; i++) {
                                    const wayTags = msgs[i][9] || "";
                                    if (wayTags.includes("route=ferry")) {
                                        hasFerry = true;
                                        break;
                                    }
                                }
                            }

                            if (hasFerry) throw new Error("Uses ferry");

                            finalRoute = route;
                            finalWaypoints = waypoints;
                            finalBearing = bearing;
                            break;
                        } catch (e) {
                            errors.push(bearing + "°: " + e.message);
                        }
                    }

                    if (!finalRoute) {
                        throw new Error("No ferry-free route found. Tried bearings: " + errors.join("; "));
                    }
                }

                const trackLength = parseFloat(finalRoute.properties["track-length"]);
                const totalTime = parseFloat(finalRoute.properties["total-time"]);

                const distanceKm = parseFloat((trackLength / 1000).toFixed(1));
                const durationMinutes = Math.round(totalTime / 60);

                window.routePlanGeometry = finalRoute.geometry.coordinates;
                window.routePlanDistance = distanceKm;

                if (routeType === "point-to-point") {
                    window.routePlanName = "Route: " + startLocStr.split(",")[0] + " to " + endLocStr.split(",")[0];
                } else {
                    const bearingNames = {
                        0: "North", 45: "Northeast", 90: "East", 135: "Southeast",
                        180: "South", 225: "Southwest", 270: "West", 315: "Northwest"
                    };
                    const dirName = bearingNames[finalBearing] || "Custom";
                    window.routePlanName = towardsStr && preferredDirectionUsed
                        ? "Loop towards " + towardsStr
                        : "Loop towards " + dirName + " from " + startLocStr.split(",")[0];
                }

                if (window.routePlannerPolyline) {
                    window.routePlannerPolyline.remove();
                }
                const latLons = finalRoute.geometry.coordinates.map(c => [c[1], c[0]]);
                window.routePlannerPolyline = L.polyline(latLons, { color: "#ff3366", weight: 6, opacity: 0.9, lineJoin: "round" }).addTo(window.routePlannerMap);
                window.routePlannerMap.fitBounds(window.routePlannerPolyline.getBounds());

                window.routePlannerMarkers.forEach(m => m.remove());
                window.routePlannerMarkers = [];
                if (window.routePlannerStartMarker) {
                    window.routePlannerStartMarker.remove();
                    window.routePlannerStartMarker = null;
                }
                if (window.routePlannerEndMarker) {
                    window.routePlannerEndMarker.remove();
                    window.routePlannerEndMarker = null;
                }

                window.updateStartMarker(startCoords.lat, startCoords.lon);
                if (endCoords) {
                    window.updateEndMarker(endCoords.lat, endCoords.lon);
                }

                if (finalWaypoints && finalWaypoints.length > 0) {
                    finalWaypoints.forEach((wp, idx) => {
                        const marker = L.marker([wp.lat, wp.lon]).addTo(window.routePlannerMap)
                            .bindPopup("Waypoint " + (idx + 1));
                        window.routePlannerMarkers.push(marker);
                    });
                }

                statusEl.style.display = "block";
                statusEl.style.background = "rgba(46, 204, 113, 0.1)";
                statusEl.style.borderColor = "#2ecc71";
                statusEl.style.color = "#2ecc71";
                statusEl.innerText = "Route successfully generated!";

                summaryEl.innerHTML = "<strong>Route:</strong> " + window.routePlanName + "<br>" +
                    "<strong>Distance:</strong> " + distanceKm + " km<br>" +
                    "<strong>Est. Riding Time:</strong> " + durationMinutes + " mins";

                document.getElementById("btn-export-route").disabled = false;
                const btnSync = document.getElementById("btn-route-sync");
                if (btnSync) btnSync.disabled = false;
                document.getElementById("btn-route-save").disabled = false;

            } catch (err) {
                statusEl.style.display = "block";
                statusEl.style.background = "rgba(231, 76, 60, 0.1)";
                statusEl.style.borderColor = "#e74c3c";
                statusEl.style.color = "#e74c3c";
                statusEl.innerText = "Error: " + err.message;
            }
        };

        window.suggestRouteWithGemini = async () => {
            const statusEl = document.getElementById("route-planner-status");
            const summaryEl = document.getElementById("route-summary-info");
            const startLocStr = document.getElementById("route-start-location").value.trim();
            const endLocStr = document.getElementById("route-end-location").value.trim();
            const distVal = parseFloat(document.getElementById("route-target-dist").value);
            const key = clientStorage.getItem('gemini_api_key');

            if (!key) {
                statusEl.style.display = "block";
                statusEl.style.background = "rgba(231, 76, 60, 0.1)";
                statusEl.style.borderColor = "#e74c3c";
                statusEl.style.color = "#e74c3c";
                statusEl.innerText = "Error: Gemini API Key missing! Please configure your API key first under 'AI Coach'.";
                alert("Gemini API Key missing! Please configure your API key first under 'AI Coach'.");
                return;
            }

            if (!startLocStr || isNaN(distVal) || distVal <= 0) {
                statusEl.style.display = "block";
                statusEl.style.background = "rgba(231, 76, 60, 0.1)";
                statusEl.style.borderColor = "#e74c3c";
                statusEl.style.color = "#e74c3c";
                statusEl.innerText = "Error: Please specify a starting location and a target distance.";
                return;
            }

            statusEl.style.display = "block";
            statusEl.style.background = "rgba(155, 89, 182, 0.1)";
            statusEl.style.borderColor = "#9b59b6";
            statusEl.style.color = "#e0aaff";
            statusEl.innerText = "🤖 Geocoding start location...";

            let startCoords = window.selectedStartCoords;
            if (!startCoords) {
                try {
                    const geocodeResult = await window.geocodeLocation(startLocStr);
                    startCoords = { lat: geocodeResult.lat, lon: geocodeResult.lon };
                    window.selectedStartCoords = startCoords;
                } catch (geocodeErr) {
                    statusEl.style.display = "block";
                    statusEl.style.background = "rgba(231, 76, 60, 0.1)";
                    statusEl.style.borderColor = "#e74c3c";
                    statusEl.style.color = "#e74c3c";
                    statusEl.innerText = "Error geocoding start location: " + geocodeErr.message;
                    return;
                }
            }

            let endCoords = null;
            if (endLocStr) {
                endCoords = window.selectedEndCoords;
                if (!endCoords) {
                    try {
                        statusEl.innerText = "🤖 Geocoding end location...";
                        const geocodeResult = await window.geocodeLocation(endLocStr);
                        endCoords = { lat: geocodeResult.lat, lon: geocodeResult.lon };
                        window.selectedEndCoords = endCoords;
                    } catch (geocodeErr) {
                        statusEl.style.display = "block";
                        statusEl.style.background = "rgba(231, 76, 60, 0.1)";
                        statusEl.style.borderColor = "#e74c3c";
                        statusEl.style.color = "#e74c3c";
                        statusEl.innerText = "Error geocoding end location: " + geocodeErr.message;
                        return;
                    }
                }
            }

            const plansByDate = JSON.parse(clientStorage.getItem("fit_training_plans_by_date") || "{}");
            const d = plansByDate[window.activeRouteDateKey] || {};
            const workoutDesc = d.description || "";
            const workoutTitle = d.title || d.workout_name || "Ride";
            const workoutDuration = d.duration_mins || 60;
            const workoutStructure = d.structure || "";
            const model = clientStorage.getItem('fit_calendar_model') || 'gemini-3.5-flash';

            let basePromptText = "You are directeurAI Coach, an expert cycling route planner. The athlete wants to plan a route starting at \"" + startLocStr + "\" at coordinates (" + startCoords.lat + ", " + startCoords.lon + ")" + (endLocStr ? " and ending at \"" + endLocStr + "\"" + (endCoords ? " at coordinates (" + endCoords.lat + ", " + endCoords.lon + ")" : "") : "") + ". The workout for today is:\n" +
                "- Date: " + window.activeRouteDateKey + "\n" +
                "- Title: " + workoutTitle + "\n" +
                "- Duration: " + workoutDuration + " mins\n" +
                "- Target Distance: " + distVal + " km\n" +
                "- Description / Structure: " + workoutDesc + " " + workoutStructure + "\n\n" +
                "Please suggest a route that fits this workout's goals.\n\n" +
                "Provide the response in the following JSON format:\n" +
                "{\n" +
                "  \"route_name\": \"Friendly Name of the Route (e.g. Hawk Hill Climbing Loop)\",\n" +
                "  \"explanation\": \"Brief explanation of why this route is selected.\",\n" +
                "  \"waypoints\": [\n" +
                "    { \"name\": \"Waypoint 1 Name\", \"lat\": 37.1234, \"lon\": -122.1234 },\n" +
                "    { \"name\": \"Waypoint 2 Name\", \"lat\": 37.5678, \"lon\": -122.5678 }\n" +
                "  ]\n" +
                "}\n\n" +
                "Rules:\n" +
                "1. Provide exactly 2 to 4 waypoints in order to shape the route. Keep them local to the start location.\n" +
                "2. CRITICAL: Hitting the target distance of " + distVal + " km is your top priority. Space the waypoints so that routing from the start coordinates to the waypoints (and back to start, or to the end coordinates) totals approximately " + distVal + " km. For a target of " + distVal + " km, the furthest waypoint MUST be approximately " + (endCoords ? distVal : (distVal / 2)) + " km away from the start coordinate. Ensure your suggested waypoint coordinates (lat, lon) reflect this distance!\n" +
                "3. Ensure the coordinates (lat, lon) are geographically accurate for the suggested waypoint names in the local region of the start coordinates.\n" +
                "4. Output ONLY valid, raw JSON. Do not wrap in markdown code block formatting.";
 
            const callGemini = (promptText, apiVersion) => {
                const url = 'https://generativelanguage.googleapis.com/' + apiVersion + '/models/' + model + ':generateContent?key=' + key;
                return fetch(url, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        contents: [{
                            role: 'user',
                            parts: [{ text: promptText }]
                        }]
                    })
                })
                .then(res => {
                    if (!res.ok) {
                        return res.json().then(errData => {
                            const errMsg = errData.error?.message || ('HTTP ' + res.status);
                            return { ok: false, status: res.status, message: errMsg };
                        });
                    }
                    return res.json().then(data => ({ ok: true, data }));
                });
            };
 
            let attempts = 0;
            const maxAttempts = 3;
            let currentPrompt = basePromptText;
 
            try {
                while (attempts < maxAttempts) {
                    attempts++;
                    try {
                        statusEl.innerText = "🤖 Querying Gemini (Attempt " + attempts + " of " + maxAttempts + ")...";
                        let apiRes = await callGemini(currentPrompt, 'v1');
                        if (!apiRes.ok && apiRes.status === 404) {
                            apiRes = await callGemini(currentPrompt, 'v1beta');
                        }
                        if (!apiRes.ok) throw new Error(apiRes.message);
 
                        let jsonText = apiRes.data.candidates?.[0]?.content?.parts?.[0]?.text;
                        if (!jsonText) throw new Error("Empty response from Gemini.");
 
                        const tick = String.fromCharCode(96);
                        jsonText = jsonText.replace(new RegExp(tick + tick + tick + "json", "gi"), "")
                                           .replace(new RegExp(tick + tick + tick, "g"), "")
                                           .trim();
                        
                        const responseObj = JSON.parse(jsonText);
                        const routeName = responseObj.route_name || "Gemini Suggested Route";
                        const explanation = responseObj.explanation || "";
                        const waypoints = responseObj.waypoints || [];
 
                        statusEl.innerText = "🤖 Parsing suggested waypoints (Attempt " + attempts + ")...";
 
                        const waypointCoords = [];
                        const resolvedWaypointNames = [];
                        for (const wp of waypoints) {
                            let lat = parseFloat(wp.lat);
                            let lon = parseFloat(wp.lon);
                            let name = wp.name || wp;

                            if (!isNaN(lat) && !isNaN(lon)) {
                                waypointCoords.push({ lat, lon });
                                resolvedWaypointNames.push(name);
                            } else {
                                // Fallback to geocoding
                                try {
                                    statusEl.innerText = "🤖 Geocoding fallback: " + name + "...";
                                    await new Promise(resolve => setTimeout(resolve, 800)); // Rate limit pause
                                    const coords = await window.geocodeLocation(name + ", " + startLocStr);
                                    waypointCoords.push(coords);
                                    resolvedWaypointNames.push(name);
                                } catch (wpErr) {
                                    try {
                                        const coords = await window.geocodeLocation(name);
                                        waypointCoords.push(coords);
                                        resolvedWaypointNames.push(name);
                                    } catch (e2) {
                                        console.error("Total failure geocoding waypoint " + name);
                                    }
                                }
                            }
                        }
 
                        if (waypointCoords.length === 0) {
                            throw new Error("None of the suggested waypoints could be geocoded or resolved.");
                        }
 
                        statusEl.innerText = "🤖 Checking route distance with BRouter (Attempt " + attempts + ")...";
                        
                        const routePoints = [startCoords];
                        waypointCoords.forEach(wp => routePoints.push(wp));
                        if (endCoords) {
                            routePoints.push(endCoords);
                        } else {
                            routePoints.push(startCoords);
                        }
 
                        const coordsString = routePoints.map(p => p.lon + "," + p.lat).join("|");
                        const brouterUrl = "/api/brouter?lonlats=" + coordsString + "&profile=trekking&alternativeidx=0&format=geojson";
                        
                        const res = await fetch(brouterUrl);
                        let text = await res.text();
                        if (!res.ok) throw new Error("BRouter failed to calculate route: status " + res.status);
                        
                        text = text.replace(/"type"\s*:\s*"LineString"\s*\n?\s*"coordinates"/g, '"type": "LineString", "coordinates"');
                        const data = JSON.parse(text);
                        if (!data.features || data.features.length === 0) throw new Error("No route found through the suggested waypoints.");
 
                        const route = data.features[0];
                        const trackLength = parseFloat(route.properties["track-length"]);
                        const totalTime = parseFloat(route.properties["total-time"]);
                        const distanceKm = parseFloat((trackLength / 1000).toFixed(1));
                        const durationMinutes = Math.round(totalTime / 60);
 
                        const isAcceptable = distanceKm >= (distVal * 0.8) || attempts >= maxAttempts;
                        
                        if (isAcceptable) {
                            window.routePlanGeometry = route.geometry.coordinates;
                            window.routePlanDistance = distanceKm;
                            window.routePlanName = routeName;
 
                            if (window.routePlannerPolyline) {
                                window.routePlannerPolyline.remove();
                            }
                            const latLons = route.geometry.coordinates.map(c => [c[1], c[0]]);
                            window.routePlannerPolyline = L.polyline(latLons, { color: "#9b59b6", weight: 6, opacity: 0.9, lineJoin: "round" }).addTo(window.routePlannerMap);
                            window.routePlannerMap.fitBounds(window.routePlannerPolyline.getBounds());
 
                            window.routePlannerMarkers.forEach(m => m.remove());
                            window.routePlannerMarkers = [];
                            if (window.routePlannerStartMarker) {
                                window.routePlannerStartMarker.remove();
                                window.routePlannerStartMarker = null;
                            }
                            if (window.routePlannerEndMarker) {
                                window.routePlannerEndMarker.remove();
                                window.routePlannerEndMarker = null;
                            }
 
                            window.updateStartMarker(startCoords.lat, startCoords.lon);
                            if (endCoords) {
                                window.updateEndMarker(endCoords.lat, endCoords.lon);
                            }
 
                            waypointCoords.forEach((wp, idx) => {
                                const marker = L.marker([wp.lat, wp.lon]).addTo(window.routePlannerMap)
                                    .bindPopup("Waypoint " + (idx + 1) + ": " + resolvedWaypointNames[idx]);
                                window.routePlannerMarkers.push(marker);
                            });
 
                            statusEl.style.display = "block";
                            statusEl.style.background = "rgba(46, 204, 113, 0.1)";
                            statusEl.style.borderColor = "#2ecc71";
                            statusEl.style.color = "#2ecc71";
                            statusEl.innerText = "🤖 AI Route Generated Successfully" + (attempts > 1 ? " (Refined in " + attempts + " attempts)" : "") + "!";
 
                            summaryEl.innerHTML = "<strong>Route:</strong> " + routeName + "<br>" +
                                "<strong>AI Selection:</strong> " + explanation + "<br>" +
                                "<strong>Distance:</strong> " + distanceKm + " km (Target: " + distVal + " km)<br>" +
                                "<strong>Est. Riding Time:</strong> " + durationMinutes + " mins";
 
                            document.getElementById("btn-export-route").disabled = false;
                            const btnSync = document.getElementById("btn-route-sync");
                            if (btnSync) btnSync.disabled = false;
                            document.getElementById("btn-route-save").disabled = false;
                            return;
                        } else {
                            console.warn("Suggested route is too short: " + distanceKm + " km vs target: " + distVal + " km. Retrying...");
                            currentPrompt = basePromptText + "\n\n" +
                                "WARNING from previous attempt:\n" +
                                "- The previous suggested waypoints: " + JSON.stringify(waypoints) + " generated a route of only " + distanceKm + " km, which is way too short compared to the target of " + distVal + " km.\n" +
                                "- You MUST suggest a DIFFERENT route. Choose waypoints that are significantly further away from the start point \"" + startLocStr + "\" to make the route much longer. For example, if you need " + distVal + " km, ensure the furthest waypoint is roughly " + (distVal / 2) + " km away or select a much wider loop. Do not reuse the previous waypoints.";
                        }
                    } catch (attemptErr) {
                        console.error("Attempt " + attempts + " failed: ", attemptErr);
                        if (attempts >= maxAttempts) {
                            throw attemptErr;
                        }
                    }
                }
            } catch (err) {
                console.error(err);
                statusEl.style.display = "block";
                statusEl.style.background = "rgba(231, 76, 60, 0.1)";
                statusEl.style.borderColor = "#e74c3c";
                statusEl.style.color = "#e74c3c";
                statusEl.innerText = "AI Route Error: " + err.message;
            }
        };

        window.escapeXML = (str) => {
            if (!str) return "Route";
            return str.replace(/[<>&'"]/g, function (c) {
                switch (c) {
                    case '<': return '&lt;';
                    case '>': return '&gt;';
                    case '&': return '&amp;';
                    case '\'': return '&apos;';
                    case '"': return '&quot;';
                }
            });
        };

        window.generateGPX = (coords, name) => {
            let trkpts = "";
            coords.forEach(pt => {
                trkpts += '<trkpt lat="' + pt[1] + '" lon="' + pt[0] + '"></trkpt>\n';
            });
            const safeName = window.escapeXML(name);
            return '<?xml version="1.0" encoding="UTF-8"?>\n' +
'<gpx version="1.1" creator="directeurAI" xmlns="http://www.topografix.com/GPX/1/1">\n' +
'  <metadata>\n' +
'    <name>' + safeName + '</name>\n' +
'  </metadata>\n' +
'  <trk>\n' +
'    <name>' + safeName + '</name>\n' +
'    <trkseg>\n' +
trkpts +
'    </trkseg>\n' +
'  </trk>\n' +
'</gpx>';
        };

        window.normalizeGeoJSONCoords = (coords) => {
            if (!coords || coords.length === 0) return coords;
            const pt = coords[0];
            if (pt && pt[0] > 0 && pt[1] < 0 && Math.abs(pt[0]) < Math.abs(pt[1])) {
                return coords.map(c => [c[1], c[0]]);
            }
            return coords;
        };

        window.exportRouteGPX = () => {
            if (!window.routePlanGeometry) {
                alert("No route geometry available.");
                return;
            }
            const gpxData = window.generateGPX(window.routePlanGeometry, window.routePlanName || "route");
            const blob = new Blob([gpxData], { type: "application/gpx+xml" });
            const url = URL.createObjectURL(blob);
            const a = document.createElement("a");
            a.href = url;
            a.download = (window.routePlanName || "route").replace(/[^a-z0-9]/gi, "_").toLowerCase() + ".gpx";
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);
        };

        window.syncRouteToHammerhead = async () => {
            if (!window.routePlanGeometry) {
                alert("No route geometry available.");
                return;
            }
            let routeName = window.routePlanName || "Planned Ride";
            const cleanTitle = routeName.replace(/^dsAI-\d{4}-\d{2}-\d{2}:\s*/, "");
            if (window.activeRouteDateKey) {
                routeName = "dsAI-" + window.activeRouteDateKey + ": " + cleanTitle;
            } else {
                const todayStr = new Date().toISOString().split('T')[0];
                routeName = "dsAI-" + todayStr + ": " + cleanTitle;
            }

            const gpxData = window.generateGPX(window.routePlanGeometry, routeName);
            try {
                const res = await fetch("/api/hammerhead/sync-route", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({
                        name: routeName,
                        gpx: gpxData
                    })
                });
                const resData = await res.json();
                if (res.ok && resData.status === "success") {
                    alert("Successfully synced route to Karoo dashboard!");
                } else {
                    alert("Failed to sync: " + (resData.error || "Unknown error"));
                }
            } catch (err) {
                alert("Sync request failed: " + err.message);
            }
        };

        window.saveRouteSchedule = () => {
            const dateKey = window.activeRouteDateKey;
            if (!dateKey) return;

            const plansByDate = JSON.parse(clientStorage.getItem("fit_training_plans_by_date") || "{}");
            if (!plansByDate[dateKey]) {
                plansByDate[dateKey] = {
                    day: new Date(dateKey).toLocaleDateString("en-US", { weekday: "long" }),
                    workout_type: "No Plan",
                    title: "Planned Ride",
                    description: "User planned workout route",
                };
            }

            const d = plansByDate[dateKey];
            d.scheduled_start_time = document.getElementById("route-start-time").value;
            d.scheduled_finish_time = document.getElementById("route-finish-time").value;
            d.route_name = window.routePlanName;
            d.route_distance = window.routePlanDistance;
            
            if (window.routePlanGeometry) {
                d.route_geojson = {
                    type: "LineString",
                    coordinates: window.routePlanGeometry
                };
                d.route_gpx = window.generateGPX(d.route_geojson.coordinates, window.routePlanName);
            }

            d.route_start_name = document.getElementById("route-start-location").value;
            d.route_end_name = document.getElementById("route-end-location").value;
            d.route_towards = document.getElementById("route-towards").value;

            clientStorage.setItem("fit_training_plans_by_date", JSON.stringify(plansByDate));

            const avgSpeed = parseFloat(document.getElementById("route-avg-speed").value);
            if (!isNaN(avgSpeed)) {
                clientStorage.setItem("fit_route_avg_speed", avgSpeed.toString());
            }

            if (window.currentCalendarProgram && window.currentCalendarProgram.start_date) {
                const program = window.currentCalendarProgram;
                const matchIdx = program.days.findIndex(day => day.date_key === dateKey);
                if (matchIdx !== -1) {
                    program.days[matchIdx] = d;
                    clientStorage.setItem("fit_training_program", JSON.stringify(program));
                }
            }

            window.hideRoutePlannerModal();
            
            if (window.currentCalendarProgram) {
                renderTrainingCalendar(window.currentCalendarProgram);
            } else {
                const synthesizedWeek = getSynthesizedWeek(window.plannerCalendarWeekIndex);
                renderTrainingCalendar(synthesizedWeek);
            }
        };

        window.downloadGPXForDay = (dateKey) => {
            const plansByDate = JSON.parse(clientStorage.getItem("fit_training_plans_by_date") || "{}");
            const d = plansByDate[dateKey];
            if (d && (d.route_gpx || d.route_geojson)) {
                let gpxData = d.route_gpx;
                if (d.route_geojson && d.route_geojson.coordinates) {
                    gpxData = window.generateGPX(d.route_geojson.coordinates, d.route_name || "route");
                    d.route_gpx = gpxData;
                    clientStorage.setItem("fit_training_plans_by_date", JSON.stringify(plansByDate));
                }
                const blob = new Blob([gpxData], { type: "application/gpx+xml" });
                const url = URL.createObjectURL(blob);
                const a = document.createElement("a");
                a.href = url;
                a.download = (d.route_name || "route").replace(/[^a-z0-9]/gi, "_").toLowerCase() + ".gpx";
                document.body.appendChild(a);
                a.click();
                document.body.removeChild(a);
                URL.revokeObjectURL(url);
            } else {
                alert("No GPX data found for this day.");
            }
        };

        window.syncGPXForDay = async (dateKey) => {
            const plansByDate = JSON.parse(clientStorage.getItem("fit_training_plans_by_date") || "{}");
            const d = plansByDate[dateKey];
            if (!d || (!d.route_gpx && !d.route_geojson)) {
                alert("No route GPX to sync.");
                return;
            }
            
            let gpxData = d.route_gpx;
            if (d.route_geojson && d.route_geojson.coordinates) {
                gpxData = window.generateGPX(d.route_geojson.coordinates, d.route_name || "route");
                d.route_gpx = gpxData;
                clientStorage.setItem("fit_training_plans_by_date", JSON.stringify(plansByDate));
            }

            const cleanTitle = (d.title || d.route_name || "Planned Ride").replace(/^dsAI-\d{4}-\d{2}-\d{2}:\s*/, "");
            const customRouteName = "dsAI-" + dateKey + ": " + cleanTitle;

            try {
                const res = await fetch("/api/hammerhead/sync-route", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({
                        name: customRouteName,
                        gpx: gpxData
                    })
                });
                const resData = await res.json();
                if (res.ok && resData.status === "success") {
                    alert("Successfully synced route to Karoo dashboard!");
                } else {
                    alert("Failed to sync: " + (resData.error || "Unknown error"));
                }
            } catch (err) {
                alert("Sync request failed: " + err.message);
            }
        };

        window.showRoutePlannerModal = (dateKey) => {
            window.activeRouteDateKey = dateKey;
            
            const plansByDate = JSON.parse(clientStorage.getItem("fit_training_plans_by_date") || "{}");
            const d = plansByDate[dateKey] || {};

            const dObj = parseLocalDate(dateKey);
            const formattedDate = dObj.toLocaleDateString("en-US", { weekday: "long", month: "short", day: "numeric" });
            document.getElementById("route-planner-title").innerText = "🗺️ Route & Schedule Planner - " + formattedDate;

            const avgSpeed = parseFloat(clientStorage.getItem("fit_route_avg_speed") || window.calculateHistoricalAvgSpeed());
            document.getElementById("route-avg-speed").value = avgSpeed;

            const duration = d.duration_mins || 60;
            const targetDist = d.route_distance || ((duration / 60) * avgSpeed).toFixed(1);
            document.getElementById("route-target-dist").value = targetDist;

            document.getElementById("route-start-time").value = d.scheduled_start_time || "08:00";
            document.getElementById("route-finish-time").value = d.scheduled_finish_time || addMinutesToTimeString(d.scheduled_start_time || "08:00", duration);

            document.getElementById("route-start-location").value = d.route_start_name || "San Francisco, CA";
            document.getElementById("route-end-location").value = d.route_end_name || "";
            document.getElementById("route-towards").value = d.route_towards || "";

            document.getElementById("route-start-location").oninput = () => {
                window.selectedStartCoords = null;
            };
            document.getElementById("route-end-location").oninput = () => {
                window.selectedEndCoords = null;
            };

            window.selectedStartCoords = null;
            window.selectedEndCoords = null;
            window.routePlanGeometry = null;
            window.routePlanName = d.route_name || "";
            window.routePlanDistance = d.route_distance || 0;

            const startRadio = document.querySelector('input[name="map-click-target"][value="start"]');
            if (startRadio) startRadio.checked = true;

            if (d.route_geojson && d.route_geojson.coordinates && d.route_geojson.coordinates.length > 0) {
                const coords = window.normalizeGeoJSONCoords(d.route_geojson.coordinates);
                d.route_geojson.coordinates = coords;
                window.routePlanGeometry = coords;
                const len = coords.length;
                window.selectedStartCoords = { lat: coords[0][1], lon: coords[0][0] };
                window.selectedEndCoords = { lat: coords[len-1][1], lon: coords[len-1][0] };
            }

            document.getElementById("route-summary-info").innerHTML = d.route_name 
                ? "<strong>Route:</strong> " + d.route_name + "<br><strong>Distance:</strong> " + d.route_distance + " km"
                : "No route generated yet. Fill in details and click \"Generate Route\".";

            document.getElementById("btn-export-route").disabled = !d.route_gpx;
            const btnSync2 = document.getElementById("btn-route-sync");
            if (btnSync2) btnSync2.disabled = !d.route_gpx;
            document.getElementById("btn-route-save").disabled = false;

            document.getElementById("route-planner-status").style.display = "none";
            document.getElementById("route-planner-modal").style.display = "flex";

            setTimeout(() => {
                const mapEl = document.getElementById("route-map");
                if (!window.routePlannerMap) {
                    window.routePlannerMap = L.map(mapEl).setView([37.7749, -122.4194], 12);
                    L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
                        attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>',
                        subdomains: 'abcd',
                        maxZoom: 20
                    }).addTo(window.routePlannerMap);

                    window.routePlannerMap.on("click", async (e) => {
                        const lat = e.latlng.lat;
                        const lon = e.latlng.lng;
                        
                        const clickTargetEl = document.querySelector('input[name="map-click-target"]:checked');
                        const isEnd = clickTargetEl && clickTargetEl.value === 'end';
                        
                        if (isEnd) {
                            window.selectedEndCoords = { lat, lon };
                            window.updateEndMarker(lat, lon);
                            document.getElementById("route-end-location").value = lat.toFixed(5) + ", " + lon.toFixed(5);
                        } else {
                            window.selectedStartCoords = { lat, lon };
                            window.updateStartMarker(lat, lon);
                            document.getElementById("route-start-location").value = lat.toFixed(5) + ", " + lon.toFixed(5);
                        }
                        
                        try {
                            const res = await fetch("/api/reverse-geocode?lat=" + lat + "&lon=" + lon);
                            if (res.ok) {
                                const data = await res.json();
                                const val = data.display_name.split(",").slice(0, 3).join(",");
                                if (isEnd) {
                                    document.getElementById("route-end-location").value = val;
                                } else {
                                    document.getElementById("route-start-location").value = val;
                                }
                            }
                        } catch (err) {
                            console.error("Reverse geocode failed:", err);
                        }
                    });
                } else {
                    window.routePlannerMap.invalidateSize();
                }

                window.routePlannerMarkers.forEach(m => m.remove());
                window.routePlannerMarkers = [];
                if (window.routePlannerStartMarker) {
                    window.routePlannerStartMarker.remove();
                    window.routePlannerStartMarker = null;
                }
                if (window.routePlannerEndMarker) {
                    window.routePlannerEndMarker.remove();
                    window.routePlannerEndMarker = null;
                }
                if (window.routePlannerPolyline) {
                    window.routePlannerPolyline.remove();
                    window.routePlannerPolyline = null;
                }

                if (d.route_geojson && d.route_geojson.coordinates && d.route_geojson.coordinates.length > 0) {
                    const coords = window.normalizeGeoJSONCoords(d.route_geojson.coordinates);
                    d.route_geojson.coordinates = coords;
                    const latLons = coords.map(c => [c[1], c[0]]);
                    window.routePlannerPolyline = L.polyline(latLons, { color: "#ff3366", weight: 6, opacity: 0.9, lineJoin: "round" }).addTo(window.routePlannerMap);
                    window.routePlannerMap.fitBounds(window.routePlannerPolyline.getBounds());
 
                    window.updateStartMarker(coords[0][1], coords[0][0]);
                    const lastIdx = coords.length - 1;
                    const distToStart = Math.hypot(coords[lastIdx][1] - coords[0][1], coords[lastIdx][0] - coords[0][0]);
                    if (distToStart > 0.0001) {
                        window.updateEndMarker(coords[lastIdx][1], coords[lastIdx][0]);
                    }
                } else {
                    window.routePlannerMap.setView([37.7749, -122.4194], 12);
                }
            }, 100);
        };

        window.hideRoutePlannerModal = () => {
            document.getElementById("route-planner-modal").style.display = "none";
        };

        window.updateStartMarker = (lat, lon) => {
            if (window.routePlannerStartMarker) {
                window.routePlannerStartMarker.setLatLng([lat, lon]);
            } else {
                window.routePlannerStartMarker = L.marker([lat, lon], {
                    icon: L.divIcon({
                        className: 'custom-div-icon',
                        html: "<div style='background-color:#48bb78; color:white; border-radius:50%; width:24px; height:24px; display:flex; align-items:center; justify-content:center; font-weight:bold; font-size:12px; border:2px solid white;'>S</div>",
                        iconSize: [24, 24],
                        iconAnchor: [12, 12]
                    })
                }).addTo(window.routePlannerMap).bindPopup("Start Location").openPopup();
            }
            window.routePlannerMap.panTo([lat, lon]);
        };

        window.updateEndMarker = (lat, lon) => {
            if (window.routePlannerEndMarker) {
                window.routePlannerEndMarker.setLatLng([lat, lon]);
            } else {
                window.routePlannerEndMarker = L.marker([lat, lon], {
                    icon: L.divIcon({
                        className: 'custom-div-icon',
                        html: "<div style='background-color:#f56565; color:white; border-radius:50%; width:24px; height:24px; display:flex; align-items:center; justify-content:center; font-weight:bold; font-size:12px; border:2px solid white;'>E</div>",
                        iconSize: [24, 24],
                        iconAnchor: [12, 12]
                    })
                }).addTo(window.routePlannerMap).bindPopup("End Location").openPopup();
            }
            window.routePlannerMap.panTo([lat, lon]);
        };

        // Global/Window-level functions called by onclick events in HTML templates
        window.exportAllLocalStorage = () => {
            if (savedDataExportBtn) {
                savedDataExportBtn.click();
            } else {
                const backup = {};
                for (const k of Object.keys(clientStorage.cache)) {
                    if (k) backup[k] = clientStorage.getItem(k);
                }
                const jsonString = JSON.stringify(backup, null, 2);
                const blob = new Blob([jsonString], { type: 'application/json' });
                const url = URL.createObjectURL(blob);
                const a = document.createElement('a');
                a.href = url;
                a.download = 'directeurAI_backup_' + new Date().toISOString().split('T')[0] + '.json';
                document.body.appendChild(a);
                a.click();
                document.body.removeChild(a);
                URL.revokeObjectURL(url);
            }
        };

        window.triggerImportBackup = () => {
            if (savedDataImportBtn) {
                savedDataImportBtn.click();
            } else if (savedDataImportFile) {
                savedDataImportFile.click();
            }
        };

    </script>
</body>
</html>`
}
