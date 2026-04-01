use anyhow::Result;
use crossterm::event::{self, Event, KeyCode, KeyEvent, KeyEventKind};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use crate::commands::user::{UserCommand, CallbackData};
use crate::db::Database;
use super::views::{PricingView, SettingsView, HistoryView};
#[cfg(feature = "estimate")]
use super::views::EstimateView;

/// View modes for the TUI
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ViewMode {
    InitAuth,
    Pricing,
    #[cfg(feature = "estimate")]
    Estimate,
    Settings,
    History,
}

/// Auth states for InitAuth view
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AuthState {
    Prompt,      // Show the initial prompt
    Waiting,     // Waiting for browser auth
    Success,     // Auth successful
    Loading,     // Loading metadata
    Error,       // Auth failed
}

/// Shared result slot for the async pricing API call.
type PricingResult = Arc<Mutex<Option<Result<crate::api::PricingApiResponse, String>>>>;

/// Which view initiated the pricing query
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum PricingQuerySource {
    PricingView,
    #[cfg(feature = "estimate")]
    EstimateView,
}

/// Main application state
pub struct App {
    pub should_quit: bool,
    pub view_mode: ViewMode,
    pub auth_state: AuthState,
    pub user_command: UserCommand,
    pub error_message: Option<String>,
    pub callback_data: Option<std::sync::Arc<std::sync::Mutex<CallbackData>>>,
    pub exchange_code: String,
    pub loading_frame: usize,
    /// Metadata refresh status message: (message, is_success)
    pub metadata_refresh_msg: Option<(String, bool)>,
    pub refresh_msg_frames: usize,
    /// Slot written by the background pricing fetch thread.
    pub pricing_callback: Option<PricingResult>,
    pub pricing_query_source: PricingQuerySource,
    
    // Views
    pub pricing_view: PricingView,
    #[cfg(feature = "estimate")]
    pub estimate_view: EstimateView,
    pub settings_view: SettingsView,
    pub history_view: HistoryView,
    pub db: Option<Database>,
    pub pending_cache_params: Option<(Vec<String>, Vec<String>, std::collections::HashMap<String, String>, Vec<String>)>,
}

impl App {
    pub fn new() -> Result<Self> {
        let user_command = UserCommand::new();
        
        // Check if API key exists
        let view_mode = if user_command.is_initialized() {
            ViewMode::Pricing
        } else {
            ViewMode::InitAuth
        };
        
        let mut app = Self {
            should_quit: false,
            view_mode,
            auth_state: AuthState::Prompt,
            user_command,
            error_message: None,
            callback_data: None,
            exchange_code: String::new(),
            loading_frame: 0,
            metadata_refresh_msg: None,
            refresh_msg_frames: 0,
            pricing_callback: None,
            pricing_query_source: PricingQuerySource::PricingView,
            pricing_view: PricingView::new(),
            #[cfg(feature = "estimate")]
            estimate_view: EstimateView::new(),
            settings_view: SettingsView::new(),
            history_view: HistoryView::new(),
            db: Database::new().ok(),
            pending_cache_params: None,
        };

        if app.view_mode == ViewMode::Pricing {
            app.pricing_view.load_options(app.user_command.client());
        }

        Ok(app)
    }
    
    pub fn run(&mut self, terminal: &mut ratatui::Terminal<ratatui::backend::CrosstermBackend<std::io::Stdout>>) -> Result<()> {
        loop {
            terminal.draw(|f| crate::tui::ui::render(f, self))?;
            
            if self.should_quit {
                break;
            }
            
            // Handle input
            if event::poll(Duration::from_millis(100))? {
                if let Event::Key(key) = event::read()? {
                    // Only handle key press events; ignore Release/Repeat to avoid
                    // double-firing on Windows where crossterm emits multiple events.
                    if key.kind == KeyEventKind::Press {
                        self.handle_key(key)?;
                    }
                }
            }
            
            // Handle async state updates (polling for auth and loading)
            if self.auth_state == AuthState::Waiting || self.auth_state == AuthState::Loading {
                self.check_callback_data()?;
            }
            
            // Poll for completed pricing fetch
            self.check_pricing_callback();

            // Update loading animation & message frames
            if self.auth_state == AuthState::Loading {
                self.loading_frame = (self.loading_frame + 1) % 8;
            }
            if self.refresh_msg_frames > 0 {
                self.refresh_msg_frames -= 1;
                if self.refresh_msg_frames == 0 {
                    self.metadata_refresh_msg = None;
                }
            }
        }
        
        Ok(())
    }
    
