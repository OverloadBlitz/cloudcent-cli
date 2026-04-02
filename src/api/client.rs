use anyhow::{Context, Result};
use reqwest::Client as HttpClient;
use super::models::{Config, PricingApiResponse, MetadataResponse, GenerateTokenResponse, ExchangeResponse, PricingRequest};


pub const API_BASE_URL: &str = "https://api.cloudcent.io";

pub const CLI_BASE_URL: &str = "https://cli.cloudcent.io";

#[derive(Clone)]
pub struct CloudCentClient {
    pub client: HttpClient,
    config: Option<Config>,
}

impl CloudCentClient {
    pub fn new() -> Self {
        Self {
            client: HttpClient::new(),
            config: None,
        }
    }

    pub fn get_config(&self) -> Option<&Config> {
        self.config.as_ref()
    }

    #[allow(dead_code)]
    pub fn set_config(&mut self, config: Config) {
        self.config = Some(config);
    }

    pub fn load_config(&mut self) -> Result<Option<Config>> {
        let config = crate::config::load_config()?;
        self.config = config.clone();
        Ok(config)
    }

    pub fn save_config(&mut self, config: &Config) -> Result<()> {
        crate::config::save_config(config)?;
        self.config = Some(config.clone());
        Ok(())
    }
    
    
    pub async fn fetch_pricing_multi(
        &self,
        products: &[String],
        regions: &[String],
        attrs: std::collections::HashMap<String, String>,
        prices: &[String]
    ) -> Result<PricingApiResponse> {
        let mut params = Vec::new();

        for product in products {
            let mut parts = product.splitn(2, ' ');
            let provider = parts.next().unwrap_or("").trim();
            let product_name = parts.next().unwrap_or("").trim();
            if !provider.is_empty() {
                params.push(format!("provider={}", provider));
            }
            if !product_name.is_empty() {
                params.push(format!("products={}", product_name));
            }
        }
        for r in regions {
            params.push(format!("region={}", r));
        }

        let endpoint = format!("/pricing?{}", params.join("&"));
        
        let config = self
            .config
            .as_ref()
            .context("Not initialized. Run 'user init' first.")?;

        let api_key = config
            .api_key
            .as_ref()
            .context("API key not found in config")?;

        let url = format!("{}{}", API_BASE_URL, endpoint);
        
        let request_body = PricingRequest {
            attrs,
            prices: prices.to_vec(),
        };

        let response = self.client
            .post(&url)
            .header("X-Cli-Id", &config.cli_id)
            .header("Authorization", format!("Bearer {}", api_key))
            .json(&request_body)
            .send()
            .await
            .context("Failed to connect pricing API")?;

        if response.status() == reqwest::StatusCode::NOT_FOUND {
            return Ok(PricingApiResponse {
                data: Vec::new(),
                total: 0,
            });
        }

        if !response.status().is_success() {
            let status = response.status();
            let error_text = response.text().await.unwrap_or_default();
            anyhow::bail!("API request failed with status {}: {}", status, error_text);
        }

        let response_text = response.text().await.context("Failed to read response text")?;
        
        // Try to parse the response
        let pricing: PricingApiResponse = serde_json::from_str(&response_text)
            .context(format!("Failed to parse pricing response. First 500 chars: {}", 
                &response_text.chars().take(500).collect::<String>()))?;

        Ok(pricing)
    }

    #[allow(dead_code)]
    async fn post_text(&self, endpoint: &str, extra_headers: &[(&str, &str)]) -> Result<String> {
        let url = format!("{}{}", API_BASE_URL, endpoint);

        let mut request = self.client
            .post(&url)
            .header("Content-Type", "application/json");

        for (key, value) in extra_headers {
            request = request.header(*key, *value);
        }

        let response = request
            .send()
            .await
            .context("Failed to connect to CloudCent API")?;
        if !response.status().is_success() {
            let status = response.status();
            let error_text = response.text().await.unwrap_or_default();
            anyhow::bail!("API request failed with status {}: {}", status, error_text);
        }
        let text = response
            .text()
            .await
            .context("Failed to parse API response")?
            .trim()
            .to_string();

        if text.is_empty() {
            anyhow::bail!("Received empty response from server");
        }

        Ok(text)
    }


