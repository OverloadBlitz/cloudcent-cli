use anyhow::Result;
use crossterm::event::{KeyCode, KeyEvent};
use indexmap::IndexMap;
use ratatui::{
    layout::{Alignment, Constraint, Direction, Layout, Rect},
    style::{Color, Modifier, Style},
    text::{Line, Span},
    widgets::{Block, BorderType, Borders, Cell, Paragraph, Row, Table},
    Frame,
};

use crate::api::{CloudCentClient, PricingApiResponse};
use crate::tui::semantic::{
    score_and_suggest_products, suggest_attrs, suggest_regions, SuggestionItem,
};

#[derive(Debug, Clone)]
pub struct PricingDisplayItem {
    pub product: String,
    pub region: String,
    pub attributes: IndexMap<String, Option<String>>,
    pub prices: Vec<PriceInfo>,
    pub min_price: Option<String>,
    pub max_price: Option<String>,
}

#[derive(Debug, Clone)]
pub struct RateInfo {
    pub price: String,
    pub start_range: String,
    pub end_range: String,
}

#[derive(Debug, Clone)]
pub struct PriceInfo {
    pub pricing_model: String,
    pub price: String,
    pub unit: String,
    pub upfront_fee: String,
    pub purchase_option: String,
    pub year: String,
    /// All rate tiers (empty if single flat price)
    pub rates: Vec<RateInfo>,
}

// ── Command modes ────────────────────────────────────────────────────────────

#[derive(Clone, PartialEq)]
pub enum CommandMode {
    RawCommand,
    CommandBuilder,
}

/// Within CommandBuilder, tracks whether keyboard focus is on the field rows or the suggestion list.
#[derive(Clone, PartialEq)]
pub enum BuilderFocus {
    Field,
    Suggestions,
}

/// State for the structured command builder.
/// Each field holds a list of selected tag values plus a live search input.
#[derive(Clone, Default)]
pub struct CommandBuilderState {
    /// 0 = Product, 1 = Region, 2 = Attrs, 3 = Price
    pub selected_field: usize,
    pub product_tags: Vec<String>,
    pub region_tags: Vec<String>,
    pub attribute_tags: Vec<String>,
    pub price_tags: Vec<String>,
    /// Text being typed in the currently active field.
    pub search_input: String,
}

impl CommandBuilderState {
    pub fn new() -> Self {
        Self::default()
    }

    #[allow(dead_code)]
    pub fn current_tags(&self) -> &Vec<String> {
        match self.selected_field {
            0 => &self.product_tags,
            1 => &self.region_tags,
            2 => &self.attribute_tags,
            _ => &self.price_tags,
        }
    }

    pub fn current_tags_mut(&mut self) -> &mut Vec<String> {
        match self.selected_field {
            0 => &mut self.product_tags,
            1 => &mut self.region_tags,
            2 => &mut self.attribute_tags,
            _ => &mut self.price_tags,
        }
    }

    #[allow(dead_code)]
    pub fn is_empty(&self) -> bool {
        self.product_tags.is_empty()
            && self.region_tags.is_empty()
            && self.attribute_tags.is_empty()
            && self.price_tags.is_empty()
    }
}