    fn handle_key(&mut self, key: KeyEvent) -> Result<()> {
        match self.view_mode {
            ViewMode::InitAuth => self.handle_init_auth_keys(key)?,
            ViewMode::Pricing => self.handle_pricing_keys(key)?,
            #[cfg(feature = "estimate")]
            ViewMode::Estimate => self.handle_estimate_keys(key)?,
            ViewMode::Settings => self.handle_settings_keys(key)?,
            ViewMode::History => self.handle_history_keys(key)?,
        }
        Ok(())
    }
    
    fn handle_init_auth_keys(&mut self, key: KeyEvent) -> Result<()> {
        match self.auth_state {
            AuthState::Prompt => {
                match key.code {
                    KeyCode::Enter => {
                        self.start_auth_flow()?;
                    }
                    KeyCode::Esc => {
                        self.should_quit = true;
                    }
                    _ => {}
                }
            }
            AuthState::Waiting => {
                match key.code {
                    KeyCode::Esc => {
                        self.should_quit = true;
                    }
                    _ => {}
                }
            }
            AuthState::Success => {
                match key.code {
                    KeyCode::Enter => {
                        // Set loading state
                        self.auth_state = AuthState::Loading;
                        self.loading_frame = 0;
                        
                        // Reload config to ensure we have the credentials
                        let _ = self.user_command.client_mut().load_config();
                        
                        // Spawn async task to download metadata
                        let user_command = self.user_command.client().clone();
                        let callback_data = Arc::new(Mutex::new(CallbackData::Pending));
                        let callback_data_clone = callback_data.clone();
                        self.callback_data = Some(callback_data);
                        
                        std::thread::spawn(move || {
                            let rt = tokio::runtime::Runtime::new().unwrap();
                            let result = rt.block_on(async {
                                user_command.download_metadata_gz().await
                            });
                            
                            match result {
                                Ok(_) => {
                                    *callback_data_clone.lock().unwrap() = CallbackData::Received {
                                        cli_id: String::new(),
                                        api_key: String::new(),
                                    };
                                }
                                Err(e) => {
                                    *callback_data_clone.lock().unwrap() = CallbackData::Failed(
                                        e.to_string()
                                    );
                                }
                            }
                        });
                    }
                    KeyCode::Esc => {
                        self.should_quit = true;
                    }
                    _ => {}
                }
            }
            AuthState::Loading => {
                match key.code {
                    KeyCode::Esc => {
                        self.should_quit = true;
                    }
                    _ => {}
                }
            }
            AuthState::Error => {
                match key.code {
                    KeyCode::Enter => {
                        // Retry on Enter
                        self.auth_state = AuthState::Prompt;
                        self.error_message = None;
                        self.callback_data = None;
                    }
                    KeyCode::Esc => {
                        self.should_quit = true;
                    }
                    _ => {}
                }
            }
        }
        Ok(())
    }
    
    fn handle_pricing_keys(&mut self, key: KeyEvent) -> Result<()> {
        // F3: refresh metadata
        if key.code == KeyCode::F(3) {
            self.start_metadata_refresh();
            return Ok(());
        }

        use crate::tui::views::pricing::PricingEvent;
        match self.pricing_view.handle_key(key)? {
            PricingEvent::Quit => self.should_quit = true,
            PricingEvent::NextView => {
                self.switch_view_next();
            }
            PricingEvent::PrevView => {
                self.switch_view_prev();
            }
            PricingEvent::SubmitQuery => {
                self.submit_pricing_query();
            }
            #[cfg(feature = "estimate")]
            PricingEvent::AddToEstimate => {
                self.estimate_view.add_from_pricing(
                    &self.pricing_view.command_builder,
                    &self.pricing_view.filtered_items,
                );
            }
            PricingEvent::None => {}
        }
        Ok(())
    }

