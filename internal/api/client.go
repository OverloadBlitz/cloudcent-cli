package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/OverloadBlitz/cloudcent-cli/internal/config"
)

const (
	APIBaseURL = "https://api.cloudcent.io"
	//APIBaseURL = "http://localhost:8080"
	CLIBaseURL = "https://cli.cloudcent.io"
)

// Client is the CloudCent HTTP client.
type Client struct {
	http   *http.Client
	Config *config.Config
}

// New creates a new Client and loads config from disk.
func New() (*Client, error) {
	c := &Client{
		http: &http.Client{Timeout: 30 * time.Second},
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	c.Config = cfg
	return c, nil
}

// IsInitialized returns true if credentials are present.
func (c *Client) IsInitialized() bool {
	return c.Config != nil && c.Config.APIKey != nil
}

// SaveConfig persists credentials and updates the in-memory copy.
func (c *Client) SaveConfig(cfg *config.Config) error {
	if err := config.Save(cfg); err != nil {
		return err
	}
	c.Config = cfg
	return nil
}

// ReloadConfig re-reads credentials from disk.
func (c *Client) ReloadConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	c.Config = cfg
	return nil
}

func (c *Client) authHeaders(req *http.Request) error {
	if c.Config == nil {
		return fmt.Errorf("not authenticated — run 'cloudcent init' first")
	}
	if c.Config.APIKey == nil {
		return fmt.Errorf("API key not found in config")
	}
	req.Header.Set("X-Cli-Id", c.Config.CliID)
	req.Header.Set("Authorization", "Bearer "+*c.Config.APIKey)
	return nil
}

func (c *Client) doRequest(method, endpoint string, body io.Reader, contentType string, requireAuth bool) (int, []byte, error) {
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if requireAuth {
		if err := c.authHeaders(req); err != nil {
			return 0, nil, err
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, data, nil
}

func (c *Client) get(endpoint string, requireAuth bool) (int, []byte, error) {
	return c.doRequest(http.MethodGet, endpoint, nil, "", requireAuth)
}

func (c *Client) post(endpoint string, payload any, requireAuth bool) (int, []byte, error) {
	var (
		body        io.Reader
		contentType string
	)
	if payload != nil {
		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to marshal POST request body: %w", err)
		}
		body = bytes.NewReader(bodyBytes)
		contentType = "application/json"
	}

	return c.doRequest(http.MethodPost, endpoint, body, contentType, requireAuth)
}

// FetchPricing calls POST /pricing with the given filters.
func (c *Client) FetchPricing(products, regions []string, attrs map[string]string, prices []string) (*PricingAPIResponse, error) {
	params := url.Values{}
	for _, p := range products {
		parts := strings.SplitN(p, " ", 2)
		if len(parts) >= 1 && parts[0] != "" {
			params.Add("provider", parts[0])
		}
		if len(parts) == 2 && parts[1] != "" {
			params.Add("products", parts[1])
		}
	}
	for _, r := range regions {
		params.Add("region", r)
	}

	endpoint := APIBaseURL + "/pricing?" + params.Encode()
	status, respBytes, err := c.post(endpoint, PricingRequest{Attrs: attrs, Prices: prices}, true)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to pricing API: %w", err)
	}

	if status == http.StatusNotFound {
		return &PricingAPIResponse{Data: []PricingItem{}, Total: 0}, nil
	}
	if status >= 300 {
		return nil, fmt.Errorf("API request failed (%d): %s", status, string(respBytes))
	}

	var result PricingAPIResponse
	if err := json.Unmarshal(respBytes, &result); err != nil {
		preview := string(respBytes)
		if len(preview) > 500 {
			preview = preview[:500]
		}
		return nil, fmt.Errorf("failed to parse pricing response: %w (first 500 chars: %s)", err, preview)
	}
	return &result, nil
}

func (c *Client) FetchPricingBatch(requests BatchPricingRequest) (*BatchPricingApiResponse, error) {
	endpoint := APIBaseURL + "/pricing/batch"
	status, respBytes, err := c.post(endpoint, requests, true)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to batch pricing API: %w", err)
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status >= 300 {
		return nil, fmt.Errorf("batch pricing request failed (%d): %s", status, string(respBytes))
	}

	var result BatchPricingApiResponse
	if err := json.Unmarshal(respBytes, &result); err != nil {
		preview := string(respBytes)
		if len(preview) > 500 {
			preview = preview[:500]
		}
		return nil, fmt.Errorf("failed to parse batch pricing response: %w (first 500 chars: %s)", err, preview)
	}
	return &result, nil
}

// DownloadMetadataGz downloads /pricing/metadata and saves as metadata.json.gz.
func (c *Client) DownloadMetadataGz() error {
	status, data, err := c.get(APIBaseURL+"/pricing/metadata", true)
	if err != nil {
		return fmt.Errorf("failed to download metadata: %w", err)
	}
	if status >= 300 {
		return fmt.Errorf("failed to download metadata (%d): %s", status, string(data))
	}

	p, err := config.MetadataGzPath()
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// LoadMetadataFromFile reads and decompresses ~/.cloudcent/metadata.json.gz.
func LoadMetadataFromFile() (*MetadataResponse, error) {
	p, err := config.MetadataGzPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("metadata file not found — run 'cloudcent metadata refresh' first")
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("failed to open gzip metadata: %w", err)
	}
	defer gr.Close()

	var meta MetadataResponse
	if err := json.NewDecoder(gr).Decode(&meta); err != nil {
		return nil, fmt.Errorf("failed to parse metadata JSON: %w", err)
	}
	return &meta, nil
}

// GenerateToken calls POST /api/auth/generate-token.
func (c *Client) GenerateToken() (*GenerateTokenResponse, error) {
	status, data, err := c.post(CLIBaseURL+"/api/auth/generate-token", nil, false)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}
	if status >= 300 {
		return nil, fmt.Errorf("generate-token failed (%d): %s", status, string(data))
	}

	var result GenerateTokenResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w (body: %s)", err, string(data))
	}
	return &result, nil
}

// ExchangeToken calls POST /api/auth/exchange.
func (c *Client) ExchangeToken(exchangeCode string) (*ExchangeResponse, error) {
	status, data, err := c.post(CLIBaseURL+"/api/auth/exchange", map[string]string{"exchange_code": exchangeCode}, false)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange token: %w", err)
	}
	if status >= 300 {
		return nil, fmt.Errorf("exchange failed (%d): %s", status, string(data))
	}

	var result ExchangeResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to parse exchange response: %w (body: %s)", err, string(data))
	}
	return &result, nil
}