    pub async fn get(&self, endpoint: &str) -> Result<reqwest::Response> {
        let config = self
            .config
            .as_ref()
            .context("Not initialized. Run 'user init' first.")?;

        let api_key = config
            .api_key
            .as_ref()
            .context("API key not found in config")?;

        let url = format!("{}{}", API_BASE_URL, endpoint);
        let response = self
            .client
            .get(&url)
            .header("X-Cli-Id", &config.cli_id)
            .header("Authorization", format!("Bearer {}", api_key))
            .send()
            .await
            .context("Failed to send request")?;
        Ok(response)
    }

    /// Get metadata including all providers, regions, products, and attributes
    pub async fn get_metadata(&self) -> Result<MetadataResponse> {
        let response = self.get("/pricing/metadata").await?;
        
        if !response.status().is_success() {
            let status = response.status();
            let error_text = response.text().await.unwrap_or_default();
            anyhow::bail!("Failed to fetch metadata: {} - {}", status, error_text);
        }
        
        let content = response.bytes().await.context("Failed to read metadata bytes")?;
        
        // Decompress GZip
        use flate2::read::GzDecoder;
        use std::io::Read;
        let mut decoder = GzDecoder::new(&content[..]);
        let mut json_content = String::new();
        decoder.read_to_string(&mut json_content).context("Failed to decompress metadata")?;
        
        let metadata: MetadataResponse = serde_json::from_str(&json_content)
            .context("Failed to parse metadata JSON")?;
        
        Ok(metadata)
    }

    /// Download metadata.json.gz from server and save to local config directory
    pub async fn download_metadata_gz(&self) -> Result<()> {
        let response = self.get("/pricing/metadata").await?;
        
        if !response.status().is_success() {
            let status = response.status();
            let error_text = response.text().await.unwrap_or_default();
            anyhow::bail!("Failed to download metadata.json.gz: {} - {}", status, error_text);
        }
        
        let content = response.bytes().await.context("Failed to read metadata bytes")?;
        
        let config_dir = dirs::home_dir()
            .context("Failed to get home directory")?
            .join(".cloudcent");
        
        std::fs::create_dir_all(&config_dir).context("Failed to create config directory")?;
        
        let file_path = config_dir.join("metadata.json.gz");
        std::fs::write(&file_path, &content).context(format!("Failed to write metadata file to {:?}", file_path))?;
        
        Ok(())
    }

    /// Generate token for CLI authentication
    pub async fn generate_token(&self) -> Result<GenerateTokenResponse> {
        let url = format!("{}/api/auth/generate-token", CLI_BASE_URL);
        
        let response = self
            .client
            .post(&url)
            .send()
            .await
            .context("Failed to generate token")?;
        
        let status = response.status();
        
        if !status.is_success() {
            let error_text = response.text().await.unwrap_or_default();
            anyhow::bail!("Failed to generate token: {} - {}", status, error_text);
        }
        
        let response_text = response.text().await.context("Failed to read response text")?;
        
        let token_response: GenerateTokenResponse = serde_json::from_str(&response_text)
            .context(format!("Failed to parse token response. Response was: {}", response_text))?;
        
        Ok(token_response)
    }

    /// Exchange token for credentials
    pub async fn exchange_token(&self, exchange_code: &str) -> Result<ExchangeResponse> {
        let url = format!("{}/api/auth/exchange", CLI_BASE_URL);
        
        let body = serde_json::json!({
            "exchange_code": exchange_code
        });
        
        let response = self
            .client
            .post(&url)
            .json(&body)
            .send()
            .await
            .context("Failed to exchange token")?;
        
        let status = response.status();
        
        if !status.is_success() {
            let error_text = response.text().await.unwrap_or_default();
            anyhow::bail!("Failed to exchange token: {} - {}", status, error_text);
        }
        
        let response_text = response.text().await.context("Failed to read exchange response")?;
        
        let exchange_response: ExchangeResponse = serde_json::from_str(&response_text)
            .context(format!("Failed to parse exchange response. Response was: {}", response_text))?;
        
        Ok(exchange_response)
    }
}