    /// Spawn a background thread to fetch pricing data with the current builder params.
    fn submit_pricing_query(&mut self) {
        if self.pricing_view.loading {
            return;
        }

        use crate::tui::views::pricing::CommandMode;
        if self.pricing_view.command_mode == CommandMode::RawCommand {
            let input = self.pricing_view.command_input.clone();
            self.pricing_view.sync_builder_from_raw(&input);
        }

        let builder = &self.pricing_view.command_builder;
        let products = builder.product_tags.clone();
        let regions = builder.region_tags.clone();
        let price_filters = builder.price_tags.clone();
        let mut attrs_map = std::collections::HashMap::new();
        for tag in builder.attribute_tags.iter() {
            if let Some((k, v)) = tag.split_once('=') {
                attrs_map.insert(k.to_string(), v.to_string());
            }
        }

        self.pricing_view.loading = true;
        self.pricing_view.error_message = None;
        self.pricing_query_source = PricingQuerySource::PricingView;

        let client = self.user_command.client().clone();
        let slot: PricingResult = Arc::new(Mutex::new(None));
        let slot_clone = slot.clone();
        self.pricing_callback = Some(slot);

        self.pending_cache_params = Some((products.clone(), regions.clone(), attrs_map.clone(), price_filters.clone()));

        std::thread::spawn(move || {
            let rt = tokio::runtime::Runtime::new().unwrap();
            let result = rt.block_on(async {
                client
                    .fetch_pricing_multi(&products, &regions, attrs_map, &price_filters)
                    .await
                    .map_err(|e| e.to_string())
            });
            *slot_clone.lock().unwrap() = Some(result);
        });
    }

    /// Poll the pricing callback slot and apply results if ready.
    fn check_pricing_callback(&mut self) {
        if let Some(slot) = &self.pricing_callback {
            let result = slot.lock().unwrap().take();
            if let Some(outcome) = result {
                self.pricing_callback = None;

                match self.pricing_query_source {
                    PricingQuerySource::PricingView => {
                        self.pricing_view.loading = false;
                        match outcome {
                            Ok(response) => {
                                use crate::tui::views::pricing::PricingView;
                                self.pricing_view.items = PricingView::convert_response(response.clone());
                                self.pricing_view.filtered_items = self.pricing_view.items.clone();
                                self.pricing_view.selected = 0;
                                self.pricing_view.h_scroll_offset = 0;
                                self.pricing_view.results_page = 0;

                                // Auto-focus results table
                                if !self.pricing_view.filtered_items.is_empty() {
                                    self.pricing_view.active_section = crate::tui::views::pricing::PricingSection::Results;
                                }

                                // Save to cache and history
                                if let Some(ref db) = self.db {
                                    let (products, regions, attrs, prices) = self.pending_cache_params.take().unwrap_or_default();
                                    let cache_key = Database::make_cache_key(&[], &regions, &[], &products, &attrs, &prices);
                                    let _ = db.set_cache(&cache_key, &response);
                                    let attr_list: Vec<String> = attrs.iter().map(|(k, v)| format!("{}={}", k, v)).collect();
                                    let _ = db.add_history(
                                        &[], &regions, &[], &products, &attr_list, &prices,
                                        self.pricing_view.items.len() as u64, &cache_key,
                                    );
                                    self.history_view.load_history(&self.db);
                                }
                            }
                            Err(e) => { self.pricing_view.error_message = Some(e); }
                        }
                    }
                    #[cfg(feature = "estimate")]
                    PricingQuerySource::EstimateView => {
                        self.estimate_view.loading = false;
                        match outcome {
                            Ok(response) => {
                                use crate::tui::views::pricing::PricingView;
                                let items = PricingView::convert_response(response);
                                if let Some((ri, pi)) = self.estimate_view.active_product() {
                                    self.estimate_view.resources[ri].products[pi].results = items;
                                    self.estimate_view.resources[ri].products[pi].result_selected = 0;
                                    if !self.estimate_view.resources[ri].products[pi].results.is_empty() {
                                        self.estimate_view.focus = crate::tui::views::estimate::EstimateFocus::ResultPicker;
                                        self.estimate_view.query_status = Some(("Succeed".to_string(), true));
                                    } else {
                                        self.estimate_view.query_status = Some(("No results found".to_string(), false));
                                    }
                                }
                            }
                            Err(e) => { 
                                self.estimate_view.error_message = Some(e.clone()); 
                                self.estimate_view.query_status = Some((format!("Failed: {}", e), false));
                            }
                        }
                    }
                }
            }
        }
    }
    