// ── View events / sections ───────────────────────────────────────────────────

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PricingSection {
    Header,
    Command,
    Results,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PricingEvent {
    None,
    Quit,
    PrevView,
    NextView,
    SubmitQuery,
    #[cfg(feature = "estimate")]
    AddToEstimate,
}

// ── Main view struct ─────────────────────────────────────────────────────────

#[derive(Clone)]
pub struct PricingView {
    pub command_mode: CommandMode,
    /// Raw text input (used only in RawCommand mode).
    pub command_input: String,
    pub command_builder: CommandBuilderState,
    /// Whether keyboard focus is on the field rows or the suggestion list (builder mode only).
    pub builder_focus: BuilderFocus,
    pub items: Vec<PricingDisplayItem>,
    pub filtered_items: Vec<PricingDisplayItem>,
    pub active_section: PricingSection,
    pub selected: usize,
    pub results_page: usize,
    pub results_per_page: usize,
    pub loading: bool,
    pub error_message: Option<String>,
    pub options: Option<std::sync::Arc<crate::commands::pricing::PricingOptions>>,
    /// Current suggestion list (shared between both modes).
    pub suggestions_cache: Vec<SuggestionItem>,
    /// Highlighted row in the suggestion list (None = no highlight).
    pub suggestion_index: Option<usize>,
    /// Horizontal scroll offset for the results table (number of scrollable columns to skip).
    pub h_scroll_offset: usize,
    /// Total number of scrollable columns (updated on render).
    pub total_scrollable_cols: std::cell::Cell<usize>,
    /// Number of visible scrollable columns that fit in the table width (updated on render).
    pub visible_scrollable_cols: std::cell::Cell<usize>,
    /// Number of columns in the suggestion panel (updated on render, used for keyboard nav)
    pub suggestion_cols: std::cell::Cell<usize>,
}

impl PricingView {
    pub fn new() -> Self {
        Self {
            // Default to Builder — more discoverable for first-time users.
            command_mode: CommandMode::CommandBuilder,
            command_input: String::new(),
            command_builder: CommandBuilderState::new(),
            builder_focus: BuilderFocus::Field,
            items: Vec::new(),
            filtered_items: Vec::new(),
            active_section: PricingSection::Command,
            selected: 0,
            results_page: 0,
            results_per_page: 15,
            loading: false,
            error_message: None,
            options: None,
            suggestions_cache: Vec::new(),
            suggestion_index: None,
            h_scroll_offset: 0,
            total_scrollable_cols: std::cell::Cell::new(0),
            visible_scrollable_cols: std::cell::Cell::new(0),
            suggestion_cols: std::cell::Cell::new(1),
        }
    }

    pub fn sync_builder_from_raw(&mut self, input: &str) {
        let tokens: Vec<String> = shlex::split(input).unwrap_or_default();
        let mut builder = CommandBuilderState::new();
        
        let mut i = 0;
        while i + 1 < tokens.len() {
            let tk = tokens[i].to_lowercase();
            if tk == "product" {
                for v in tokens[i + 1].split(',') {
                    if !v.is_empty() { builder.product_tags.push(v.to_string()); }
                }
            } else if tk == "region" {
                for v in tokens[i + 1].split(',') {
                    if !v.is_empty() { builder.region_tags.push(v.to_string()); }
                }
            } else if tk == "attrs" {
                for v in tokens[i + 1].split(',') {
                    if !v.is_empty() { builder.attribute_tags.push(v.to_string()); }
                }
            } else if tk == "price" {
                for v in tokens[i + 1].split(',') {
                    if !v.is_empty() { builder.price_tags.push(v.to_string()); }
                }
            }
            i += 1;
        }
        self.command_builder = builder;
    }

    fn field_name(field: usize) -> &'static str {
        match field {
            0 => "Product",
            1 => "Region",
            2 => "Attrs",
            _ => "Price",
        }
    }

    // ── Key handling ─────────────────────────────────────────────────────────

    pub fn handle_key(&mut self, key: KeyEvent) -> Result<PricingEvent> {
        // Global shortcuts active regardless of section
        match key.code {
            KeyCode::F(1) => {
                self.toggle_mode();
                return Ok(PricingEvent::None);
            }
            KeyCode::F(2) => {
                self.command_input.clear();
                self.command_builder = CommandBuilderState::new();
                self.builder_focus = BuilderFocus::Field;
                self.suggestions_cache.clear();
                self.suggestion_index = None;
                self.filter_items();
                if self.active_section == PricingSection::Command {
                    self.update_suggestions();
                }
                return Ok(PricingEvent::None);
            }
            _ => {}
        }

        match self.active_section {
            PricingSection::Header => self.handle_key_header(key),
            PricingSection::Command => self.handle_key_command(key),
            PricingSection::Results => self.handle_key_results(key),
        }
    }

    fn handle_key_header(&mut self, key: KeyEvent) -> Result<PricingEvent> {
        match key.code {
            KeyCode::Left => return Ok(PricingEvent::PrevView),
            KeyCode::Right => return Ok(PricingEvent::NextView),
            KeyCode::Down => {
                self.active_section = PricingSection::Command;
                self.update_suggestions();
            }
            KeyCode::Esc => return Ok(PricingEvent::Quit),
            _ => {}
        }
        Ok(PricingEvent::None)
    }

    fn handle_key_command(&mut self, key: KeyEvent) -> Result<PricingEvent> {
        match key.code {
            KeyCode::Esc => return Ok(PricingEvent::Quit),
            _ => {}
        }

        match self.command_mode {
            CommandMode::CommandBuilder => self.handle_key_builder(key),
            CommandMode::RawCommand => self.handle_key_raw(key),
        }
    }

    fn handle_key_builder(&mut self, key: KeyEvent) -> Result<PricingEvent> {
        match self.builder_focus {
            BuilderFocus::Field => self.handle_key_builder_field(key),
            BuilderFocus::Suggestions => self.handle_key_builder_suggestions(key),
        }
    }

    fn handle_key_builder_field(&mut self, key: KeyEvent) -> Result<PricingEvent> {
        match key.code {
            // Up: move to Header when at top field, otherwise go to previous field
            KeyCode::Up => {
                if self.command_builder.selected_field == 0 {
                    self.active_section = PricingSection::Header;
                } else {
                    self.command_builder.selected_field -= 1;
                    self.command_builder.search_input.clear();
                    self.suggestion_index = None;
                    self.update_suggestions();
                }
            }
            // Down: navigate to the next field, or to Results if at last field and results exist
            KeyCode::Down => {
                if self.command_builder.selected_field == 3 && !self.filtered_items.is_empty() {
                    // At last field and have results, go to Results section
                    self.active_section = PricingSection::Results;
                } else {
                    self.command_builder.selected_field =
                        (self.command_builder.selected_field + 1) % 4;
                    self.command_builder.search_input.clear();
                    self.suggestion_index = None;
                    self.update_suggestions();
                }
            }
            // Right: enter suggestion browsing mode if suggestions exist
            KeyCode::Right => {
                if !self.suggestions_cache.is_empty() {
                    self.builder_focus = BuilderFocus::Suggestions;
                    if self.suggestion_index.is_none() {
                        self.suggestion_index = Some(0);
                    }
                }
            }
            // Enter: submit query to API
            KeyCode::Enter => {
                return Ok(PricingEvent::SubmitQuery);
            }
            // Backspace: delete last char from search_input; if empty, pop last tag
            KeyCode::Backspace => {
                if self.command_builder.search_input.is_empty() {
                    self.command_builder.current_tags_mut().pop();
                    self.filter_items();
                    self.update_suggestions();
                } else {
                    self.command_builder.search_input.pop();
                    self.suggestion_index = None;
                    self.update_suggestions();
                    self.filter_items();
                }
            }
            // Delete: clear search_input or all tags for the current field
            KeyCode::Delete => {
                if self.command_builder.search_input.is_empty() {
                    self.command_builder.current_tags_mut().clear();
                    self.filter_items();
                    self.update_suggestions();
                } else {
                    self.command_builder.search_input.clear();
                    self.suggestion_index = None;
                    self.update_suggestions();
                    self.filter_items();
                }
            }
            // Any printable character: type into the current field's search buffer
            KeyCode::Char(c) => {
                self.command_builder.search_input.push(c);
                self.suggestion_index = None;
                self.update_suggestions();
                self.filter_items();
            }
            _ => {}
        }
        Ok(PricingEvent::None)
    }

    fn handle_key_builder_suggestions(&mut self, key: KeyEvent) -> Result<PricingEvent> {
        let cols = self.suggestion_cols.get().max(1);
        let total = self.suggestions_cache.len();
        match key.code {
            // Up/Down: navigate rows
            KeyCode::Up => {
                if total > 0 {
                    self.suggestion_index = Some(match self.suggestion_index {
                        None | Some(0) => total - 1,
                        Some(i) => i.saturating_sub(cols),
                    });
                }
            }
            KeyCode::Down => {
                if total > 0 {
                    let next = self.suggestion_index.map(|i| (i + cols).min(total - 1)).unwrap_or(0);
                    self.suggestion_index = Some(next);
                } else if !self.filtered_items.is_empty() {
                    self.active_section = PricingSection::Results;
                }
            }
            // Left/Right: navigate columns
            KeyCode::Right => {
                if total > 0 {
                    let next = self.suggestion_index.map(|i| (i + 1).min(total - 1)).unwrap_or(0);
                    self.suggestion_index = Some(next);
                }
            }
            KeyCode::Left if self.suggestion_index.map(|i| i % cols == 0).unwrap_or(true) => {
                // At leftmost column: go back to field focus
                self.builder_focus = BuilderFocus::Field;
                return Ok(PricingEvent::None);
            }
            KeyCode::Left => {
                if total > 0 {
                    let prev = self.suggestion_index.map(|i| i.saturating_sub(1)).unwrap_or(0);
                    self.suggestion_index = Some(prev);
                }
            }
            // Space: toggle selection
            KeyCode::Char(' ') => {
                if let Some(idx) = self.suggestion_index {
                    if idx < total {
                        self.toggle_suggestion(idx);
                    }
                }
            }
            // Enter: submit query
            KeyCode::Enter => {
                return Ok(PricingEvent::SubmitQuery);
            }
            _ => {}
        }
        Ok(PricingEvent::None)
    }
    fn toggle_suggestion(&mut self, idx: usize) {
        if idx >= self.suggestions_cache.len() {
            return;
        }
        let value = self.suggestions_cache[idx].value.clone();
        if value.is_empty() {
            return;
        }
        
        // Attrs phase 1: selected value is a key name (no '=').
        // Transition to value selection by setting search_input to "key=".
        if self.command_builder.selected_field == 2 && !value.contains('=') {
            self.command_builder.search_input = format!("{}=", value);
            self.update_suggestions();
            if idx < self.suggestions_cache.len() {
                self.suggestion_index = Some(idx);
            } else if !self.suggestions_cache.is_empty() {
                self.suggestion_index = Some(0);
            }
        } else if self.command_builder.selected_field == 3 {
            // Price field: append operator and let user type the value
            self.command_builder.search_input = value;
            self.update_suggestions();
            self.suggestion_index = None;
        } else {
            let tags = self.command_builder.current_tags_mut();
            if let Some(pos) = tags.iter().position(|t| t == &value) {
                tags.remove(pos);
            } else {
                tags.push(value.clone());
            }
            self.command_builder.search_input.clear();
            self.update_suggestions();
            self.filter_items();
            // After toggle, position cursor on the same value in the refreshed list
            let new_idx = self.suggestions_cache.iter().position(|s| s.value == value);
            self.suggestion_index = new_idx
                .or_else(|| if self.suggestions_cache.is_empty() { None } else { Some(idx.min(self.suggestions_cache.len() - 1)) });
        }
    }

    fn handle_key_raw(&mut self, key: KeyEvent) -> Result<PricingEvent> {
        match key.code {
            KeyCode::Up => {
                if !self.suggestions_cache.is_empty() {
                    self.suggestion_index = Some(match self.suggestion_index {
                        None | Some(0) => self.suggestions_cache.len() - 1,
                        Some(i) => i - 1,
                    });
                } else {
                    self.active_section = PricingSection::Header;
                }
            }
            KeyCode::Down => {
                if !self.suggestions_cache.is_empty() {
                    let next_idx = self.suggestion_index.map(|i| i + 1).unwrap_or(0);
                    if next_idx >= self.suggestions_cache.len() {
                        if !self.filtered_items.is_empty() {
                            self.active_section = PricingSection::Results;
                            self.suggestion_index = None;
                        } else {
                            self.suggestion_index = Some(0);
                        }
                    } else {
                        self.suggestion_index = Some(next_idx);
                    }
                } else if !self.filtered_items.is_empty() {
                    self.active_section = PricingSection::Results;
                }
            }
            KeyCode::Enter => {
                if let Some(idx) = self.suggestion_index {
                    if idx < self.suggestions_cache.len() {
                        self.apply_raw_suggestion(idx);
                        self.suggestion_index = None;
                        self.update_suggestions();
                        self.filter_items();
                    }
                } else {
                    return Ok(PricingEvent::SubmitQuery);
                }
            }
            KeyCode::Char(c) => {
                self.command_input.push(c);
                self.suggestion_index = None;
                self.update_suggestions();
                self.filter_items();
            }
            KeyCode::Backspace | KeyCode::Delete => {
                self.command_input.pop();
                self.suggestion_index = None;
                self.update_suggestions();
                self.filter_items();
            }
            _ => {}
        }
        Ok(PricingEvent::None)
    }

    fn handle_key_results(&mut self, key: KeyEvent) -> Result<PricingEvent> {
        match key.code {
            KeyCode::Up => {
                // Move up within current page
                let page_start = self.results_page * self.results_per_page;
                if self.selected > page_start {
                    self.selected -= 1;
                } else if self.selected == page_start {
                    // At top of current page, go back to Command
                    self.active_section = PricingSection::Command;
                    self.update_suggestions();
                }
            }
            KeyCode::Down => {
                // Move down within current page
                if !self.filtered_items.is_empty() {
                    let page_end = ((self.results_page + 1) * self.results_per_page)
                        .min(self.filtered_items.len())
                        .saturating_sub(1);
                    if self.selected < page_end {
                        self.selected += 1;
                    }
                }
            }
            KeyCode::Char('j') => {
                // j: 上一页 (as requested)
                if self.results_page > 0 {
                    self.results_page -= 1;
                    self.selected = self.results_page * self.results_per_page;
                }
            }
            KeyCode::Char('k') => {
                // k: 下一页 (as requested)
                let total_pages = (self.filtered_items.len() + self.results_per_page - 1) / self.results_per_page;
                if self.results_page + 1 < total_pages {
                    self.results_page += 1;
                    self.selected = self.results_page * self.results_per_page;
                }
            }
            KeyCode::PageDown => {
                // Explicit page down
                let total_pages = (self.filtered_items.len() + self.results_per_page - 1) / self.results_per_page;
                if self.results_page + 1 < total_pages {
                    self.results_page += 1;
                    self.selected = self.results_page * self.results_per_page;
                }
            }
            KeyCode::PageUp => {
                // Explicit page up
                if self.results_page > 0 {
                    self.results_page -= 1;
                    self.selected = self.results_page * self.results_per_page;
                }
            }
            #[cfg(feature = "estimate")]
            KeyCode::Char('a') => return Ok(PricingEvent::AddToEstimate),
            KeyCode::Left => {
                if self.h_scroll_offset > 0 {
                    self.h_scroll_offset -= 1;
                }
            }
            KeyCode::Right => {
                // Only scroll right if not all columns are already visible
                let total = self.total_scrollable_cols.get();
                let visible = self.visible_scrollable_cols.get();
                if total > visible {
                    let max_offset = total.saturating_sub(visible);
                    if self.h_scroll_offset < max_offset {
                        self.h_scroll_offset += 1;
                    }
                }
            }
            KeyCode::Esc => return Ok(PricingEvent::Quit),
            _ => {}
        }
        Ok(PricingEvent::None)
    }

    // ── Raw-command helpers ───────────────────────────────────────────────────

    /// Replace / append the currently-typed value with a selected suggestion.
    fn apply_raw_suggestion(&mut self, idx: usize) {
        let value = self.suggestions_cache[idx].value.clone();
        if value.is_empty() {
            return;
        }
        let tokens: Vec<String> = shlex::split(&self.command_input).unwrap_or_default();
        let ends_with_space = self.command_input.ends_with(' ');

        let mut active_kw: Option<String> = None;
        let mut kw_index: Option<usize> = None;
        for (i, token) in tokens.iter().enumerate().rev() {
            let lower = token.to_lowercase();
            if ["product", "region", "attrs"].contains(&lower.as_str()) {
                active_kw = Some(lower);
                kw_index = Some(i);
                break;
            }
        }

        match active_kw {
            Some(_) => {
                let base = tokens[..=kw_index.unwrap()].join(" ");
                self.command_input = format!("{} {} ", base, value);
            }
            None => {
                if tokens.is_empty() || ends_with_space {
                    self.command_input = format!("{}{} ", self.command_input.trim_end(), value);
                } else {
                    let base = if tokens.len() > 1 {
                        format!("{} ", tokens[..tokens.len() - 1].join(" "))
                    } else {
                        String::new()
                    };
                    self.command_input = format!("{}{} ", base, value);
                }
            }
        }
    }

    // ── Suggestion update ─────────────────────────────────────────────────────

    /// Rebuild `suggestions_cache` based on the current mode and input state.
    /// Must be called whenever the input changes or the active field changes.
    pub fn update_suggestions(&mut self) {
        let opts = match self.options.clone() {
            Some(o) => o,
            None => {
                self.suggestions_cache.clear();
                return;
            }
        };

        match self.command_mode {
            CommandMode::CommandBuilder => {
                let q = self.command_builder.search_input.clone();
                self.suggestions_cache = match self.command_builder.selected_field {
                    0 => score_and_suggest_products(
                        &q,
                        &opts.products,
                        &opts.attribute_values,
                        &opts.product_groups,
                        &self.command_builder.product_tags,
                    ),
                    1 => suggest_regions(
                        &q,
                        &opts.regions,
                        &opts.product_regions,
                        &self.command_builder.product_tags,
                        &self.command_builder.region_tags,
                    ),
                    2 => {
                        suggest_attrs(
                            &q,
                            &self.command_builder.product_tags,
                            &opts.product_attrs,
                            &opts.attribute_values,
                            &self.command_builder.attribute_tags,
                        )
                    }
                    _ => {
                        // Suggest price operators
                        [">", "<", ">=", "<="].iter()
                            .filter(|op| q.is_empty() || op.starts_with(&q))
                            .map(|op| crate::tui::semantic::SuggestionItem {
                                value: op.to_string(),
                                display: format!("{} (price operator)", op),
                                reason: "operator".to_string(),
                                is_semantic: false,
                                already_selected: false,
                            }).collect()
                    }
                };
            }
            CommandMode::RawCommand => {
                let input = self.command_input.trim_end().to_string();
                let tokens: Vec<String> = shlex::split(&input).unwrap_or_default();
                let ends_with_space = self.command_input.ends_with(' ');

                if tokens.is_empty() {
                    self.suggestions_cache = ["product", "region", "attrs", "price"]
                        .iter()
                        .map(|k| SuggestionItem {
                            value: k.to_string(),
                            display: k.to_string(),
                            reason: "keyword".to_string(),
                            is_semantic: false,
                            already_selected: false,
                        })
                        .collect();
                    return;
                }

                // Find the most recent keyword in the token list
                let mut active_kw: Option<String> = None;
                let mut kw_index: Option<usize> = None;
                for (i, token) in tokens.iter().enumerate().rev() {
                    let lower = token.to_lowercase();
                    if ["product", "region", "attrs", "price"].contains(&lower.as_str()) {
                        active_kw = Some(lower);
                        kw_index = Some(i);
                        break;
                    }
                }

                match active_kw {
                    None => {
                        // No keyword found — suggest keyword completions
                        let last = if ends_with_space {
                            ""
                        } else {
                            tokens.last().map(String::as_str).unwrap_or("")
                        };
                        self.suggestions_cache = ["product", "region", "attrs", "price"]
                            .iter()
                            .filter(|k| last.is_empty() || k.starts_with(last))
                            .map(|k| SuggestionItem {
                                value: k.to_string(),
                                display: k.to_string(),
                                reason: "keyword".to_string(),
                                is_semantic: false,
                                already_selected: false,
                            })
                            .collect();
                    }
                    Some(kw) => {
                        let idx = kw_index.unwrap();
                        // Value query: tokens after the keyword (if any) and not ending with space
                        let value_query = if ends_with_space || tokens.len() <= idx + 1 {
                            String::new()
                        } else {
                            tokens[idx + 1..].join(" ").to_lowercase()
                        };

                        // Collect already-selected values for this keyword occurrence
                        let mut selected_vals: Vec<String> = Vec::new();
                        let mut i = 0;
                        while i + 1 < tokens.len() {
                            if tokens[i].to_lowercase() == kw {
                                for v in tokens[i + 1].split(',') {
                                    if !v.is_empty() {
                                        selected_vals.push(v.to_string());
                                    }
                                }
                            }
                            i += 1;
                        }

                        // Context for RawCommand: parse all tokens to find current selections
                        let mut prod_tags: Vec<String> = Vec::new();
                        let mut j = 0;
                        while j + 1 < tokens.len() {
                            let tk = tokens[j].to_lowercase();
                            if tk == "product" { prod_tags.push(tokens[j + 1].clone()); }
                            j += 1;
                        }

                        self.suggestions_cache = match kw.as_str() {
                            "product" => score_and_suggest_products(
                                &value_query,
                                &opts.products,
                                &opts.attribute_values,
                                &opts.product_groups,
                                &selected_vals,
                            ),
                            "region" => {
                                suggest_regions(
                                    &value_query,
                                    &opts.regions,
                                    &opts.product_regions,
                                    &prod_tags,
                                    &selected_vals,
                                )
                            }
                            "attrs" => {
                                suggest_attrs(
                                    &value_query,
                                    &prod_tags,
                                    &opts.product_attrs,
                                    &opts.attribute_values,
                                    &selected_vals,
                                )
                            }
                            "price" => {
                                // Suggest price operators for raw command
                                [">", "<", ">=", "<="].iter()
                                    .filter(|op| value_query.is_empty() || op.starts_with(&value_query))
                                    .map(|op| crate::tui::semantic::SuggestionItem {
                                        value: op.to_string(),
                                        display: format!("{} (price operator)", op),
                                        reason: "operator".to_string(),
                                        is_semantic: false,
                                        already_selected: false,
                                    }).collect()
                            }
                            _ => Vec::new(),
                        };
                    }
                }
            }
        }
    }

    // ── Rendering ────────────────────────────────────────────────────────────

    pub fn render(&self, f: &mut Frame, active: bool) {
        let area = f.area();

        // Compute price details height dynamically based on selected item
        let price_count = self.filtered_items.get(self.selected)
            .map(|item| item.prices.len())
            .unwrap_or(0);
        // Two columns, so rows needed = ceil(count / 2), plus 3 for borders + header
        let details_h: u16 = if price_count == 0 {
            4
        } else {
            (((price_count + 1) / 2) as u16 + 3).max(4)
        };

        let main_chunks = Layout::default()
            .direction(Direction::Vertical)
            .constraints(vec![
                Constraint::Length(3),                      // Header
                Constraint::Length(10),                     // Command + Suggestions
                Constraint::Min(3),                         // Results table
                Constraint::Length(details_h),              // Price details
                Constraint::Length(3),                      // Help bar
            ])
            .split(area);

        let mid_chunks = Layout::default()
            .direction(Direction::Horizontal)
            .constraints(vec![
                Constraint::Percentage(50), // Left: Command (Raw or Builder)
                Constraint::Percentage(50), // Right: Suggestions
            ])
            .split(main_chunks[1]);

        self.render_header(f, main_chunks[0], active);
        match self.command_mode {
            CommandMode::RawCommand => self.render_raw_command(f, mid_chunks[0], active),
            CommandMode::CommandBuilder => self.render_builder_command(f, mid_chunks[0], active),
        }
        self.render_suggestions(f, mid_chunks[1], active);
        self.render_results(f, main_chunks[2], active);
        self.render_price_details(f, main_chunks[3]);
        self.render_help(f, main_chunks[4], active);
    }

    fn render_header(&self, f: &mut Frame, area: Rect, active: bool) {
        let is_focused = active && self.active_section == PricingSection::Header;

        let border_type = if is_focused { BorderType::Thick } else { BorderType::Plain };
        let border_color = if is_focused {
            Color::Green
        } else if active {
            Color::Cyan
        } else {
            Color::DarkGray
        };

        let pricing_style = if is_focused {
            Style::default()
                .fg(Color::Black)
                .bg(Color::Green)
                .add_modifier(Modifier::BOLD)
        } else if active {
            Style::default().fg(Color::Cyan).add_modifier(Modifier::BOLD)
        } else {
            Style::default().fg(Color::DarkGray)
        };

        let title = if is_focused {
            Line::from(vec![
                Span::styled(
                    format!(" > CloudCent CLI v{} < ", crate::VERSION),
                    Style::default()
                        .fg(Color::Green)
                        .add_modifier(Modifier::BOLD),
                ),
            ])
        } else {
            Line::from(format!(" CloudCent CLI v{} ", crate::VERSION))
        };

        let mut nav_spans = vec![
            Span::styled(
                if is_focused { " > Pricing < " } else { " Pricing " },
                pricing_style,
            ),
        ];
        #[cfg(feature = "estimate")]
        {
            nav_spans.push(Span::styled(" | ", Style::default().fg(Color::DarkGray)));
            nav_spans.push(Span::styled("Estimate", Style::default().fg(Color::DarkGray)));
        }
        nav_spans.push(Span::styled(" | ", Style::default().fg(Color::DarkGray)));
        nav_spans.push(Span::styled("History", Style::default().fg(Color::DarkGray)));
        nav_spans.push(Span::styled(" | ", Style::default().fg(Color::DarkGray)));
        nav_spans.push(Span::styled("Settings", Style::default().fg(Color::DarkGray)));

        let text = vec![Line::from(nav_spans)];

        f.render_widget(
            Paragraph::new(text).block(
                Block::default()
                    .borders(Borders::ALL)
                    .border_type(border_type)
                    .title(title)
                    .title_alignment(Alignment::Center)
                    .border_style(Style::default().fg(border_color)),
            ),
            area,
        );
    }

    /// Raw command: single-line input with keyword/value colour coding.
    fn render_raw_command(&self, f: &mut Frame, area: Rect, active: bool) {
        let is_focused = active && self.active_section == PricingSection::Command;
        let border_type = if is_focused { BorderType::Thick } else { BorderType::Plain };
        let border_color = if is_focused { Color::Yellow } else { Color::DarkGray };
        let title_style = if is_focused {
            Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD)
        } else {
            Style::default().fg(Color::DarkGray)
        };

        const KEYWORDS: &[&str] = &["provider", "region", "product", "attrs", "price"];

        let content = if self.command_input.is_empty() {
            vec![Line::from(vec![
                Span::styled("> ", Style::default().fg(Color::DarkGray)),
                Span::styled(
                    "type: provider aws  region us-east-1  product ec2...",
                    Style::default()
                        .fg(Color::DarkGray)
                        .add_modifier(Modifier::ITALIC),
                ),
            ])]
        } else {
            let tokens: Vec<String> =
                shlex::split(&self.command_input).unwrap_or_default();
            let ends_with_space = self.command_input.ends_with(' ');

            let mut spans: Vec<Span> =
                vec![Span::styled("> ", Style::default().fg(Color::DarkGray))];

            for (i, token) in tokens.iter().enumerate() {
                let is_last = i == tokens.len() - 1;
                let lower = token.to_lowercase();

                if KEYWORDS.contains(&lower.as_str()) {
                    // Keywords: bold cyan
                    spans.push(Span::styled(
                        token.clone(),
                        Style::default().fg(Color::Cyan).add_modifier(Modifier::BOLD),
                    ));
                } else {
                    // Values: green (comma-separated multi-values still one span)
                    spans.push(Span::styled(
                        token.clone(),
                        Style::default().fg(Color::Green),
                    ));
                }

                if !is_last || ends_with_space {
                    spans.push(Span::raw(" "));
                }
            }
            // Blinking cursor indicator
            spans.push(Span::styled("|", Style::default().fg(Color::Cyan)));

            vec![Line::from(spans)]
        };

        f.render_widget(
            Paragraph::new(content).block(
                Block::default()
                    .borders(Borders::ALL)
                    .border_type(border_type)
                    .title(Line::from(vec![
                        Span::styled(if is_focused { " > " } else { "   " }, title_style),
                        Span::styled(" Command [Raw]  F1=Builder ", title_style),
                    ]))
                    .border_style(Style::default().fg(border_color)),
            ),
            area,
        );
    }

    /// Builder: vertical layout — one row per field showing tag chips + search input.
    fn render_builder_command(&self, f: &mut Frame, area: Rect, active: bool) {
        let is_focused = active && self.active_section == PricingSection::Command 
            && self.builder_focus == BuilderFocus::Field;
        let border_type = if is_focused { BorderType::Thick } else { BorderType::Plain };
        let border_color = if is_focused { Color::Green } else { Color::DarkGray };
        let title_style = if is_focused {
            Style::default().fg(Color::Green).add_modifier(Modifier::BOLD)
        } else {
            Style::default().fg(Color::DarkGray)
        };

        let mut lines: Vec<Line> = Vec::new();
        for field_idx in 0..4usize {
            let field_name = Self::field_name(field_idx);
            let is_active_field =
                field_idx == self.command_builder.selected_field && is_focused;

            let indicator =
                if is_active_field { "> " } else { "  " };
            let label_style = if is_active_field {
                Style::default().fg(Color::Cyan).add_modifier(Modifier::BOLD)
            } else {
                Style::default().fg(Color::DarkGray)
            };
            let tag_style = if is_active_field {
                Style::default()
                    .fg(Color::Black)
                    .bg(Color::Cyan)
                    .add_modifier(Modifier::BOLD)
            } else {
                Style::default().fg(Color::White).bg(Color::DarkGray)
            };

            let tags: &Vec<String> = match field_idx {
                0 => &self.command_builder.product_tags,
                1 => &self.command_builder.region_tags,
                2 => &self.command_builder.attribute_tags,
                _ => &self.command_builder.price_tags,
            };

            let mut spans: Vec<Span> = vec![
                Span::styled(
                    indicator,
                    Style::default().fg(if is_active_field {
                        Color::Green
                    } else {
                        Color::DarkGray
                    }),
                ),
                Span::styled(format!("{:16}", field_name), label_style),
                Span::styled(": ", Style::default().fg(Color::DarkGray)),
            ];

            // Render selected tags as chips: [value] (no × — remove with Backspace)
            for tag in tags.iter() {
                spans.push(Span::styled(format!(" {} ", tag), tag_style));
                spans.push(Span::raw(" "));
            }

            // Search buffer and hint on the active field
            if is_active_field {
                if self.command_builder.search_input.is_empty() {
                    let hint = if tags.is_empty() {
                        "(→ browse · Enter submit · type to search)"
                    } else {
                        "(→ browse · ⌫ remove last · Del clear all)"
                    };
                    spans.push(Span::styled(
                        hint,
                        Style::default()
                            .fg(Color::DarkGray)
                            .add_modifier(Modifier::ITALIC),
                    ));
                } else {
                    spans.push(Span::styled(
                        self.command_builder.search_input.clone(),
                        Style::default().fg(Color::White),
                    ));
                    spans.push(Span::styled(
                        "|",
                        Style::default().fg(Color::Cyan),
                    ));
                }
            }

            lines.push(Line::from(spans));
        }

        f.render_widget(
            Paragraph::new(lines).block(
                Block::default()
                    .borders(Borders::ALL)
                    .border_type(border_type)
                    .title(Line::from(vec![
                        Span::styled(if is_focused { " > " } else { "   " }, title_style),
                        Span::styled(" Command [Builder]  F1=Raw ", title_style),
                    ]))
                    .border_style(Style::default().fg(border_color)),
            ),
            area,
        );
    }

    /// Navigable suggestion list with semantic / fuzzy grouping by colour.
    fn render_suggestions(&self, f: &mut Frame, area: Rect, active: bool) {
        let cmd_active = active && self.active_section == PricingSection::Command;
        let suggestion_focused = cmd_active
            && self.command_mode == CommandMode::CommandBuilder
            && self.builder_focus == BuilderFocus::Suggestions;

        let title = match self.command_mode {
            CommandMode::CommandBuilder => {
                let hint = if cmd_active && self.builder_focus == BuilderFocus::Field {
                    "  [→ browse]"
                } else if cmd_active && self.builder_focus == BuilderFocus::Suggestions {
                    "  [← back · Space select]"
                } else {
                    ""
                };
                format!(
                    " {} Suggestions ({}){}",
                    Self::field_name(self.command_builder.selected_field),
                    self.suggestions_cache.len(),
                    hint,
                )
            }
            CommandMode::RawCommand => {
                format!(" Suggestions ({}) ", self.suggestions_cache.len())
            }
        };

        let border_type = if suggestion_focused { BorderType::Thick } else { BorderType::Plain };
        let border_color = if suggestion_focused {
            Color::Green
        } else if cmd_active {
            Color::Cyan
        } else {
            Color::DarkGray
        };

        if self.suggestions_cache.is_empty() {
            let msg = if self.options.is_none() {
                "(Sync metadata to see suggestions)"
            } else {
                "(No matches)"
            };
            f.render_widget(
                Paragraph::new(Line::from(Span::styled(
                    msg,
                    Style::default()
                        .fg(Color::DarkGray)
                        .add_modifier(Modifier::ITALIC),
                )))
                .block(
                    Block::default()
                        .borders(Borders::ALL)
                        .border_type(border_type)
                        .title(title)
                        .border_style(Style::default().fg(border_color)),
                ),
                area,
            );
            return;
        }

        // Multi-column layout: calculate how many columns fit
        let inner_w = area.width.saturating_sub(2) as usize; // minus borders
        let inner_h = area.height.saturating_sub(2).max(1) as usize;

        // Each cell: display + reason, min width 20, max 40
        let max_display = self.suggestions_cache.iter().map(|s| s.display.len()).max().unwrap_or(10);
        let max_reason = self.suggestions_cache.iter().map(|s| s.reason.len()).max().unwrap_or(0);
        let cell_w = (4 + max_display + if max_reason > 0 { 2 + max_reason } else { 0 }).clamp(18, 38);
        let num_cols = (inner_w / cell_w).max(1);
        self.suggestion_cols.set(num_cols);
        let num_rows = inner_h;

        // Determine scroll: keep selected item visible
        let sel_idx = self.suggestion_index.unwrap_or(0);
        let sel_row = sel_idx / num_cols;
        let scroll_row = if sel_row >= num_rows { sel_row - num_rows + 1 } else { 0 };

        let mut lines: Vec<Line> = Vec::new();
        for row in scroll_row..(scroll_row + num_rows) {
            let mut spans: Vec<Span> = Vec::new();
            for col in 0..num_cols {
                let i = row * num_cols + col;
                if i >= self.suggestions_cache.len() {
                    break;
                }
                let item = &self.suggestions_cache[i];
                let is_sel = self.suggestion_index == Some(i);

                let (item_style, reason_style, check) = if is_sel && item.already_selected {
                    // Cursor on a selected item: cyan bg + checkmark
                    (
                        Style::default().fg(Color::Black).bg(Color::Green).add_modifier(Modifier::BOLD),
                        Style::default().fg(Color::Black).bg(Color::Green),
                        "✓",
                    )
                } else if is_sel {
                    (
                        Style::default().fg(Color::Black).bg(Color::Cyan).add_modifier(Modifier::BOLD),
                        Style::default().fg(Color::Black).bg(Color::Cyan),
                        ">",
                    )
                } else if item.already_selected {
                    (Style::default().fg(Color::Green), Style::default().fg(Color::DarkGray), "✓")
                } else if item.is_semantic {
                    (Style::default().fg(Color::Yellow), Style::default().fg(Color::DarkGray), " ")
                } else {
                    (Style::default().fg(Color::Gray), Style::default().fg(Color::DarkGray), " ")
                };

                // Build cell content: [check] display [reason]
                let display_str = format!("{} {}", check, item.display);
                let reason_str = if !item.reason.is_empty() {
                    format!(" {}", item.reason)
                } else {
                    String::new()
                };
                // Pad cell to cell_w
                let content_len = display_str.len() + reason_str.len();
                let pad = cell_w.saturating_sub(content_len);

                spans.push(Span::styled(display_str, item_style));
                if !reason_str.is_empty() {
                    spans.push(Span::styled(reason_str, reason_style));
                }
                spans.push(Span::raw(" ".repeat(pad.max(1))));
            }
            if !spans.is_empty() {
                lines.push(Line::from(spans));
            }
        }

        f.render_widget(
            Paragraph::new(lines).block(
                Block::default()
                    .borders(Borders::ALL)
                    .border_type(border_type)
                    .title(title)
                    .border_style(Style::default().fg(border_color)),
            ),
            area,
        );
    }

    fn normalize_na(s: &str) -> &str {
        let trimmed = s.trim();
        if trimmed.eq_ignore_ascii_case("na") || trimmed.eq_ignore_ascii_case("n/a") {
            "-"
        } else {
            s
        }
    }

    fn render_results(&self, f: &mut Frame, area: Rect, active: bool) {
        let is_focused = active && self.active_section == PricingSection::Results;
        let border_type = if is_focused { BorderType::Thick } else { BorderType::Plain };
        let border_color = if is_focused { Color::Green } else { Color::DarkGray };
        let title_style = if is_focused {
            Style::default().fg(Color::Green).add_modifier(Modifier::BOLD)
        } else {
            Style::default().fg(Color::DarkGray)
        };

        if self.loading {
            f.render_widget(
                Paragraph::new(Line::from(Span::styled(
                    "Loading pricing data...",
                    Style::default().fg(Color::Yellow),
                )))
                .alignment(Alignment::Center)
                .block(
                    Block::default()
                        .borders(Borders::ALL)
                        .border_type(border_type)
                        .title(" Results ")
                        .border_style(Style::default().fg(border_color)),
                ),
                area,
            );
            return;
        }

        if let Some(ref error) = self.error_message {
            f.render_widget(
                Paragraph::new(Line::from(Span::styled(
                    format!("Error: {}", error),
                    Style::default().fg(Color::Red),
                )))
                .alignment(Alignment::Center)
                .block(
                    Block::default()
                        .borders(Borders::ALL)
                        .title(" Error ")
                        .border_style(Style::default().fg(Color::Red)),
                ),
                area,
            );
            return;
        }

        if self.filtered_items.is_empty() {
            f.render_widget(
                Paragraph::new(Line::from(Span::styled(
                    "No results found. Press Enter to submit query.",
                    Style::default().fg(Color::DarkGray),
                )))
                .alignment(Alignment::Center)
                .block(
                    Block::default()
                        .borders(Borders::ALL)
                        .border_type(border_type)
                        .title(Line::from(vec![
                            Span::styled(if is_focused { " > " } else { "   " }, title_style),
                            Span::styled(" Results (0) ", title_style),
                        ]))
                        .border_style(Style::default().fg(border_color)),
                ),
                area,
            );
            return;
        }

        // Collect all unique attribute keys preserving API insertion order (from first item)
        let mut attr_keys_ordered: Vec<String> = Vec::new();
        let mut attr_keys_seen: std::collections::HashSet<String> = std::collections::HashSet::new();
        let mut pricing_models: std::collections::HashSet<String> = std::collections::HashSet::new();
        
        for item in &self.filtered_items {
            for key in item.attributes.keys() {
                if attr_keys_seen.insert(key.clone()) {
                    attr_keys_ordered.push(key.clone());
                }
            }
            for price in &item.prices {
                pricing_models.insert(price.pricing_model.clone());
            }
        }

        let attr_keys_sorted = attr_keys_ordered; // keep API order
        
        let mut pricing_models_sorted: Vec<String> = pricing_models.into_iter().collect();
        pricing_models_sorted.sort();

        // Build header: No | Product | Region | [Attributes...] | Min Price | Max Price
        // Frozen columns: No, Product, Region
        // Scrollable columns: attributes + Min Price + Max Price
        // Compute dynamic widths for frozen columns based on actual content
        let product_col_w = self.filtered_items.iter()
            .map(|i| i.product.len())
            .max().unwrap_or(7)
            .max(7) as u16 + 2; // +2 padding
        let region_col_w = self.filtered_items.iter()
            .map(|i| i.region.len())
            .max().unwrap_or(6)
            .max(6) as u16 + 2;

        let frozen_header_cells: Vec<Cell> = vec![
            Cell::from("No."),
            Cell::from("Product"),
            Cell::from("Region"),
        ].into_iter().map(|c| c.style(Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD))).collect();

        let mut scrollable_headers: Vec<String> = Vec::new();
        for attr_key in &attr_keys_sorted {
            scrollable_headers.push(attr_key.clone());
        }
        scrollable_headers.push("Min Price".to_string());
        scrollable_headers.push("Max Price".to_string());

        // Compute content-based widths for scrollable columns
        let scrollable_col_widths: Vec<u16> = (0..scrollable_headers.len()).map(|i| {
            if i < attr_keys_sorted.len() {
                let header_w = attr_keys_sorted[i].len() as u16;
                let content_w = self.filtered_items.iter()
                    .map(|item| {
                        item.attributes.get(&attr_keys_sorted[i])
                            .and_then(|v| v.as_ref())
                            .map(|v| v.len())
                            .unwrap_or(1) as u16
                    })
                    .max().unwrap_or(1);
                header_w.max(content_w).max(8) + 2
            } else {
                // Min Price / Max Price
                14
            }
        }).collect();

        // Store total scrollable column count for key handler
        self.total_scrollable_cols.set(scrollable_headers.len());

        // Determine which scrollable columns fit in the remaining width (from offset 0 first)
        let frozen_width: u16 = 5 + product_col_w + region_col_w;
        let available_width = area.width.saturating_sub(frozen_width + 2 + 1); // 2 for borders, 1 for separator

        // Check how many columns fit from offset 0 to decide if scrolling is needed
        let mut all_fit_count = 0;
        let mut all_fit_width: u16 = 0;
        for i in 0..scrollable_headers.len() {
            let col_w = scrollable_col_widths[i];
            if all_fit_width + col_w > available_width && all_fit_count > 0 {
                break;
            }
            all_fit_width += col_w;
            all_fit_count += 1;
        }
        let all_columns_fit = all_fit_count >= scrollable_headers.len();

        // Clamp h_scroll_offset locally for rendering (key handler caps the actual value)
        let h_offset = if all_columns_fit {
            0
        } else {
            let max_offset = scrollable_headers.len().saturating_sub(1);
            self.h_scroll_offset.min(max_offset)
        };

        let mut visible_scrollable_count = 0;
        let mut used_width: u16 = 0;
        for i in h_offset..scrollable_headers.len() {
            let col_w = scrollable_col_widths[i];
            if used_width + col_w > available_width && visible_scrollable_count > 0 {
                break;
            }
            used_width += col_w;
            visible_scrollable_count += 1;
        }
        self.visible_scrollable_cols.set(visible_scrollable_count);

        // Build final header row
        let mut header_cells = frozen_header_cells;
        for i in h_offset..(h_offset + visible_scrollable_count).min(scrollable_headers.len()) {
            header_cells.push(
                Cell::from(scrollable_headers[i].clone())
                    .style(Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD))
            );
        }
        let header_row = Row::new(header_cells).height(1);

        // Calculate how many rows fit in the visible area (minus header + borders)
        let visible_rows = area.height.saturating_sub(3) as usize; // 2 borders + 1 header
        let total_items = self.filtered_items.len();
        let total_pages = (total_items + self.results_per_page - 1) / self.results_per_page;

        // Vertical scroll within the current page: keep selected item visible
        let page_start = self.results_page * self.results_per_page;
        let local_sel = self.selected.saturating_sub(page_start);
        let v_scroll = if local_sel >= visible_rows {
            local_sel - visible_rows + 1
        } else {
            0
        };

        // Build rows for current page only, capped at results_per_page
        let start_idx = page_start;
        let rows: Vec<Row> = self
            .filtered_items
            .iter()
            .enumerate()
            .skip(start_idx + v_scroll)
            .take(self.results_per_page.min(visible_rows))
            .map(|(idx, item)| {
                let style = if idx == self.selected {
                    Style::default().bg(Color::DarkGray).fg(Color::White)
                } else {
                    Style::default()
                };
                
                let mut cells: Vec<Cell> = vec![
                    Cell::from(format!("{}", idx + 1)),
                    Cell::from(item.product.clone()),
                    Cell::from(item.region.clone()),
                ];
                
                // Add only visible scrollable columns
                for i in h_offset..(h_offset + visible_scrollable_count).min(scrollable_headers.len()) {
                    if i < attr_keys_sorted.len() {
                        let attr_key = &attr_keys_sorted[i];
                        let value = item.attributes.get(attr_key)
                            .and_then(|v| v.clone())
                            .unwrap_or_else(|| "-".to_string());
                        cells.push(Cell::from(Self::normalize_na(&value).to_string()));
                    } else if i == scrollable_headers.len() - 2 {
                        // Min Price
                        let v = item.min_price.clone().unwrap_or_else(|| "-".to_string());
                        cells.push(Cell::from(Self::normalize_na(&v).to_string()));
                    } else {
                        // Max Price
                        let v = item.max_price.clone().unwrap_or_else(|| "-".to_string());
                        cells.push(Cell::from(Self::normalize_na(&v).to_string()));
                    }
                }
                
                Row::new(cells).style(style)
            })
            .collect();

        // Build constraints: frozen columns + visible scrollable columns
        // Distribute remaining space evenly across all columns
        let total_col_count = 3 + visible_scrollable_count; // No + Product + Region + scrollable
        let base_widths: Vec<u16> = {
            let mut w = vec![5u16, product_col_w, region_col_w];
            for i in h_offset..(h_offset + visible_scrollable_count).min(scrollable_headers.len()) {
                w.push(scrollable_col_widths[i]);
            }
            w
        };
        let total_base: u16 = base_widths.iter().sum();
        let table_inner_width = area.width.saturating_sub(2); // borders
        let extra = table_inner_width.saturating_sub(total_base);
        let per_col_extra = if total_col_count > 0 { extra / total_col_count as u16 } else { 0 };

        let constraints: Vec<Constraint> = base_widths.iter().map(|&w| {
            Constraint::Length(w + per_col_extra)
        }).collect();

        f.render_widget(
            Table::new(rows, constraints)
                .header(header_row)
                .block(
                    Block::default()
                        .borders(Borders::ALL)
                        .border_type(border_type)
                        .title(Line::from({
                            let mut spans = vec![
                                Span::styled(if is_focused { " > " } else { "   " }, title_style),
                                Span::styled(
                                    format!(" Results ({} total, page {}/{}) ", 
                                        self.filtered_items.len(), 
                                        self.results_page + 1, 
                                        total_pages.max(1)),
                                    title_style,
                                ),
                            ];
                            if !all_columns_fit {
                                spans.push(Span::styled(
                                    format!(" ←→ col {}/{} ", h_offset + 1, scrollable_headers.len()),
                                    Style::default().fg(Color::Cyan),
                                ));
                            }
                            spans
                        }))
                        .border_style(Style::default().fg(border_color)),
                ),
            area,
        );
    }

    fn render_price_details(&self, f: &mut Frame, area: Rect) {
        let item = match self.filtered_items.get(self.selected) {
            Some(i) => i,
            None => {
                f.render_widget(
                    Paragraph::new(Line::from(vec![
                        Span::styled("Select a result to see pricing details", Style::default().fg(Color::DarkGray).add_modifier(Modifier::ITALIC)),
                    ]))
                    .block(
                        Block::default()
                            .borders(Borders::ALL)
                            .title(" Price Details ")
                            .border_style(Style::default().fg(Color::DarkGray)),
                    ),
                    area,
                );
                return;
            }
        };

        if item.prices.is_empty() {
            f.render_widget(
                Paragraph::new(Line::from(Span::styled(
                    "No price details available",
                    Style::default().fg(Color::DarkGray).add_modifier(Modifier::ITALIC),
                )))
                .block(
                    Block::default()
                        .borders(Borders::ALL)
                        .title(" Price Details ")
                        .border_style(Style::default().fg(Color::DarkGray)),
                ),
                area,
            );
            return;
        }

        // Check if any price has tiered rates (more than 1 rate)
        let has_tiers = item.prices.iter().any(|p| p.rates.len() > 1);

        if has_tiers {
            self.render_price_details_tiered(f, area, item);
        } else {
            self.render_price_details_flat(f, area, item);
        }
    }

    fn render_price_details_tiered(&self, f: &mut Frame, area: Rect, item: &PricingDisplayItem) {
        let title = Line::from(vec![
            Span::styled(" > Price Details - ", Style::default().fg(Color::Cyan).add_modifier(Modifier::BOLD)),
            Span::styled(
                format!("{} / {} ({} models, tiered)", item.region, item.product, item.prices.len()),
                Style::default().fg(Color::White),
            ),
            Span::styled(" ", Style::default()),
        ]);

        // Build rows: for each price, show a header row then rate tiers
        let mut rows: Vec<Row> = Vec::new();
        for p in &item.prices {
            // Price summary row
            let model_label = format!(
                "{}{}{}",
                p.pricing_model,
                if !p.purchase_option.is_empty() { format!(" ({})", p.purchase_option) } else { String::new() },
                if !p.year.is_empty() && p.year != "-" { format!(" {}yr", p.year) } else { String::new() },
            );
            rows.push(Row::new(vec![
                Cell::from(model_label).style(Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD)),
                Cell::from(""),
                Cell::from(""),
                Cell::from(p.unit.clone()).style(Style::default().fg(Color::DarkGray)),
                Cell::from(if p.upfront_fee.is_empty() || p.upfront_fee == "0" { String::new() } else { format!("upfront: {}", p.upfront_fee) })
                    .style(Style::default().fg(Color::DarkGray)),
            ]));

            if p.rates.len() <= 1 {
                // Single rate, show inline
                let price_str = p.rates.first().map(|r| r.price.clone()).unwrap_or(p.price.clone());
                rows.push(Row::new(vec![
                    Cell::from("  flat"),
                    Cell::from(price_str).style(Style::default().fg(Color::Green)),
                    Cell::from(""),
                    Cell::from(""),
                    Cell::from(""),
                ]));
            } else {
                // Multiple tiers
                for r in &p.rates {
                    let range = if r.start_range.is_empty() && r.end_range.is_empty() {
                        String::new()
                    } else {
                        let start = if r.start_range.is_empty() { "0".to_string() } else { r.start_range.clone() };
                        let end = if r.end_range.is_empty() || r.end_range == "Inf" { "∞".to_string() } else { r.end_range.clone() };
                        format!("{} - {}", start, end)
                    };
                    rows.push(Row::new(vec![
                        Cell::from(format!("  {}", range)).style(Style::default().fg(Color::DarkGray)),
                        Cell::from(r.price.clone()).style(Style::default().fg(Color::Green)),
                        Cell::from(""),
                        Cell::from(""),
                        Cell::from(""),
                    ]));
                }
            }
        }

        let constraints = vec![
            Constraint::Min(20),   // Model / Range
            Constraint::Min(12),   // Price
            Constraint::Length(0), // spacer
            Constraint::Min(10),   // Unit
            Constraint::Min(14),   // Upfront
        ];

        f.render_widget(
            Table::new(rows, constraints)
                .block(
                    Block::default()
                        .borders(Borders::ALL)
                        .title(title)
                        .border_style(Style::default().fg(Color::Cyan)),
                ),
            area,
        );
    }

    fn render_price_details_flat(&self, f: &mut Frame, area: Rect, item: &PricingDisplayItem) {
        let cols = Layout::default()
            .direction(Direction::Horizontal)
            .constraints(vec![
                Constraint::Percentage(50),
                Constraint::Percentage(50),
            ])
            .split(area);

        let visible_rows = cols[0].height.saturating_sub(3) as usize; // borders(2) + header(1)
        // Left gets first half, right gets the rest
        let (left_prices, right_prices) = if item.prices.len() <= visible_rows {
            (item.prices.as_slice(), &[] as &[PriceInfo])
        } else {
            let split = ((item.prices.len() + 1) / 2).max(1);
            (&item.prices[..split], &item.prices[split..])
        };

        let title = Line::from(vec![
            Span::styled(" > Price Details - ", Style::default().fg(Color::Cyan).add_modifier(Modifier::BOLD)),
            Span::styled(
                format!("{} / {} ({} models)", item.region, item.product, item.prices.len()),
                Style::default().fg(Color::White),
            ),
            Span::styled(" ", Style::default()),
        ]);

        // Compute dynamic column widths based on actual content
        let all_prices = &item.prices;
        let model_w = all_prices.iter().map(|p| p.pricing_model.len()).max().unwrap_or(5).max(5) as u16 + 2;
        let price_w = all_prices.iter().map(|p| p.price.len()).max().unwrap_or(5).max(5) as u16 + 2;
        let unit_w = all_prices.iter().map(|p| p.unit.len()).max().unwrap_or(4).max(4) as u16 + 2;
        let upfront_w = all_prices.iter().map(|p| p.upfront_fee.len()).max().unwrap_or(7).max(7) as u16 + 2;
        let year_w: u16 = 4;
        let option_w = all_prices.iter().map(|p| p.purchase_option.len()).max().unwrap_or(6).max(6) as u16 + 2;

        let make_table = |prices: &[PriceInfo]| -> Table {
            let header_cells = vec![
                Cell::from("Model").style(Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD)),
                Cell::from("Price").style(Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD)),
                Cell::from("Unit").style(Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD)),
                Cell::from("Upfront").style(Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD)),
                Cell::from("Yr").style(Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD)),
                Cell::from("Option").style(Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD)),
            ];
            let header_row = Row::new(header_cells).height(1);

            let rows: Vec<Row> = prices.iter().enumerate().map(|(i, p)| {
                let row_style = if i % 2 == 0 {
                    Style::default().fg(Color::Gray)
                } else {
                    Style::default().fg(Color::White)
                };
                Row::new(vec![
                    Cell::from(p.pricing_model.clone()),
                    Cell::from(p.price.clone()).style(Style::default().fg(Color::Green)),
                    Cell::from(p.unit.clone()),
                    Cell::from(if p.upfront_fee.is_empty() || p.upfront_fee == "0" { "-".to_string() } else { p.upfront_fee.clone() }),
                    Cell::from(if p.year.is_empty() { "-".to_string() } else { p.year.clone() }),
                    Cell::from(if p.purchase_option.is_empty() { "-".to_string() } else { p.purchase_option.clone() }),
                ]).style(row_style)
            }).collect();

            let constraints = vec![
                Constraint::Min(model_w),
                Constraint::Min(price_w),
                Constraint::Min(unit_w),
                Constraint::Min(upfront_w),
                Constraint::Length(year_w),
                Constraint::Min(option_w),
            ];

            Table::new(rows, constraints).header(header_row)
        };

        // Left table (with title)
        f.render_widget(
            make_table(left_prices).block(
                Block::default()
                    .borders(Borders::ALL)
                    .title(title)
                    .border_style(Style::default().fg(Color::Cyan)),
            ),
            cols[0],
        );

        // Right table
        if right_prices.is_empty() {
            f.render_widget(
                Paragraph::new("").block(
                    Block::default()
                        .borders(Borders::ALL)
                        .border_style(Style::default().fg(Color::Cyan)),
                ),
                cols[1],
            );
        } else {
            f.render_widget(
                make_table(right_prices).block(
                    Block::default()
                        .borders(Borders::ALL)
                        .title(" (cont.) ")
                        .border_style(Style::default().fg(Color::Cyan)),
                ),
                cols[1],
            );
        }
    }

    fn render_help(&self, f: &mut Frame, area: Rect, _active: bool) {
        let text = match self.active_section {
            PricingSection::Header => Line::from(vec![
                Span::styled("[←→] ", Style::default().fg(Color::Cyan)),
                Span::raw("Switch View  "),
                Span::styled("[↓] ", Style::default().fg(Color::Yellow)),
                Span::raw("Command  "),
                Span::styled("[F1] ", Style::default().fg(Color::Magenta)),
                Span::raw("Toggle Mode  "),
                Span::styled("[F3] ", Style::default().fg(Color::Green)),
                Span::raw("Refresh Metadata  "),
                Span::styled("[Esc] ", Style::default().fg(Color::Red)),
                Span::raw("Quit"),
            ]),
            PricingSection::Command => match self.command_mode {
                CommandMode::CommandBuilder => match self.builder_focus {
                    BuilderFocus::Field => Line::from(vec![
                        Span::styled("[↑↓] ", Style::default().fg(Color::Cyan)),
                        Span::raw("Field / Header  "),
                        Span::styled("[→] ", Style::default().fg(Color::Cyan)),
                        Span::raw("Browse Suggestions  "),
                        Span::styled("[Enter] ", Style::default().fg(Color::Green)),
                        Span::raw("Submit  "),
                        Span::styled("[⌫] ", Style::default().fg(Color::Yellow)),
                        Span::raw("Remove  "),
                        Span::styled("[F1] ", Style::default().fg(Color::Magenta)),
                        Span::raw("Raw  "),
                        Span::styled("[F2] ", Style::default().fg(Color::Yellow)),
                        Span::raw("Reset  "),
                        Span::styled("[F3] ", Style::default().fg(Color::Green)),
                        Span::raw("Refresh  "),
                        Span::styled("[←→] ", Style::default().fg(Color::Cyan)),
                        Span::raw("Switch View (Header)"),
                    ]),
                    BuilderFocus::Suggestions => Line::from(vec![
                        Span::styled("[↑↓] ", Style::default().fg(Color::Cyan)),
                        Span::raw("Navigate  "),
                        Span::styled("[Space] ", Style::default().fg(Color::Green)),
                        Span::raw("Select  "),
                        Span::styled("[←/Esc] ", Style::default().fg(Color::Yellow)),
                        Span::raw("Back  "),
                        Span::styled("[Enter] ", Style::default().fg(Color::Green)),
                        Span::raw("Submit Query"),
                    ]),
                },
                CommandMode::RawCommand => Line::from(vec![
                    Span::styled("[↑↓] ", Style::default().fg(Color::Cyan)),
                    Span::raw("Browse  "),
                    Span::styled("[Enter] ", Style::default().fg(Color::Green)),
                    Span::raw("Select  "),
                    Span::styled("[F1] ", Style::default().fg(Color::Magenta)),
                    Span::raw("Builder  "),
                    Span::styled("[F2] ", Style::default().fg(Color::Yellow)),
                    Span::raw("Reset All  "),
                    Span::styled("[F3] ", Style::default().fg(Color::Green)),
                    Span::raw("Refresh  "),
                    Span::styled("[Esc] ", Style::default().fg(Color::Red)),
                    Span::raw("Quit"),
                ]),
            },
            PricingSection::Results => Line::from(vec![
                Span::styled("[j/k] ", Style::default().fg(Color::Cyan)),
                Span::raw("Scroll  "),
                Span::styled("[←→] ", Style::default().fg(Color::Cyan)),
                Span::raw("H-Scroll  "),
                Span::styled("[↑] ", Style::default().fg(Color::Yellow)),
                Span::raw("Command  "),
                Span::styled("[Esc] ", Style::default().fg(Color::Red)),
                Span::raw("Quit"),
            ]),
        };

        f.render_widget(
            Paragraph::new(text).block(
                Block::default()
                    .borders(Borders::ALL)
                    .title(" Help ")
                    .border_style(Style::default().fg(Color::DarkGray)),
            ),
            area,
        );
    }

    // ── Filtering ─────────────────────────────────────────────────────────────

    pub fn filter_items(&mut self) {
        match self.command_mode {
            CommandMode::RawCommand => {
                if self.command_input.is_empty() {
                    self.filtered_items = self.items.clone();
                    self.selected = 0;
                    return;
                }

                use fuzzy_matcher::skim::SkimMatcherV2;
                use fuzzy_matcher::FuzzyMatcher;

                let matcher = SkimMatcherV2::default();
                let query = self.command_input.to_lowercase();
                // Use shlex so quoted multi-word values become single tokens
                let terms: Vec<String> = shlex::split(&query).unwrap_or_else(|| {
                    query.split_whitespace().map(String::from).collect()
                });

                let mut scored: Vec<(PricingDisplayItem, i64)> = self
                    .items
                    .iter()
                    .filter_map(|item| {
                        let searchable = format!(
                            "{} {} {}",
                            item.product,
                            item.region,
                            item.prices.iter().map(|p| p.price.as_str()).collect::<Vec<_>>().join(" ")
                        )
                        .to_lowercase();

                        let all_match = terms
                            .iter()
                            .all(|t| matcher.fuzzy_match(&searchable, t).is_some());

                        if all_match {
                            let score =
                                matcher.fuzzy_match(&searchable, &query).unwrap_or(0);
                            Some((item.clone(), score))
                        } else {
                            None
                        }
                    })
                    .collect();

                scored.sort_by(|a, b| b.1.cmp(&a.1));
                self.filtered_items =
                    scored.into_iter().map(|(item, _)| item).collect();
                self.selected = 0;
            }

            CommandMode::CommandBuilder => {
                let r_tags: Vec<String> = self
                    .command_builder
                    .region_tags
                    .iter()
                    .map(|s| s.to_lowercase())
                    .collect();
                let prod_tags: Vec<String> = self
                    .command_builder
                    .product_tags
                    .iter()
                    .map(|s| s.to_lowercase())
                    .collect();
                let attr_tags: Vec<String> = self
                    .command_builder
                    .attribute_tags
                    .iter()
                    .map(|s| s.to_lowercase())
                    .collect();
                let price_tags: Vec<String> = self
                    .command_builder
                    .price_tags
                    .iter()
                    .map(|s| s.to_lowercase())
                    .collect();

                if r_tags.is_empty()
                    && prod_tags.is_empty()
                    && attr_tags.is_empty()
                    && price_tags.is_empty()
                {
                    self.filtered_items = self.items.clone();
                    self.selected = 0;
                    return;
                }

                self.filtered_items = self
                    .items
                    .iter()
                    .filter(|item| {
                        // Region: OR across selected region tags
                        let region_ok = r_tags.is_empty()
                            || r_tags.iter().any(|t| {
                                item.region.to_lowercase().contains(t.as_str())
                            });
                        // Product: OR across selected product tags
                        let product_ok = prod_tags.is_empty()
                            || prod_tags.iter().any(|t| {
                                item.product.to_lowercase().contains(t.as_str())
                            });
                        // Attrs: ALL selected attr tags must be present (AND)
                        let attrs_ok = attr_tags.is_empty() || {
                            let flat = item
                                .attributes
                                .iter()
                                .map(|(k, v)| {
                                    format!(
                                        "{}={}",
                                        k.to_lowercase(),
                                        v.as_deref().unwrap_or("")
                                    )
                                })
                                .collect::<Vec<_>>()
                                .join(" ");
                            attr_tags
                                .iter()
                                .all(|t| flat.contains(t.as_str()))
                        };

                        // Price: local filtering by comparing with min_price
                        let price_ok = price_tags.is_empty() || {
                            item.min_price.as_ref().map(|mp| {
                                price_tags.iter().all(|pt| {
                                    if let Ok(mp_val) = mp.parse::<f64>() {
                                        if pt.starts_with('>') {
                                            if let Ok(v) = pt[1..].parse::<f64>() { return mp_val > v; }
                                        } else if pt.starts_with('<') {
                                            if let Ok(v) = pt[1..].parse::<f64>() { return mp_val < v; }
                                        }
                                    }
                                    true
                                })
                            }).unwrap_or(true)
                        };

                        region_ok && product_ok && attrs_ok && price_ok
                    })
                    .cloned()
                    .collect();

                self.selected = 0;
            }
        }
    }

    fn toggle_mode(&mut self) {
        match self.command_mode {
            CommandMode::RawCommand => {
                // Parse raw tokens → populate builder tags
                let tokens: Vec<String> =
                    shlex::split(&self.command_input).unwrap_or_default();
                self.command_builder = CommandBuilderState::new();

                let mut i = 0;
                while i < tokens.len() {
                    let lower = tokens[i].to_lowercase();
                    if i + 1 < tokens.len() {
                        let values: Vec<String> = tokens[i + 1]
                            .split(',')
                            .map(|v| v.trim().to_string())
                            .filter(|v| !v.is_empty())
                            .collect();
                        match lower.as_str() {
                            "product" => {
                                for v in values {
                                    if !self.command_builder.product_tags.contains(&v) {
                                        self.command_builder.product_tags.push(v);
                                    }
                                }
                                i += 2;
                                continue;
                            }
                            "region" => {
                                for v in values {
                                    if !self.command_builder.region_tags.contains(&v) {
                                        self.command_builder.region_tags.push(v);
                                    }
                                }
                                i += 2;
                                continue;
                            }
                            "attrs" => {
                                for v in values {
                                    if !self.command_builder.attribute_tags.contains(&v) {
                                        self.command_builder.attribute_tags.push(v);
                                    }
                                }
                                i += 2;
                                continue;
                            }
                            "price" => {
                                for v in values {
                                    if !self.command_builder.price_tags.contains(&v) {
                                        self.command_builder.price_tags.push(v);
                                    }
                                }
                                i += 2;
                                continue;
                            }
                            _ => {}
                        }
                    }
                    i += 1;
                }

                self.command_mode = CommandMode::CommandBuilder;
            }

            CommandMode::CommandBuilder => {
                // Serialise builder tags → raw command string
                let mut parts: Vec<String> = Vec::new();
                if !self.command_builder.product_tags.is_empty() {
                    parts.push(format!(
                        "product {}",
                        self.command_builder.product_tags.join(",")
                    ));
                }
                if !self.command_builder.region_tags.is_empty() {
                    parts.push(format!(
                        "region {}",
                        self.command_builder.region_tags.join(",")
                    ));
                }
                if !self.command_builder.attribute_tags.is_empty() {
                    parts.push(format!(
                        "attrs {}",
                        self.command_builder.attribute_tags.join(",")
                    ));
                }
                if !self.command_builder.price_tags.is_empty() {
                    parts.push(format!(
                        "price {}",
                        self.command_builder.price_tags.join(",")
                    ));
                }
                self.command_input = parts.join(" ");
                self.command_mode = CommandMode::RawCommand;
            }
        }

        self.builder_focus = BuilderFocus::Field;
        self.suggestions_cache.clear();
        self.suggestion_index = None;
        self.update_suggestions();
        // Skip filter_items() to preserve current results
    }

    // ── Data loading ──────────────────────────────────────────────────────────

    pub fn load_options(&mut self, client: &CloudCentClient) {
        let config = client.get_config().cloned();
        match crate::commands::pricing::load_metadata_async(config) {
            Ok(options) => {
                self.options = Some(options);
                self.update_suggestions();
                self.filter_items();
            }
            Err(e) => {
                self.error_message = Some(format!("Failed to load metadata: {}", e));
            }
        }
    }


    #[allow(dead_code)]
    pub async fn load_data(&mut self, client: &CloudCentClient) {
        self.loading = true;
        self.error_message = None;

        match client
            .fetch_pricing_multi(&[], &[], std::collections::HashMap::new(), &[])
            .await
        {
            Ok(response) => {
                self.items = Self::convert_response(response);
                self.filtered_items = self.items.clone();
                self.loading = false;
                self.h_scroll_offset = 0;
            }
            Err(e) => {
                self.error_message = Some(e.to_string());
                self.loading = false;
            }
        }
    }

    fn stringify_json(v: &Option<serde_json::Value>) -> Option<String> {
        let s = match v {
            Some(serde_json::Value::String(s)) => s.clone(),
            Some(serde_json::Value::Number(n)) => format!("{}", n),
            Some(serde_json::Value::Bool(b)) => format!("{}", b),
            _ => return None,
        };
        if s.trim().is_empty() { None } else { Some(s) }
    }

    pub fn convert_response(response: PricingApiResponse) -> Vec<PricingDisplayItem> {
        response
            .data
            .into_iter()
            .map(|item| {
                let prices: Vec<PriceInfo> = item.prices.iter().map(|p| {
                    let display_price = if let Some(rates) = &p.rates {
                        if let Some(first_rate) = rates.first() {
                            Self::stringify_json(&first_rate.price).unwrap_or_else(|| "N/A".to_string())
                        } else {
                            "N/A".to_string()
                        }
                    } else {
                        "N/A".to_string()
                    };

                    let rate_infos: Vec<RateInfo> = if let Some(rates) = &p.rates {
                        rates.iter().map(|r| RateInfo {
                            price: Self::stringify_json(&r.price).unwrap_or_else(|| "N/A".to_string()),
                            start_range: Self::stringify_json(&r.start_range).unwrap_or_default(),
                            end_range: Self::stringify_json(&r.end_range).unwrap_or_default(),
                        }).collect()
                    } else {
                        Vec::new()
                    };

                    PriceInfo {
                        pricing_model: p.pricing_model.clone().unwrap_or_else(|| "OnDemand".to_string()),
                        price: display_price,
                        unit: p.unit.clone().unwrap_or_default(),
                        upfront_fee: Self::stringify_json(&p.upfront_fee).unwrap_or_default(),
                        purchase_option: p.purchase_option.clone().unwrap_or_default(),
                        year: Self::stringify_json(&p.year).unwrap_or_default(),
                        rates: rate_infos,
                    }
                }).collect();

                PricingDisplayItem {
                    product: if item.provider.is_empty() {
                        item.product
                    } else {
                        format!("{} {}", item.provider, item.product)
                    },
                    region: item.region,
                    attributes: item.attributes,
                    prices,
                    min_price: Self::stringify_json(&item.min_price),
                    max_price: Self::stringify_json(&item.max_price),
                }
            })
            .collect()
    }
}
