package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"math"
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
	FTP            int              `json:"ftp"`
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

	// Select Theme Name based on ride start month
	analysis.Summary.ThemeName = selectThemeName(analysis.Summary.StartTime)
	analysis.Schema = "https://raw.githubusercontent.com/robshakir/directeur/main/schema.json"
	analysis.SourceFile = filepath.Base(filePath)

	return analysis, nil
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
	resolvedConfigPath := *configFile
	configPassed := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			configPassed = true
		}
	})

	if !configPassed {
		homeDir, _ := os.UserHomeDir()
		if homeDir != "" {
			homeConfig := filepath.Join(homeDir, ".directeur.config.json")
			if _, err := os.Stat(homeConfig); err == nil {
				resolvedConfigPath = homeConfig
			}
		}
	}

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

	inputPassed := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "input" {
			inputPassed = true
		}
	})

	if inputPassed {
		resolvedInputFile = *inputFile
		hasData = true
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
			} else if err != nil {
				fmt.Printf("Error listing local directory: %v\n", err)
			}
		}

		// Fallback to default inputFile if it exists and no other source was resolved/configured
		if !hasData {
			if _, err := os.Stat(*inputFile); err == nil {
				resolvedInputFile = *inputFile
				hasData = true
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
		fmt.Printf("Writing JSON analysis to %s...\n", *outputJSON)
		writeJSON(*outputJSON, resolvedAnalysis)

		// Generate HTML Dashboard
		fmt.Printf("Generating HTML dashboard to %s...\n", *outputHTML)
		writeHTML(*outputHTML, resolvedAnalysis, config)
		fmt.Println("Analysis completed successfully!")
	} else {
		// Generate blank dashboard to serve as base if in serveMode but no initial data found
		writeHTML(*outputHTML, RideAnalysis{}, config)
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
		FTP:        250,
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
	if config.HammerheadAPI.DownloadDir == "" {
		config.HammerheadAPI.DownloadDir = "./fit_downloads"
	}
	if config.WahooAPI.DownloadDir == "" {
		config.WahooAPI.DownloadDir = "./wahoo_downloads"
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
	Filename  string    `json:"filename"`
	ModTime   time.Time `json:"mod_time"`
	SizeBytes int64     `json:"size_bytes"`
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
			rides = append(rides, RideFile{
				Filename:  f.Name(),
				ModTime:   info.ModTime(),
				SizeBytes: info.Size(),
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
		url := fmt.Sprintf("https://api.hammerhead.io/v1/api/activities?page=%d&perPage=10", page)
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
		u := fmt.Sprintf("https://api.wahooligan.com/v1/workouts?page=%d&per_page=10", page)
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

func writeHTML(path string, analysis RideAnalysis, config Config) {
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
	schemaBytes, err := os.ReadFile("schema.json")
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
		JSONStr   template.JS
		SchemaStr template.JS
		BikesStr  template.JS
		Summary   RideSummary
		GearUsage []GearStats
		FTP       int
	}

	data := TmplData{
		JSONStr:   template.JS(jsonData),
		SchemaStr: template.JS(schemaBytes),
		BikesStr:  template.JS(bikesData),
		Summary:   analysis.Summary,
		GearUsage: analysis.GearUsage,
		FTP:       config.FTP,
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
			http.Error(w, "Client credentials not configured in config.json", http.StatusInternalServerError)
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
			http.Error(w, "Wahoo client credentials not configured in config.json", http.StatusInternalServerError)
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

		type RidesResponse struct {
			Local                []RideFile           `json:"local"`
			Hammerhead           []HammerheadActivity `json:"hammerhead"`
			HammerheadConfigured bool                 `json:"hammerhead_configured"`
			HammerheadLinked     bool                 `json:"hammerhead_linked"`
			HammerheadError      string               `json:"hammerhead_error,omitempty"`
			ClientID             string               `json:"client_id,omitempty"`
			CurrentPage          int                  `json:"current_page"`
			TotalPages           int                  `json:"total_pages"`

			Wahoo                []WahooWorkout       `json:"wahoo"`
			WahooConfigured      bool                 `json:"wahoo_configured"`
			WahooLinked          bool                 `json:"wahoo_linked"`
			WahooError           string               `json:"wahoo_error,omitempty"`
			WahooClientID        string               `json:"wahoo_client_id,omitempty"`
			WahooCurrentPage     int                  `json:"wahoo_current_page"`
			WahooTotalPages      int                  `json:"wahoo_total_pages"`

			Bikes                []BikeProfile        `json:"bikes"`
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

			Wahoo:                wahooRides,
			WahooConfigured:      cfg.WahooAPI.ClientID != "" && cfg.WahooAPI.ClientSecret != "",
			WahooLinked:          cfg.WahooAPI.Enabled && (cfg.WahooAPI.AuthToken != "" || cfg.WahooAPI.RefreshToken != ""),
			WahooError:           wahooErrStr,
			WahooClientID:        cfg.WahooAPI.ClientID,
			WahooCurrentPage:     wahooCurrentPage,
			WahooTotalPages:      wahooTotalPages,

			Bikes:                cfg.Bikes,
		}
		json.NewEncoder(w).Encode(resp)
	})

	http.HandleFunc("/api/analyze", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cfg := loadConfig(configPath)
		source := r.URL.Query().Get("source")
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
		} else if source == "hammerhead" {
			id := r.URL.Query().Get("id")
			if id == "" {
				http.Error(w, `{"error": "missing id parameter"}`, http.StatusBadRequest)
				return
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
			filePath, err = downloadWahooFITFile(cfg.WahooAPI, cdnURL, id)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error": "failed to download activity from Wahoo: %s"}`, err.Error()), http.StatusInternalServerError)
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

		json.NewEncoder(w).Encode(analysis)
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
    </style>
</head>
<body>
    <!-- Landing View -->
    <div id="landing-view">
        <div class="landing-glow"></div>
        <div class="landing-container">
            <div class="landing-logo-container">
                <svg class="landing-logo-icon" width="80" height="80" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41" stroke-width="2"/>
                    <circle cx="12" cy="12" r="7" stroke-width="1.5"/>
                    <path d="M12 7c0 2-1 3-3 3 2 0 3 1 3 3 0-2 1-3 3-3-2 0-3-1-3-3z" fill="currentColor" stroke="none"/>
                </svg>
                <h1 class="landing-title">directeur<span style="color: var(--accent); font-weight: 800;">AI</span></h1>
                <div style="font-size: 0.75rem; font-weight: 600; opacity: 0.8; background: rgba(255,255,255,0.08); padding: 0.15rem 0.4rem; border-radius: 4px; letter-spacing: 0.05em; text-transform: uppercase; margin-top: 0.5rem; display: inline-block;">dsAI Cycling Coach</div>
            </div>
            
            <p class="landing-subtitle">
                Unlock elite cycling analytics. Map drivetrain efficiency, evaluate neuromuscular load, and receive advanced AI coaching telemetry.
            </p>
            
            <div class="landing-menu">
                <button class="landing-btn landing-btn-primary" onclick="showDashboardView()">
                    ⚡ Analyse Ride
                </button>
                <button class="landing-btn" onclick="showCalendarView()">
                    🗓️ Training Programme
                </button>
                <button class="landing-btn" onclick="triggerDeviceLinking()">
                    🔗 Link Devices & Import
                </button>
                <button class="landing-btn" onclick="promptFTPConfig()">
                    ⚙️ Configure FTP
                </button>
                <button class="landing-btn" onclick="promptAPIConfig()">
                    🔑 Configure Gemini Key
                </button>
            </div>
            
            <div class="landing-footer">
                directeurAI v1.2 &copy; 2026. Standalone offline mode enabled.
            </div>
        </div>
    </div>

    <!-- Calendar View Container -->
    <div id="calendar-view" style="display: none; padding: 2rem; max-width: 1400px; margin: 0 auto; min-height: 100vh; box-sizing: border-box; font-family: var(--font-family); color: var(--text-primary);">
        <header style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 1.5rem; margin-bottom: 2rem;">
            <div class="logo-area">
                <h1 style="display: flex; align-items: center; gap: 0.5rem; text-transform: none; letter-spacing: normal; cursor: pointer;" onclick="showLandingView()" title="Return to Landing Page">
                    <svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="color: var(--accent); filter: drop-shadow(0 0 8px var(--accent-glow)); transition: all 0.3s ease; margin-right: 0.1rem; vertical-align: middle;">
                        <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41" stroke-width="2"/>
                        <circle cx="12" cy="12" r="7" stroke-width="1.5"/>
                        <path d="M12 7c0 2-1 3-3 3 2 0 3 1 3 3 0-2 1-3 3-3-2 0-3-1-3-3z" fill="currentColor" stroke="none"/>
                    </svg>
                    directeur<span style="color: var(--accent); font-weight: 800;">AI</span> <span style="font-size: 0.75rem; font-weight: 600; opacity: 0.8; background: rgba(255,255,255,0.08); padding: 0.15rem 0.4rem; border-radius: 4px; letter-spacing: 0.05em; text-transform: uppercase;">dsAI</span>
                </h1>
                <p style="color: var(--text-secondary); font-size: 0.85rem; margin-top: 0.2rem;">Elite Training Programme Planner</p>
            </div>
            <button class="btn-action" onclick="showLandingView()" style="font-weight: 600; display: flex; align-items: center; gap: 0.3rem;" title="Return to Landing Page">🏠 Home</button>
        </header>

        <div style="display: grid; grid-template-columns: 1fr; gap: 2rem; align-items: start; margin-top: 1rem;">
            <!-- Grid columns split for desktop -->
            <div style="display: flex; gap: 2rem; flex-wrap: wrap;">
                <!-- Left Column: Inputs -->
                <div class="card" style="flex: 1 1 320px; max-width: 340px; display: flex; flex-direction: column; gap: 1.25rem;">
                    <div class="card-header">
                        <div class="card-title">Planner Configuration</div>
                    </div>
                    
                    <div style="display: flex; flex-direction: column; gap: 0.5rem;">
                        <label style="font-size: 0.8rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Training Goals</label>
                        <textarea id="calendar-goals-input" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; resize: vertical; min-height: 70px; outline: none; transition: border-color 0.2s;" placeholder="e.g. Build FTP and climbing endurance for hilly grand fondo">Build FTP and climbing endurance</textarea>
                    </div>
                    
                    <div style="display: flex; flex-direction: column; gap: 0.5rem;">
                        <label style="font-size: 0.8rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Constraints for Next Week</label>
                        <textarea id="calendar-constraints-input" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 8px; color: #ffffff; padding: 0.6rem; font-family: inherit; font-size: 0.85rem; resize: vertical; min-height: 100px; outline: none; transition: border-color 0.2s;" placeholder="e.g. Tuesday/Thursday trainer sessions capped at 1h. Long ride on Saturday. Monday/Friday Rest.">Monday and Friday are rest days. Long endurance ride on Saturday. Tuesday/Thursday trainer sessions capped at 1 hour.</textarea>
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

                    <button id="btn-generate-calendar" onclick="generateTrainingCalendar()" class="landing-btn landing-btn-primary" style="width: 100%; justify-content: center; font-size: 0.9rem; padding: 0.75rem;">
                        🗓️ Generate Programme
                    </button>
                </div>

                <!-- Right Column: Outputs -->
                <div style="flex: 3 1 600px; min-width: 0; display: flex; flex-direction: column; gap: 1.5rem;">
                    <!-- Weekly Focus Box -->
                    <div class="card" id="calendar-summary-box" style="display: none; background: linear-gradient(135deg, rgba(255,255,255,0.01) 0%, rgba(255,255,255,0.03) 100%); border-left: 4px solid var(--accent);">
                        <h4 style="color: var(--accent); margin: 0 0 0.5rem 0; font-family: 'Outfit'; font-weight: 600; font-size: 0.95rem; text-transform: uppercase; letter-spacing: 0.05em;">Weekly Physiological Focus</h4>
                        <p id="calendar-summary-text" style="color: var(--text-secondary); font-size: 0.9rem; line-height: 1.5; margin: 0;"></p>
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

        <header>
            <div class="logo-area">
                <h1 style="display: flex; align-items: center; gap: 0.5rem; text-transform: none; letter-spacing: normal; cursor: pointer;" onclick="showLandingView()" title="Return to Landing Page">
                    <svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="color: var(--accent); filter: drop-shadow(0 0 8px var(--accent-glow)); transition: all 0.3s ease; margin-right: 0.1rem; vertical-align: middle;">
                        <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41" stroke-width="2"/>
                        <circle cx="12" cy="12" r="7" stroke-width="1.5"/>
                        <path d="M12 7c0 2-1 3-3 3 2 0 3 1 3 3 0-2 1-3 3-3-2 0-3-1-3-3z" fill="currentColor" stroke="none"/>
                    </svg>
                    directeur<span style="color: var(--accent); font-weight: 800;">AI</span> <span style="font-size: 0.75rem; font-weight: 600; opacity: 0.8; background: rgba(255,255,255,0.08); padding: 0.15rem 0.4rem; border-radius: 4px; letter-spacing: 0.05em; text-transform: uppercase;">dsAI</span>
                </h1>
                <p id="ride-date-sub">Cycling Analysis Dashboard</p>
            </div>
        <div style="display: flex; gap: 1rem; align-items: center;">
            <button class="btn-action" onclick="showLandingView()" style="font-weight: 600; display: flex; align-items: center; gap: 0.3rem;" title="Return to Landing Page">🏠 Home</button>
            <button id="btn-select-ride" class="btn-action" style="font-weight: 600; display: flex; align-items: center; gap: 0.3rem;">📂 Select Ride</button>
            <select id="bike-selector" class="btn-action" style="display: none;">
                <option value="">⚙️ Default Gears</option>
            </select>
            <select id="theme-selector" class="badge" style="cursor: pointer; appearance: none; -webkit-appearance: none; font-family: inherit; font-size: 0.85rem; font-weight: 600; text-align: center; border-radius: 9999px; text-transform: uppercase;">
                <option value="theme-giro" style="background: var(--bg-secondary); color: var(--text-primary);">GIRO PINK THEME</option>
                <option value="theme-flandrian" style="background: var(--bg-secondary); color: var(--text-primary);">FLANDRIAN YELLOW THEME</option>
                <option value="theme-tour" style="background: var(--bg-secondary); color: var(--text-primary);">TOUR YELLOW THEME</option>
                <option value="theme-vuelta" style="background: var(--bg-secondary); color: var(--text-primary);">VUELTA RED THEME</option>
                <option value="theme-carbon" style="background: var(--bg-secondary); color: var(--text-primary);">CARBON DARK THEME</option>
            </select>
            <button id="btn-gemini-coach" class="btn-action" style="background: linear-gradient(135deg, rgba(155, 89, 182, 0.2), rgba(52, 152, 219, 0.2)); border-color: #9b59b6; color: #e0aaff; font-weight: 600; display: flex; align-items: center; gap: 0.3rem;">🤖 Ask directeurAI Coach</button>
            <div class="dropdown" id="data-dropdown">
                <button class="btn-action" id="btn-data-dropdown" style="gap: 0.5rem;">
                    📦 Data Options <span style="font-size: 0.6rem; opacity: 0.7; transition: transform 0.2s ease; display: inline-block;" id="dropdown-arrow">▼</span>
                </button>
                <div class="dropdown-menu" id="data-dropdown-menu">
                    <button id="btn-copy-json" class="dropdown-item">📋 View JSON Data</button>
                    <button id="btn-view-schema" class="dropdown-item">📋 View Schema</button>
                    <button id="btn-download-json" class="dropdown-item">📥 Download JSON</button>
                    <button id="btn-show-saved-data" class="dropdown-item">📦 Show Saved Data</button>
                </div>
            </div>
        </div>
    </header>

    <!-- Stats Grid -->
    <div class="stats-grid">
        <div class="stat-card">
            <div class="stat-label">Power (NP)</div>
            <div class="stat-value" id="val-np">- <span class="stat-unit">W</span></div>
            <div class="stat-subtext" id="val-avg-power">Avg: - W</div>
        </div>
        <div class="stat-card">
            <div class="stat-label">Max Power</div>
            <div class="stat-value" id="val-max-power">- <span class="stat-unit">W</span></div>
            <div class="stat-subtext" id="val-max-power-sub">-</div>
        </div>
        <div class="stat-card">
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
            <div class="card">
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
                <div style="display: flex; gap: 1.5rem; flex-wrap: wrap; padding: 1rem 0;">
                    <div style="flex: 1; min-width: 280px; height: 260px;">
                        <canvas id="chart-power-zones"></canvas>
                    </div>
                    <div style="flex: 1; min-width: 280px; height: 260px;">
                        <canvas id="chart-hr-zones"></canvas>
                    </div>
                </div>
                <div style="margin-top: 1rem; border-top: 1px solid var(--border-color); padding-top: 1rem;">
                    <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 1.5rem;">
                        <div>
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
                            <h4 style="color: var(--accent); margin-bottom: 0.5rem; font-family: 'Outfit'; font-weight: 600;">Heart Rate Zones (Max: <span id="zones-max-hr">-</span> bpm)</h4>
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
            <div class="card">
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
            <div class="card">
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
            <div class="card">
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
    <script>
        let rideData = {{.JSONStr}};
        const schemaData = {{.SchemaStr}};
        const configBikes = {{.BikesStr}};
        console.log("Loaded Ride Data:", rideData);

        const defaultFTP = {{.FTP}} || 250;
        let athleteFTP = parseInt(localStorage.getItem('fit_athlete_ftp')) || defaultFTP;

        // Global Chart and Map references for dynamic updating
        let powerChart, speedAltChart, hrCadenceChart, altGearsChart, powerCurveChart, chartPZones, chartHZones, routePolyline, quadrantAnalysisChart;
        let leafletMap, startMarker, endMarker;
        let fullJSONString = "";
        const fullSchemaString = JSON.stringify(schemaData, null, 2);

        let currentRideSource = 'local';
        let currentRideParam = rideData ? (rideData.source_file || '') : '';
        let currentRideParam2 = '';

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
        window.addEventListener('DOMContentLoaded', () => {
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
                        localStorage.setItem('directeur_selected_bike', selectedBikeName);
                    } else {
                        localStorage.removeItem('directeur_selected_bike');
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

            // Initial render
            renderDashboard(rideData);
        });

        function initializeBikeSelector() {
            const bikeSelector = document.getElementById('bike-selector');
            if (!bikeSelector) return;
            
            // 1. Populate from embedded configBikes if available (for static mode)
            if (typeof configBikes !== 'undefined' && configBikes && configBikes.length > 0) {
                populateSelectorOptions(configBikes);
            }
            
            // 2. Query /api/rides on load to fetch the configured bikes from the live server
            if (window.location.protocol.startsWith('http')) {
                fetch('/api/rides')
                    .then(res => res.json())
                    .then(data => {
                        if (data.bikes && data.bikes.length > 0) {
                            populateSelectorOptions(data.bikes);
                        }
                        
                        // Handle connection error banner at the top of the page
                        const errBanner = document.getElementById('connection-error-banner');
                        if (data.hammerhead_error) {
                            const errMessage = document.getElementById('connection-error-message');
                            const reauthLink = document.getElementById('btn-reauth-banner');
                            if (errBanner && errMessage && reauthLink) {
                                errMessage.textContent = data.hammerhead_error;
                                const authUrl = 'https://api.hammerhead.io/v1/auth/oauth/authorize?client_id=' + encodeURIComponent(data.client_id) + '&redirect_uri=' + encodeURIComponent(window.location.origin + '/callback') + '&response_type=code&scope=activity:read&state=directeur';
                                reauthLink.href = authUrl;
                                errBanner.style.display = 'block';
                            }
                        } else if (errBanner) {
                            errBanner.style.display = 'none';
                        }
                    })
                    .catch(err => {
                        console.log("Could not fetch bikes from server (normal if offline/static):", err);
                    });
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
                    initialBike = localStorage.getItem('directeur_selected_bike');
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

        // ==========================================
        // Training Zones Calculations & Charts
        // ==========================================
        const renderZones = () => {
            const maxHR = rideData.summary.max_heart_rate || 180;
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
                const power = r.power;
                const pz = pZones.find(z => power >= z.min && power <= z.max);
                if (pz) pz.secs++;

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
                const historyData = localStorage.getItem('fit_ride_history');
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
            
            localStorage.setItem('fit_athlete_ftp', athleteFTP);
            
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

        const showDashboardView = () => {
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

        const showLandingView = () => {
            document.getElementById('dashboard-view').style.display = 'none';
            document.getElementById('calendar-view').style.display = 'none';
            document.getElementById('landing-view').style.display = 'flex';
        };
        window.showLandingView = showLandingView;

        const showCalendarView = () => {
            document.getElementById('landing-view').style.display = 'none';
            document.getElementById('dashboard-view').style.display = 'none';
            document.getElementById('calendar-view').style.display = 'block';
            
            // Load custom inputs from local storage if saved
            const savedGoals = localStorage.getItem('fit_calendar_goals');
            if (savedGoals) {
                document.getElementById('calendar-goals-input').value = savedGoals;
            }
            const savedConstraints = localStorage.getItem('fit_calendar_constraints');
            if (savedConstraints) {
                document.getElementById('calendar-constraints-input').value = savedConstraints;
            }
            const savedModel = localStorage.getItem('fit_calendar_model');
            if (savedModel) {
                document.getElementById('calendar-model-select').value = savedModel;
            }

            // Check if there is cached calendar data
            const cached = localStorage.getItem('fit_training_program');
            if (cached) {
                try {
                    const parsed = JSON.parse(cached);
                    renderTrainingCalendar(parsed);
                } catch(e) {
                    console.error("Error loading cached calendar:", e);
                    renderTrainingCalendar(null);
                }
            } else {
                renderTrainingCalendar(null);
            }
        };
        window.showCalendarView = showCalendarView;

        const renderTrainingCalendar = (data) => {
            const grid = document.getElementById('calendar-grid');
            const summaryBox = document.getElementById('calendar-summary-box');
            const summaryText = document.getElementById('calendar-summary-text');
            const overviewBox = document.getElementById('calendar-overview-box');
            const overviewGrid = document.getElementById('calendar-overview-grid');
            const emptyState = document.getElementById('calendar-empty-state');
            
            if (!data || !data.days) {
                grid.style.display = 'none';
                summaryBox.style.display = 'none';
                overviewBox.style.display = 'none';
                emptyState.style.display = 'flex';
                return;
            }

            emptyState.style.display = 'none';
            summaryText.innerText = data.weekly_summary || 'Weekly training plan focus.';
            summaryBox.style.display = 'block';
            overviewBox.style.display = 'block';

            grid.innerHTML = '';
            overviewGrid.innerHTML = '';
            
            let startDate = null;
            if (data && data.start_date) {
                startDate = new Date(data.start_date);
            }
            
            data.days.forEach((d, idx) => {
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
                const weekdayOffsets = {
                    'monday': 0, 'tuesday': 1, 'wednesday': 2, 'thursday': 3,
                    'friday': 4, 'saturday': 5, 'sunday': 6
                };
                const dayName = (d.day || '').toLowerCase().trim();
                const offset = weekdayOffsets[dayName] !== undefined ? weekdayOffsets[dayName] : idx;

                if (startDate) {
                    const dayDate = new Date(startDate);
                    dayDate.setDate(startDate.getDate() + offset);
                    shortDateStr = dayDate.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
                    dateDisplay = '<span style="font-size: 0.85rem; color: var(--text-secondary); margin-top: -0.2rem; margin-bottom: 0.2rem; font-weight: 500;">' + shortDateStr + '</span>';
                }

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
                    const targetRow = document.getElementById('calendar-day-row-' + d.day);
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
                const durationText = d.duration_mins ? d.duration_mins + ' mins' : 'Rest Day';

                overviewCard.innerHTML = 
                    '<div style="display: flex; justify-content: space-between; align-items: baseline; gap: 0.25rem;">' +
                        '<strong style="font-size: 0.85rem; color: #ffffff; font-family: \'Outfit\';">' + d.day.substring(0, 3) + '</strong>' +
                        '<span style="font-size: 0.75rem; color: var(--text-secondary);">' + shortDateStr + '</span>' +
                    '</div>' +
                    '<span class="badge" style="background: ' + badgeColor + '; color: ' + textColor + '; border: 1px solid ' + borderColor + '; font-size: 0.65rem; text-align: center; border-radius: 4px; padding: 0.05rem 0.25rem; text-transform: uppercase; width: fit-content; font-weight: 600; font-family: var(--font-family);">' + (type.includes('rest') ? 'REST' : d.workout_type) + '</span>' +
                    '<div style="font-size: 0.75rem; font-weight: 500; color: #ffffff; text-overflow: ellipsis; white-space: nowrap; overflow: hidden; margin-top: 0.1rem;" title="' + titleText + '">' + titleText + '</div>' +
                    '<div style="font-size: 0.7rem; color: var(--text-secondary);">' + durationText + '</div>';

                overviewGrid.appendChild(overviewCard);

                // Create the detailed Day Row Card
                const row = document.createElement('div');
                row.id = 'calendar-day-row-' + d.day;
                row.className = 'calendar-day-row';
                row.style.display = 'flex';
                row.style.gap = '1.5rem';
                row.style.background = 'var(--bg-secondary)';
                row.style.border = '1px solid var(--border-color)';
                row.style.borderRadius = '12px';
                row.style.padding = '1.25rem';
                row.style.alignItems = 'start';
                row.style.transition = 'all 0.3s ease';
                
                row.innerHTML = 
                    '<div style="flex: 0 0 160px; min-width: 160px; display: flex; flex-direction: column; gap: 0.4rem;">' +
                        '<span style="font-size: 1.15rem; font-weight: 700; color: #ffffff; font-family: \'Outfit\';">' + d.day + '</span>' +
                        dateDisplay +
                        '<span class="badge" style="background: ' + badgeColor + '; color: ' + textColor + '; border: 1px solid ' + borderColor + '; font-size: 0.75rem; text-align: center; border-radius: 4px; padding: 0.15rem 0.4rem; text-transform: uppercase; width: fit-content; font-weight: 600;">' + d.workout_type + '</span>' +
                    '</div>' +
                    '<div style="flex: 3 1 0px; min-width: 0; display: flex; flex-direction: column; gap: 0.3rem; padding-right: 0.5rem;">' +
                        '<span style="font-size: 1rem; font-weight: 600; color: #ffffff; font-family: \'Outfit\';">' + d.title + '</span>' +
                        '<span style="font-size: 0.85rem; color: var(--text-secondary); line-height: 1.4;">' + d.description + '</span>' +
                    '</div>' +
                    '<div class="calendar-day-details" style="flex: 4 1 0px; min-width: 0; display: flex; flex-direction: column; gap: 0.5rem; border-left: 1px solid rgba(255,255,255,0.05); padding-left: 1.5rem;">' +
                        '<div style="display: flex; gap: 1.5rem; font-size: 0.8rem; color: var(--text-secondary);">' +
                            '<span>⏱️ <strong>' + d.duration_mins + '</strong> mins</span>' +
                            '<span>⚡ Target TSS: <strong>' + (d.target_tss || 0) + '</strong></span>' +
                            '<span>📈 Target IF: <strong>' + (d.target_if || 0) + '</strong></span>' +
                        '</div>' +
                        '<div style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 6px; padding: 0.5rem 0.75rem; font-size: 0.8rem; font-family: var(--font-family); line-height: 1.4; color: var(--text-primary);">' +
                            d.structure +
                        '</div>' +
                    '</div>';
                grid.appendChild(row);
            });
            grid.style.display = 'flex';
        };
        window.renderTrainingCalendar = renderTrainingCalendar;

        const generateTrainingCalendar = () => {
            const key = localStorage.getItem('gemini_api_key');
            if (!key) {
                alert('Gemini API Key missing! Please configure your API key first.');
                return;
            }

            const goals = document.getElementById('calendar-goals-input').value.trim();
            const constraints = document.getElementById('calendar-constraints-input').value.trim();
            const model = document.getElementById('calendar-model-select').value;
            
            // Persist parameters in local storage
            localStorage.setItem('fit_calendar_goals', goals);
            localStorage.setItem('fit_calendar_constraints', constraints);
            localStorage.setItem('fit_calendar_model', model);

            // Show loading, hide outputs
            document.getElementById('calendar-loading').style.display = 'flex';
            document.getElementById('calendar-empty-state').style.display = 'none';
            document.getElementById('calendar-grid').style.display = 'none';
            document.getElementById('calendar-summary-box').style.display = 'none';
            document.getElementById('calendar-overview-box').style.display = 'none';

            
            // Build recent ride history context
            let historyText = "No previous ride history found.";
            const historyData = localStorage.getItem('fit_ride_history');
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

            // Calculate current date context and training week dates
            const today = new Date();
            const dayOfWeek = today.getDay(); // 0 = Sunday, 1 = Monday, ..., 6 = Saturday
            
            let startMonday = new Date(today);
            if (dayOfWeek >= 1 && dayOfWeek <= 3) {
                // Mon-Wed: plan for this week (starting this week's Monday)
                startMonday.setDate(today.getDate() - (dayOfWeek - 1));
            } else {
                // Thu-Sun: plan for next week (starting next week's Monday)
                const daysToMonday = dayOfWeek === 0 ? 1 : 8 - dayOfWeek;
                startMonday.setDate(today.getDate() + daysToMonday);
            }
            
            const formatDate = (d) => d.toLocaleDateString('en-US', { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' });
            
            const todayStr = formatDate(today);
            
            const tomorrow = new Date(today);
            tomorrow.setDate(today.getDate() + 1);
            const tomorrowStr = formatDate(tomorrow);
            
            const startMondayStr = formatDate(startMonday);
            
            let weekDaysText = "";
            for (let i = 0; i < 7; i++) {
                const d = new Date(startMonday);
                d.setDate(startMonday.getDate() + i);
                weekDaysText += "- " + d.toLocaleDateString('en-US', { weekday: 'long', month: 'short', day: 'numeric', year: 'numeric' }) + "\n";
            }

            const promptText = "You are an elite cycling coach and exercise physiologist. Design a structured weekly training program (7 days, Monday to Sunday) for an athlete based on their recent ride history, current FTP, training goals, and constraints.\n\n" +
                "Date Context:\n" +
                "- Today is: " + todayStr + "\n" +
                "- Tomorrow is: " + tomorrowStr + "\n" +
                "- The training program is for the week starting on: " + startMondayStr + "\n" +
                "- The specific days of this training week are:\n" + weekDaysText + "\n" +
                "IMPORTANT RULES FOR HANDLING DAYS/DATES:\n" +
                "1. Today's date and day of the week is " + todayStr + ". Use this exact date context to interpret relative days/dates mentioned in the constraints (e.g. if today is Tuesday, then 'yesterday' is Monday, 'tomorrow' is Wednesday, 'this Friday' is Friday, etc.).\n" +
                "2. If the athlete says they 'just finished' or 'completed' a workout today or on any day in the past, you MUST record that specific completed workout on that day in your training plan. DO NOT prescribe 'Rest' or a different workout for a day when a workout has already been completed.\n" +
                "3. Align all other planned workouts and rest days exactly with the dates/weekdays mentioned in the athlete's constraints (e.g. if they say they will ride 100km on Friday, place that ride on the corresponding Friday date; if they suggest rides for Saturday and Sunday, schedule appropriate workouts on those weekend days).\n" +
                "4. Make sure that any prior days in the current week that have already passed (relative to today's date) are set to 'Rest' or to the actual workouts completed if specified by the athlete.\n\n" +
                "Athlete FTP: " + athleteFTP + " W\n\n" +
                "Recent Ride History:\n" + historyText + "\n\n" +
                "Athlete's Training Goals:\n" + goals + "\n\n" +
                "Athlete's Constraints for the Training Week:\n" + constraints + "\n\n" +
                "Please output the program strictly as a JSON object matching the following structure:\n" +
                "{\n" +
                "  \"weekly_summary\": \"Provide a 2-3 sentence overview of the week's physiological focus and progression.\",\n" +
                "  \"days\": [\n" +
                "    {\n" +
                "      \"day\": \"Monday\",\n" +
                "      \"workout_type\": \"Rest Day / Recovery / Endurance / Tempo / Sweet Spot / Threshold / VO2 Max / Anaerobic\",\n" +
                "      \"title\": \"Workout Name\",\n" +
                "      \"duration_mins\": 60,\n" +
                "      \"target_tss\": 55,\n" +
                "      \"target_if\": 0.72,\n" +
                "      \"description\": \"Overview of the workout focus.\",\n" +
                "      \"structure\": \"Warm Up: 10m easy spinning. Main Set: 3x8m at Sweet Spot (200-215W) with 4m recovery. Cool Down: 10m easy spinning.\"\n" +
                "    },\n" +
                "    ... (continue for Tuesday, Wednesday, Thursday, Friday, Saturday, Sunday)\n" +
                "  ]\n" +
                "}\n\n" +
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
                    
                    // Attach the start_date of the training week to the program object
                    parsedProgram.start_date = startMonday.toISOString();
                    
                    localStorage.setItem('fit_training_program', JSON.stringify(parsedProgram));
                    renderTrainingCalendar(parsedProgram);
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
            const currentKey = localStorage.getItem('gemini_api_key') || '';
            const newKey = prompt('Enter your Gemini API Key:', currentKey);
            if (newKey !== null) {
                localStorage.setItem('gemini_api_key', newKey.trim());
                alert('Gemini API Key updated successfully!');
            }
        };
        window.promptAPIConfig = promptAPIConfig;

        // Initialize FTP input field and render lists
        const ftpInput = document.getElementById('ftp-input');
        if (ftpInput) {
            ftpInput.value = athleteFTP;
            ftpInput.addEventListener('input', (e) => {
                const val = parseInt(e.target.value);
                if (!isNaN(val) && val > 0) {
                    athleteFTP = val;
                    localStorage.setItem('fit_athlete_ftp', athleteFTP);
                    if (window.updateIFDisplay) window.updateIFDisplay();
                    if (window.renderZones) window.renderZones();
                    renderFtpEstimates();
                }
            });
        }
        renderFtpEstimates();
        } // End of renderDashboard function

        // Prepare JSON and Schema strings

        // Modal Logic for JSON Viewer
        const jsonModal = document.getElementById('json-modal');
        const btnCopyJson = document.getElementById('btn-copy-json');
        const modalCloseBtn = document.getElementById('modal-close-btn');
        const modalCopyBtn = document.getElementById('modal-copy-btn');
        const modalDownloadBtn = document.getElementById('modal-download-btn');
        const jsonTextarea = document.getElementById('json-textarea');

        // Populate textarea with a fast preview of the JSON on load to prevent rendering crash/lag
        const jsonLines = fullJSONString.split('\n');
        const jsonPreview = jsonLines.slice(0, 100).join('\n') + 
            '\n\n... [Telemetry records truncated for performance. Download the full JSON file or copy it below] ...';
        jsonTextarea.value = jsonPreview;

        btnCopyJson.addEventListener('click', () => {
            jsonModal.style.display = 'flex';
        });

        modalCloseBtn.addEventListener('click', () => {
            jsonModal.style.display = 'none';
        });

        jsonModal.addEventListener('click', (e) => {
            if (e.target === jsonModal) {
                jsonModal.style.display = 'none';
            }
        });

        // Copy JSON directly from memory to clipboard (avoiding textarea selection crash)
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

        // Download JSON using Blob (avoiding copy-paste buffer/DOM crash)
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

        modalDownloadBtn.addEventListener('click', downloadJSON);
        document.getElementById('btn-download-json').addEventListener('click', downloadJSON);

        // Modal Logic for Schema Viewer
        const schemaModal = document.getElementById('schema-modal');
        const btnViewSchema = document.getElementById('btn-view-schema');
        const schemaCloseBtn = document.getElementById('schema-close-btn');
        const schemaCopyBtn = document.getElementById('schema-copy-btn');
        const schemaDownloadBtn = document.getElementById('schema-download-btn');
        const schemaTextarea = document.getElementById('schema-textarea');

        // Populate schema textarea (schema is small, no need for truncation)
        schemaTextarea.value = fullSchemaString;

        btnViewSchema.addEventListener('click', () => {
            schemaModal.style.display = 'flex';
        });

        schemaCloseBtn.addEventListener('click', () => {
            schemaModal.style.display = 'none';
        });

        schemaModal.addEventListener('click', (e) => {
            if (e.target === schemaModal) {
                schemaModal.style.display = 'none';
            }
        });

        // Copy Schema directly from memory
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

        // Download Schema using Blob
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

        // Dropdown Toggle Logic
        const dataDropdown = document.getElementById('data-dropdown');
        const btnDataDropdown = document.getElementById('btn-data-dropdown');
        const dropdownArrow = document.getElementById('dropdown-arrow');

        btnDataDropdown.addEventListener('click', (e) => {
            e.stopPropagation();
            dataDropdown.classList.toggle('active');
            if (dataDropdown.classList.contains('active')) {
                dropdownArrow.style.transform = 'rotate(180deg)';
            } else {
                dropdownArrow.style.transform = 'rotate(0deg)';
            }
        });

        // Close dropdown when clicking an option
        const dropdownItems = dataDropdown.querySelectorAll('.dropdown-item');
        dropdownItems.forEach(item => {
            item.addEventListener('click', () => {
                dataDropdown.classList.remove('active');
                dropdownArrow.style.transform = 'rotate(0deg)';
            });
        });

        // Close dropdown when clicking outside
        window.addEventListener('click', (e) => {
            if (!dataDropdown.contains(e.target)) {
                dataDropdown.classList.remove('active');
                dropdownArrow.style.transform = 'rotate(0deg)';
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
            const historyData = localStorage.getItem('fit_ride_history');
            
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
                    return '<div style="background: rgba(255,255,255,0.03); border: 1px solid rgba(255,255,255,0.05); border-radius: 8px; padding: 0.6rem; font-family: sans-serif; line-height: 1.4; text-align: left;">' +
                        '<div style="display: flex; justify-content: space-between; font-weight: 600; color: #ffffff; font-size: 0.8rem; margin-bottom: 0.25rem;">' +
                            '<span>📅 ' + ride.date + '</span>' +
                            '<span style="color: var(--accent);">' + ride.distance_km + ' km</span>' +
                        '</div>' +
                        '<div style="font-size: 0.72rem; color: #a0aec0; margin-bottom: 0.35rem;">' +
                            'NP: ' + ride.np + 'W | Avg HR: ' + ride.avg_hr + 'bpm | Gain: ' + ride.elevation_gain + 'm' +
                        '</div>' +
                        '<div style="font-size: 0.72rem; color: #e2e8f0; font-style: italic; border-left: 2px solid rgba(155, 89, 182, 0.5); padding-left: 0.4rem; margin-top: 0.25rem;">' +
                            ride.summary +
                        '</div>' +
                    '</div>';
                }).join('');
            } catch (e) {
                console.error("Error rendering history:", e);
                historyList.innerHTML = '<div style="color: #fc8181; font-size: 0.75rem;">Error loading history.</div>';
                if (clearHistoryBtn) clearHistoryBtn.style.display = 'none';
            }
        };

        const checkCachedReport = (autoNavigate = false) => {
            const currentPlan = localStorage.getItem('fit_athlete_training_plan') || '';
            const currentModel = coachModelSelect.value;
            const rideId = rideData.summary.start_time;
            const currentNotes = document.getElementById('coach-ride-notes') ? document.getElementById('coach-ride-notes').value.trim() : '';
            const cacheStatus = document.getElementById('coach-cache-status');
            
            const historyData = localStorage.getItem('fit_ride_history');
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
            const savedKey = localStorage.getItem('gemini_api_key');
            if (savedKey) {
                coachKeyPanel.style.display = 'none';
                coachAnalysisPanel.style.display = 'flex';
                coachClearKeyBtn.style.display = 'inline-block';
                
                // Load Training Plan and History
                const planInput = document.getElementById('coach-plan-input');
                if (planInput) {
                    planInput.value = localStorage.getItem('fit_athlete_training_plan') || '';
                }
                
                // Load Ride Notes (keyed by ride start time)
                const rideNotesInput = document.getElementById('coach-ride-notes');
                if (rideNotesInput && rideData && rideData.summary) {
                    const noteKey = 'fit_ride_notes_' + rideData.summary.start_time;
                    rideNotesInput.value = localStorage.getItem(noteKey) || '';
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
                localStorage.setItem('fit_athlete_training_plan', e.target.value);
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
                        localStorage.setItem(noteKey, rideNotesInput.value);
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
                    localStorage.removeItem('fit_ride_history');
                    renderHistory();
                    checkCachedReport(false);
                }
            });
        }

        // Save & Continue
        coachSaveKeyBtn.addEventListener('click', () => {
            const key = coachKeyInput.value.trim();
            if (key) {
                localStorage.setItem('gemini_api_key', key);
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
                localStorage.removeItem('gemini_api_key');
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
            const key = localStorage.getItem('gemini_api_key');
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
            const historyData = localStorage.getItem('fit_ride_history');
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
                'Here is the telemetry data for the CURRENT ride:\n' +
                bikeLine +
                '- Start Time: ' + rideData.summary.start_time + '\n' +
                '- Total Distance: ' + (rideData.summary.distance_meters / 1000).toFixed(2) + ' km\n' +
                '- Total Duration: ' + formatDuration(rideData.summary.duration_seconds) + '\n' +
                '- Average Power: ' + Math.round(rideData.summary.average_power) + ' W\n' +
                '- Normalized Power (NP): ' + rideData.summary.normalized_power + ' W\n' +
                '- Intensity Factor (IF): ' + intensityFactor + ' (FTP ' + ftp + 'W)\n' +
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
                        const historyData = localStorage.getItem('fit_ride_history');
                        let history = [];
                        if (historyData) {
                            history = JSON.parse(historyData);
                        }
                        
                        const rideId = rideData.summary.start_time;
                        const existingIdx = history.findIndex(r => r.id === rideId);
                        
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
                            power_curve: rideData.summary.power_curve
                        };
                        
                        if (existingIdx !== -1) {
                            history[existingIdx] = newRecord;
                        } else {
                            history.push(newRecord);
                        }
                        
                        localStorage.setItem('fit_ride_history', JSON.stringify(history));
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

            const key = localStorage.getItem('gemini_api_key');
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
                        const historyData = localStorage.getItem('fit_ride_history');
                        if (historyData) {
                            const history = JSON.parse(historyData);
                            const rideId = rideData.summary.start_time;
                            const idx = history.findIndex(r => r.id === rideId);
                            if (idx !== -1) {
                                history[idx].chatHistory = coachChatHistory;
                                localStorage.setItem('fit_ride_history', JSON.stringify(history));
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
        const listLocalContainer = document.getElementById('list-local-container');
        const listHammerheadContainer = document.getElementById('list-hammerhead-container');
        const listWahooContainer = document.getElementById('list-wahoo-container');
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

            listLocalContainer.style.display = 'none';
            listHammerheadContainer.style.display = 'none';
            listWahooContainer.style.display = 'none';

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

        const checkEmptyState = () => {
            const hasLocal = listLocalContainer.children.length > 0;
            const hasHammerhead = listHammerheadContainer.children.length > 0;
            const hasWahoo = listWahooContainer.children.length > 0;
            if (selectRideActiveTab === 'local') {
                selectRideEmpty.style.display = hasLocal ? 'none' : 'block';
            } else if (selectRideActiveTab === 'hammerhead') {
                selectRideEmpty.style.display = hasHammerhead ? 'none' : 'block';
            } else if (selectRideActiveTab === 'wahoo') {
                selectRideEmpty.style.display = hasWahoo ? 'none' : 'block';
            }
        };

        const loadRideData = (source, param, param2) => {
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
                    
                    forceSetupView = true;
                    if (document.getElementById('coach-plan-input')) {
                        document.getElementById('coach-plan-input').value = localStorage.getItem('fit_athlete_training_plan') || '';
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

        const populateRideLists = (hhPage = 1, wahooPage = 1) => {
            selectRideLoading.style.display = 'flex';
            listLocalContainer.innerHTML = '';
            listHammerheadContainer.innerHTML = '';
            listWahooContainer.innerHTML = '';
            selectRideEmpty.style.display = 'none';

            fetch('/api/rides?hh_page=' + hhPage + '&wahoo_page=' + wahooPage)
                .then(res => {
                    if (!res.ok) throw new Error('HTTP error ' + res.status);
                    return res.json();
                })
                .then(data => {
                    selectRideLoading.style.display = 'none';

                    // Toggle connection error banner
                    const errBanner = document.getElementById('connection-error-banner');
                    if (data.hammerhead_error) {
                        const errMessage = document.getElementById('connection-error-message');
                        const reauthLink = document.getElementById('btn-reauth-banner');
                        if (errBanner && errMessage && reauthLink) {
                            errMessage.textContent = data.hammerhead_error;
                            const authUrl = 'https://api.hammerhead.io/v1/auth/oauth/authorize?client_id=' + encodeURIComponent(data.client_id) + '&redirect_uri=' + encodeURIComponent(window.location.origin + '/callback') + '&response_type=code&scope=activity:read&state=directeur';
                            reauthLink.href = authUrl;
                            errBanner.style.display = 'block';
                        }
                    } else if (errBanner) {
                        errBanner.style.display = 'none';
                    }

                    if (data.local && data.local.length > 0) {
                        data.local.forEach(file => {
                            const dateStr = new Date(file.mod_time).toLocaleString();
                            const sizeStr = (file.size_bytes / 1024).toFixed(1) + ' KB';
                            const item = document.createElement('div');
                            item.className = 'ride-list-item';
                            item.innerHTML = '<div>' +
                                '<div style="font-weight: 600; color: #ffffff; font-size: 0.95rem; margin-bottom: 0.2rem;">' + file.filename + '</div>' +
                                '<div style="font-size: 0.8rem; color: var(--text-secondary);">Modified: ' + dateStr + '</div>' +
                                '</div>' +
                                '<div style="display: flex; align-items: center; gap: 1rem;">' +
                                '<span style="font-size: 0.85rem; color: var(--text-secondary); font-weight: 500;">' + sizeStr + '</span>' +
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
                            '<li>Add the generated <code>client_id</code> and <code>client_secret</code> to <code>config.json</code> under <code>"hammerhead_api"</code> and restart the server.</li>' +
                            '</ol>' +
                            '<p style="margin: 0 0 0.75rem 0; font-size: 0.85rem; color: #ffffff; font-weight: 600;">Method B: Manual Session Token (Expires after 1 hour)</p>' +
                            '<ol style="margin: 0; padding-left: 1.25rem; font-size: 0.82rem; display: flex; flex-direction: column; gap: 0.4rem; color: var(--text-secondary);">' +
                            '<li>Log in to the <a href="https://dashboard.hammerhead.io/" target="_blank" style="color: var(--accent); text-decoration: none; font-weight: 600; border-bottom: 1px dotted var(--accent);">Hammerhead Dashboard</a>.</li>' +
                            '<li>Open Developer Tools (press <kbd style="background: rgba(255, 255, 255, 0.1); border: 1px solid rgba(255, 255, 255, 0.15); border-radius: 4px; padding: 1px 5px; font-family: monospace; font-size: 0.75rem; color: #ffffff;">F12</kbd>).</li>' +
                            '<li>Switch to the <strong>Network</strong> tab, refresh, and filter requests by <code>activities</code>.</li>' +
                            '<li>Select the request, copy the token string after <code>Bearer </code> in the <code>Authorization</code> header.</li>' +
                            '<li>Paste it into <code>config.json</code> under <code>"auth_token"</code>, set <code>"enabled": true</code>, and restart the server.</li>' +
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
                        
                        const authUrl = 'https://api.hammerhead.io/v1/auth/oauth/authorize?client_id=' + encodeURIComponent(data.client_id) + '&redirect_uri=' + encodeURIComponent(window.location.origin + '/callback') + '&response_type=code&scope=activity:read&state=directeur';
                        
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
                            const authUrl = 'https://api.hammerhead.io/v1/auth/oauth/authorize?client_id=' + encodeURIComponent(data.client_id) + '&redirect_uri=' + encodeURIComponent(window.location.origin + '/callback') + '&response_type=code&scope=activity:read&state=directeur';
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
                            '<li>Paste it into <code>config.json</code> under <code>"hammerhead_api"</code> &rarr; <code>"auth_token"</code>, set <code>"enabled": true</code>, and restart the server.</li>' +
                            '</ol>' +
                            reAuthHtml;
                        listHammerheadContainer.appendChild(errorCard);
                    } else {
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
                            '<li>Add the generated <code>client_id</code> and <code>client_secret</code> to <code>config.json</code> under <code>"wahoo_api"</code> and restart the server.</li>' +
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
        btnShowSavedData.addEventListener('click', () => {
            savedDataModal.style.display = 'flex';
            populateSavedDataModal();
        });

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
            const savedKey = localStorage.getItem('gemini_api_key') || '';
            const maskedKey = savedKey ? (savedKey.substring(0, 6) + '...' + savedKey.substring(savedKey.length - 4)) : 'Not Configured';
            const planVal = localStorage.getItem('fit_athlete_training_plan') || '';
            const planPreview = planVal ? (planVal.length > 50 ? planVal.substring(0, 50) + '...' : planVal) : 'Not Configured';
            const selectedBikeVal = localStorage.getItem('directeur_selected_bike') || '';
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
                        localStorage.removeItem('gemini_api_key');
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
                        localStorage.removeItem('fit_athlete_training_plan');
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
                        localStorage.removeItem('directeur_selected_bike');
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
            
            const historyData = localStorage.getItem('fit_ride_history');
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
                        localStorage.setItem('fit_ride_history', JSON.stringify(updatedHistory));
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
            for (let i = 0; i < localStorage.length; i++) {
                const k = localStorage.key(i);
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
                    
                    const noteContent = localStorage.getItem(k) || '';
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
                        localStorage.removeItem(noteKey);
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
                    const keysToRemove = [];
                    for (let i = 0; i < localStorage.length; i++) {
                        const k = localStorage.key(i);
                        if (k && (k.startsWith('fit_') || k.startsWith('directeur_') || k === 'gemini_api_key')) {
                            keysToRemove.push(k);
                        }
                    }
                    keysToRemove.forEach(k => localStorage.removeItem(k));
                    
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
                    checkCachedReport(false);
                    savedDataModal.style.display = 'none';
                    alert('All browser local storage data wiped successfully.');
                }
            }
        });

        // Backup Export
        savedDataExportBtn.addEventListener('click', () => {
            const backup = {};
            for (let i = 0; i < localStorage.length; i++) {
                const k = localStorage.key(i);
                if (k && (k.startsWith('fit_') || k.startsWith('directeur_') || k === 'gemini_api_key')) {
                    backup[k] = localStorage.getItem(k);
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

        // Backup Import
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
                        if (k.startsWith('fit_') || k.startsWith('directeur_') || k === 'gemini_api_key') {
                            localStorage.setItem(k, importedData[k]);
                            count++;
                        }
                    });

                    if (count === 0) {
                        alert('No valid directeurAI browser cache data found in the backup file.');
                        return;
                    }

                    const planInput = document.getElementById('coach-plan-input');
                    if (planInput) {
                        planInput.value = localStorage.getItem('fit_athlete_training_plan') || '';
                    }
                    
                    const savedKey = localStorage.getItem('gemini_api_key');
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
                        rideNotesInput.value = localStorage.getItem(noteKey) || '';
                    }
                    const savedBadge = document.getElementById('coach-notes-saved-badge');
                    if (savedBadge) {
                        savedBadge.style.display = (rideNotesInput && rideNotesInput.value) ? 'inline' : 'none';
                    }

                    const bikeSelector = document.getElementById('bike-selector');
                    if (bikeSelector) {
                        const initialBike = localStorage.getItem('directeur_selected_bike');
                        if (initialBike) {
                            bikeSelector.value = initialBike;
                            recalculateGearsClientSide(initialBike);
                        } else {
                            bikeSelector.value = '';
                            recalculateGearsClientSide('');
                        }
                    }

                    renderHistory();
                    checkCachedReport(true);
                    populateSavedDataModal();
                    
                    alert('Successfully imported ' + count + ' data items from backup file.');
                } catch(err) {
                    alert('Failed to parse backup file: ' + err.message);
                }
                savedDataImportFile.value = '';
            };
            reader.readAsText(file);
        });

    </script>    </div> <!-- End dashboard-view -->
</body>
</html>`
}