    #[cfg(feature = "estimate")]
    fn handle_estimate_keys(&mut self, key: KeyEvent) -> Result<()> {
        use crate::tui::views::estimate::EstimateEvent;
        match self.estimate_view.handle_key(key)? {
            EstimateEvent::Quit => self.should_quit = true,
            EstimateEvent::NextView => self.switch_view_next(),
            EstimateEvent::PrevView => self.switch_view_prev(),
            EstimateEvent::SubmitProductQuery => self.submit_estimate_product_query(),
            EstimateEvent::OpenInPricing(ri, pi) => self.handle_open_in_pricing(ri, pi),
            EstimateEvent::None => {}
        }
        Ok(())
    }

    #[cfg(feature = "estimate")]
    fn submit_estimate_product_query(&mut self) {
        if self.estimate_view.loading { return; }
        let (ri, pi) = match self.estimate_view.active_product() {
            Some(p) => p,
            None => return,
        };
        let builder = &self.estimate_view.resources[ri].products[pi].builder;
        let products = builder.product_tags.clone();
        let regions = builder.region_tags.clone();
        let price_filters = builder.price_tags.clone();
        let mut attrs_map = std::collections::HashMap::new();
        for tag in builder.attribute_tags.iter() {
            if let Some((k, v)) = tag.split_once('=') {
                attrs_map.insert(k.to_string(), v.to_string());
            }
        }

        self.estimate_view.loading = true;
        self.estimate_view.error_message = None;
        self.estimate_view.query_status = None;

        let client = self.user_command.client().clone();
        let slot: PricingResult = Arc::new(Mutex::new(None));
        let slot_clone = slot.clone();
        self.pricing_callback = Some(slot);
        self.pricing_query_source = PricingQuerySource::EstimateView;

        std::thread::spawn(move || {
            let rt = tokio::runtime::Runtime::new().unwrap();
            let result = rt.block_on(async {
                client
                    .fetch_pricing_multi(&products, &regions, attrs_map, &price_filters)
                    .await
                    .map_err(|e| e.to_string())
            });
            *slot_clone.lock().unwrap() = Some(result);
        });
    }

    #[cfg(feature = "estimate")]
    fn handle_open_in_pricing(&mut self, ri: usize, pi: usize) {
        if let Some(res) = self.estimate_view.resources.get(ri) {
            if let Some(product) = res.products.get(pi) {
                // Populate PricingView's builder with tags from estimate product
                self.pricing_view.command_builder = product.builder.clone();
                self.pricing_view.command_mode = crate::tui::views::pricing::CommandMode::CommandBuilder;
                self.view_mode = ViewMode::Pricing;
                self.pricing_view.active_section = crate::tui::views::pricing::PricingSection::Command;
                self.pricing_view.builder_focus = crate::tui::views::pricing::BuilderFocus::Field;
                
                // Trigger query automatically
                self.submit_pricing_query();
            }
        }
    }
    
    fn handle_settings_keys(&mut self, key: KeyEvent) -> Result<()> {
        use crate::tui::views::settings::SettingsEvent;
        match self.settings_view.handle_key(key)? {
            SettingsEvent::Quit => self.should_quit = true,
            SettingsEvent::NextView => self.switch_view_next(),
            SettingsEvent::PrevView => self.switch_view_prev(),
            SettingsEvent::None => {}
        }
        Ok(())
    }

