package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var buildVersion = "dev"

const (
	defaultTunerCount = 12
	cacheTTL          = 30 * time.Minute
	stitcherBase      = "https://cfd-v4-service-channel-stitcher-use1-1.prd.pluto.tv"
)

// AuthData holds per-session authentication info.
type AuthData struct {
	SessionToken  string
	StitcherParams string
	DeviceID      string
}

// BootResponse is the relevant subset of the Pluto TV boot response.
type BootResponse struct {
	SessionToken  string `json:"sessionToken"`
	StitcherParams string `json:"stitcherParams"`
}

// Channel represents a Pluto TV channel from the API.
type Channel struct {
	ID          string       `json:"_id"`
	Slug        string       `json:"slug"`
	Name        string       `json:"name"`
	Number      int          `json:"number"`
	Summary     string       `json:"summary"`
	Category    string       `json:"category"`
	IsStitched  bool         `json:"isStitched"`
	ColorLogoPNG ImageRef    `json:"colorLogoPNG"`
	FeaturedImage ImageRef   `json:"featuredImage"`
}

// ImageRef holds an image path.
type ImageRef struct {
	Path string `json:"path"`
}

// Server holds shared state.
type Server struct {
	mu            sync.RWMutex
	sessions      map[int]*AuthData // 1-indexed
	channels      []Channel
	channelsCachedAt time.Time
	username      string
	password      string
	tunerCount    int
}

func newServer() *Server {
	username := os.Getenv("PLUTO_USERNAME")
	password := os.Getenv("PLUTO_PASSWORD")
	if username == "" || password == "" {
		log.Fatal("PLUTO_USERNAME and PLUTO_PASSWORD environment variables are required")
	}

	return &Server{
		sessions:   make(map[int]*AuthData),
		username:   username,
		password:   password,
		tunerCount: defaultTunerCount,
	}
}

// authenticate creates a new session for a given tuner number.
func (s *Server) authenticate(tunerNum int) (*AuthData, error) {
	deviceID := uuid.New().String()

	params := url.Values{
		"appName":            {"web"},
		"appVersion":         {"8.0.0-111b2b9dc00bd0bea9030b30662159ed9e7c8bc6"},
		"deviceVersion":      {"122.0.0"},
		"deviceModel":        {"web"},
		"deviceMake":         {"chrome"},
		"deviceType":         {"web"},
		"clientID":           {deviceID},
		"clientModelNumber":  {"1.0.0"},
		"serverSideAds":      {"false"},
		"drmCapabilities":    {"widevine:L3"},
		"username":           {s.username},
		"password":           {s.password},
	}

	bootURL := "https://boot.pluto.tv/v4/start?" + params.Encode()
	log.Printf("[INFO] Authenticating tuner-%d (deviceId: %s...)...", tunerNum, deviceID[:8])

	resp, err := http.Get(bootURL)
	if err != nil {
		return nil, fmt.Errorf("auth request failed: %w", err)
	}
	defer resp.Body.Close()

	var boot BootResponse
	if err := json.NewDecoder(resp.Body).Decode(&boot); err != nil {
		return nil, fmt.Errorf("failed to parse auth response: %w", err)
	}

	if boot.SessionToken == "" {
		return nil, fmt.Errorf("authentication failed: no session token in response")
	}

	log.Printf("[INFO] tuner-%d authentication successful", tunerNum)
	return &AuthData{
		SessionToken:  boot.SessionToken,
		StitcherParams: boot.StitcherParams,
		DeviceID:      deviceID,
	}, nil
}

// authenticateAll creates sessions for all tuners.
func (s *Server) authenticateAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := 1; i <= s.tunerCount; i++ {
		auth, err := s.authenticate(i)
		if err != nil {
			return fmt.Errorf("tuner-%d: %w", i, err)
		}
		s.sessions[i] = auth
	}
	return nil
}

// getSession returns the auth data for a tuner, authenticating if needed.
func (s *Server) getSession(tunerNum int) (*AuthData, error) {
	s.mu.RLock()
	auth, ok := s.sessions[tunerNum]
	s.mu.RUnlock()

	if ok && auth != nil {
		return auth, nil
	}

	// Need to authenticate this tuner
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if auth, ok := s.sessions[tunerNum]; ok && auth != nil {
		return auth, nil
	}

	newAuth, err := s.authenticate(tunerNum)
	if err != nil {
		return nil, err
	}
	s.sessions[tunerNum] = newAuth
	return newAuth, nil
}