    fn handle_history_keys(&mut self, key: KeyEvent) -> Result<()> {
        use crate::tui::views::history::HistoryEvent;
        match self.history_view.handle_key(key)? {
            HistoryEvent::Quit => self.should_quit = true,
            HistoryEvent::NextView => self.switch_view_next(),
            HistoryEvent::PrevView => self.switch_view_prev(),
            HistoryEvent::ClearAll => {
                if let Some(ref db) = self.db {
                    let _ = db.clear_all();
                    self.history_view.load_history(&self.db);
                    self.history_view.selected_results.clear();
                }
            }
            HistoryEvent::OpenInPricing(idx) => {
                if let Some(h) = self.history_view.history.get(idx) {
                    // Populate PricingView's builder with tags from history
                    self.pricing_view.command_builder.product_tags = h.product_families.split(',')
                        .filter(|s| !s.is_empty())
                        .map(|s| s.to_string())
                        .collect();
                    self.pricing_view.command_builder.region_tags = h.regions.split(',')
                        .filter(|s| !s.is_empty())
                        .map(|s| s.to_string())
                        .collect();
                    self.pricing_view.command_builder.attribute_tags = h.attributes.split(',')
                        .filter(|s| !s.is_empty())
                        .map(|s| s.to_string())
                        .collect();
                    self.pricing_view.command_builder.price_tags = h.prices.split(',')
                        .filter(|s| !s.is_empty())
                        .map(|s| s.to_string())
                        .collect();
                    
                    self.view_mode = crate::tui::app::ViewMode::Pricing;
                    self.pricing_view.active_section = crate::tui::views::pricing::PricingSection::Command;
                    self.pricing_view.command_mode = crate::tui::views::pricing::CommandMode::CommandBuilder;
                    
                    // Trigger query if cache missing, otherwise load from cache
                    if let Some(ref db) = self.db {
                        if let Ok(Some(cached_resp)) = db.get_cache(&h.cache_key) {
                            // Load from cache directly, no API call
                            self.pricing_view.items = crate::tui::views::pricing::PricingView::convert_response(cached_resp);
                            self.pricing_view.filtered_items = self.pricing_view.items.clone();
                            self.pricing_view.selected = 0;
                            self.pricing_view.h_scroll_offset = 0;
                            self.pricing_view.results_page = 0;
                            self.pricing_view.loading = false;
                            // Auto-focus results table
                            if !self.pricing_view.filtered_items.is_empty() {
                                self.pricing_view.active_section = crate::tui::views::pricing::PricingSection::Results;
                            }
                        } else {
                            // Fallback to API call
                            self.submit_pricing_query();
                        }
                    } else {
                        self.submit_pricing_query();
                    }
                }
            }
            HistoryEvent::None => {
                // Update price preview if selection changed
                if let Some(h) = self.history_view.history.get(self.history_view.selected) {
                    if let Some(ref db) = self.db {
                        if let Ok(Some(resp)) = db.get_cache(&h.cache_key) {
                            use crate::tui::views::pricing::PricingView;
                            self.history_view.selected_results = PricingView::convert_response(resp);
                        } else {
                            self.history_view.selected_results.clear();
                        }
                    }
                }
            }
        }
        Ok(())
    }

    fn switch_view_next(&mut self) {
        self.view_mode = match self.view_mode {
            #[cfg(feature = "estimate")]
            ViewMode::Pricing => ViewMode::Estimate,
            #[cfg(not(feature = "estimate"))]
            ViewMode::Pricing => ViewMode::History,
            #[cfg(feature = "estimate")]
            ViewMode::Estimate => ViewMode::History,
            ViewMode::History => ViewMode::Settings,
            ViewMode::Settings => ViewMode::Pricing,
            _ => ViewMode::Pricing,
        };
        self.set_header_focus();
        if self.view_mode == ViewMode::History {
            self.history_view.load_history(&self.db);
        }
    }

    fn switch_view_prev(&mut self) {
        self.view_mode = match self.view_mode {
            ViewMode::Pricing => ViewMode::Settings,
            #[cfg(feature = "estimate")]
            ViewMode::Estimate => ViewMode::Pricing,
            #[cfg(feature = "estimate")]
            ViewMode::History => ViewMode::Estimate,
            #[cfg(not(feature = "estimate"))]
            ViewMode::History => ViewMode::Pricing,
            ViewMode::Settings => ViewMode::History,
            _ => ViewMode::Pricing,
        };
        self.set_header_focus();
        if self.view_mode == ViewMode::History {
            self.history_view.load_history(&self.db);
        }
    }

    fn set_header_focus(&mut self) {
        match self.view_mode {
            #[cfg(feature = "estimate")]
            ViewMode::Estimate => {
                self.estimate_view.active_section = crate::tui::views::estimate::EstimateSection::Header;
                // Pass pricing options to estimate view for suggestions
                if self.estimate_view.options.is_none() {
                    self.estimate_view.options = self.pricing_view.options.clone();
                }
            }
            ViewMode::Settings => {
                self.settings_view.active_section = crate::tui::views::settings::SettingsSection::Header;
            }
            ViewMode::Pricing => {
                self.pricing_view.active_section = crate::tui::views::pricing::PricingSection::Header;
            }
            ViewMode::History => {
                self.history_view.active_section = crate::tui::views::history::HistorySection::Header;
                self.history_view.load_history(&self.db);
            }
            _ => {}
        }
    }
    
    fn start_metadata_refresh(&mut self) {
        if self.auth_state == AuthState::Loading {
            return; // already loading
        }
        self.auth_state = AuthState::Loading;
        self.loading_frame = 0;
        self.refresh_msg_frames = 60; // Show for 60 iterations (~6s at 100ms poll)
        self.metadata_refresh_msg = Some(("Refreshing metadata...".to_string(), true));

        let user_command = self.user_command.client().clone();
        let callback_data = Arc::new(Mutex::new(CallbackData::Pending));
        let callback_data_clone = callback_data.clone();
        self.callback_data = Some(callback_data);

        std::thread::spawn(move || {
            let rt = tokio::runtime::Runtime::new().unwrap();
            let result = rt.block_on(async { user_command.download_metadata_gz().await });
            match result {
                Ok(_) => {
                    *callback_data_clone.lock().unwrap() = CallbackData::Received {
                        cli_id: String::new(),
                        api_key: String::new(),
                    };
                }
                Err(e) => {
                    *callback_data_clone.lock().unwrap() = CallbackData::Failed(e.to_string());
                }
            }
        });
    }

    fn start_auth_flow(&mut self) -> Result<()> {
        let rt = tokio::runtime::Runtime::new()?;
        
        match rt.block_on(async { self.user_command.start_browser_auth_for_tui().await }) {
            Ok((exchange_code, callback_data)) => {
                self.exchange_code = exchange_code.clone();
                self.callback_data = Some(callback_data.clone());
                self.auth_state = AuthState::Waiting;
                
                // 启动轮询任务
                let user_command = UserCommand::new();
                let exchange_code_clone = exchange_code.clone();
                let callback_data_clone = callback_data.clone();
                
                std::thread::spawn(move || {
                    let rt = tokio::runtime::Runtime::new().unwrap();
                    let _ = rt.block_on(async {
                        user_command.poll_for_credentials(&exchange_code_clone, callback_data_clone).await
                    });
                });
                
                Ok(())
            }
            Err(e) => {
                self.error_message = Some(e);
                self.auth_state = AuthState::Error;
                Ok(())
            }
        }
    }
    
    fn check_callback_data(&mut self) -> Result<()> {
        if let Some(callback_data) = &self.callback_data {
            let data = callback_data.lock().unwrap().clone();
            
            match data {
                CallbackData::Received { cli_id, api_key } => {
                    // Check if this is from auth or metadata loading
                    if self.view_mode == ViewMode::InitAuth {
                        // In initial auth flow
                        self.pricing_view.load_options(self.user_command.client());
                        #[cfg(feature = "estimate")]
                        { self.estimate_view.options = self.pricing_view.options.clone(); }
                        self.view_mode = ViewMode::Pricing;
                        self.auth_state = AuthState::Prompt;
                        self.callback_data = None;
                    } else {
                        // On-demand refresh via F3
                        self.pricing_view.load_options(self.user_command.client());
                        #[cfg(feature = "estimate")]
                        { self.estimate_view.options = self.pricing_view.options.clone(); }
                        self.metadata_refresh_msg = Some(("Refresh Succeed".to_string(), true));
                        self.refresh_msg_frames = 40;
                        self.auth_state = AuthState::Prompt; // Reset from Loading
                        self.callback_data = None;
                    }

                    if !cli_id.is_empty() && !api_key.is_empty() {
                        // Auth completed
                        let rt = tokio::runtime::Runtime::new()?;
                        
                        let result = rt.block_on(async {
                            self.user_command.complete_auth_for_tui(&cli_id, &api_key).await
                        });
                        
                        match result {
                            Ok(_) => {
                                self.auth_state = AuthState::Success;
                            }
                            Err(e) => {
                                self.error_message = Some(e);
                                self.auth_state = AuthState::Error;
                            }
                        }
                    }
                }
                CallbackData::Failed(msg) => {
                    self.metadata_refresh_msg = Some((format!("Refresh Error: {}", msg), false));
                    self.refresh_msg_frames = 120; // error shows longer
                    if self.view_mode == ViewMode::InitAuth {
                        self.error_message = Some(msg);
                        self.auth_state = AuthState::Error;
                    } else {
                        self.auth_state = AuthState::Prompt;
                    }
                    self.callback_data = None;
                }
                CallbackData::Pending => {
                    // Still waiting
                }
            }
        }
        
        Ok(())
    }
}