// fetchChannels gets the channel list from the Pluto API, with caching.
func (s *Server) fetchChannels() ([]Channel, error) {
	s.mu.RLock()
	if s.channels != nil && time.Since(s.channelsCachedAt) < cacheTTL {
		channels := s.channels
		s.mu.RUnlock()
		return channels, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if s.channels != nil && time.Since(s.channelsCachedAt) < cacheTTL {
		return s.channels, nil
	}

	log.Println("[INFO] Fetching channels from Pluto TV API...")

	now := time.Now().UTC()
	var allChannels []Channel

	// Fetch 4 x 6-hour windows (24 hours of data)
	for i := 0; i < 4; i++ {
		startTime := now.Add(time.Duration(i*6) * time.Hour)
		endTime := startTime.Add(6 * time.Hour)

		startStr := startTime.Format("2006-01-02 15:00:00.000-0700")
		endStr := endTime.Format("2006-01-02 15:00:00.000-0700")

		apiURL := fmt.Sprintf("https://api.pluto.tv/v2/channels?start=%s&stop=%s",
			url.QueryEscape(startStr), url.QueryEscape(endStr))

		log.Println(apiURL)

		resp, err := http.Get(apiURL)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch channels: %w", err)
		}
		defer resp.Body.Close()

		var channels []Channel
		if err := json.NewDecoder(resp.Body).Decode(&channels); err != nil {
			return nil, fmt.Errorf("failed to parse channel response: %w", err)
		}

		allChannels = append(allChannels, channels...)
	}

	// Deduplicate by ID
	seen := make(map[string]bool)
	var unique []Channel
	for _, ch := range allChannels {
		if !seen[ch.ID] {
			seen[ch.ID] = true
			unique = append(unique, ch)
		}
	}

	sort.Slice(unique, func(i, j int) bool {
		return unique[i].Number < unique[j].Number
	})

	s.channels = unique
	s.channelsCachedAt = time.Now()
	log.Printf("[INFO] Cached %d channels", len(unique))
	return unique, nil
}

// generatePlaylist builds the M3U content for a given auth session.
func (s *Server) generatePlaylist(auth *AuthData, channels []Channel) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n\n")

	seen := make(map[int]bool)
	for _, ch := range channels {
		if seen[ch.Number] {
			continue
		}
		seen[ch.Number] = true

		if !ch.IsStitched {
			continue
		}
		if strings.HasPrefix(ch.Slug, "announcement") || strings.HasPrefix(ch.Slug, "privacy-policy") {
			continue
		}

		channelNumber := ch.Number
		logo := ch.ColorLogoPNG.Path
		art := strings.Replace(
			strings.Replace(ch.FeaturedImage.Path, "w=1600", "w=1000", 1),
			"h=900", "h=562", 1)
		group := ch.Category
		name := ch.Name
		guideDesc := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\"", "", "\u201D", "").Replace(ch.Summary)

		m3uURL := fmt.Sprintf("%s/v2/stitch/hls/channel/%s/master.m3u8?%s&jwt=%s&masterJWTPassthrough=true&includeExtendedEvents=true",
			stitcherBase, ch.ID, auth.StitcherParams, auth.SessionToken)

		// Use Pluto _id as channel-id to match the plutotv EPG plugin's site_id
		fmt.Fprintf(&b, "#EXTINF:0 channel-id=\"%s\" tvg-chno=\"%d\" channel-number=\"%d\" tvg-logo=\"%s\" tvc-guide-art=\"%s\" tvc-guide-title=\"%s\" tvc-guide-description=\"%s\" group-title=\"%s\", %s\n%s\n\n",
			ch.ID, channelNumber, channelNumber, logo, art, name, guideDesc, group, name, m3uURL)
	}

	return b.String()
}

// handlePlaylist serves GET /playlist.m3u?user=N
func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	userStr := r.URL.Query().Get("user")
	userNum := 1
	if userStr != "" {
		n, err := strconv.Atoi(userStr)
		if err != nil || n < 1 || n > s.tunerCount {
			http.Error(w, fmt.Sprintf("invalid user: must be 1-%d", s.tunerCount), http.StatusBadRequest)
			return
		}
		userNum = n
	}

	auth, err := s.getSession(userNum)
	if err != nil {
		log.Printf("[ERROR] %v", err)
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}

	channels, err := s.fetchChannels()
	if err != nil {
		log.Printf("[ERROR] %v", err)
		http.Error(w, "failed to fetch channels", http.StatusInternalServerError)
		return
	}

	playlist := s.generatePlaylist(auth, channels)

	w.Header().Set("Content-Type", "audio/mpegurl")
	w.Header().Set("Content-Disposition", "inline; filename=\"playlist.m3u\"")
	fmt.Fprint(w, playlist)
}

func main() {
	srv := newServer()

	log.Printf("[INFO] Authenticating %d tuner sessions...", srv.tunerCount)
	if err := srv.authenticateAll(); err != nil {
		log.Fatalf("[ERROR] %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/playlist.m3u", srv.handlePlaylist)

	log.Printf("[INFO] Listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
