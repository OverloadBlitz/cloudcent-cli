use anyhow::Result;
use crossterm::event::{KeyCode, KeyEvent};
use ratatui::{
    layout::{Alignment, Constraint, Direction, Layout},
    style::{Color, Modifier, Style},
    text::{Line, Span},
    widgets::{Block, BorderType, Borders, List, ListItem, Paragraph},
    Frame,
};

use crate::db::{Database, QueryHistory};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HistorySection {
    Header,
    Content,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HistoryEvent {
    None,
    Quit,
    PrevView,
    NextView,
    ClearAll,
    OpenInPricing(usize),
}

pub struct HistoryView {
    pub active_section: HistorySection,
    pub history: Vec<QueryHistory>,
    pub selected: usize,
    pub selected_results: Vec<crate::tui::views::pricing::PricingDisplayItem>,
}

impl HistoryView {
    pub fn new() -> Self {
        Self {
            active_section: HistorySection::Content,
            history: Vec::new(),
            selected: 0,
            selected_results: Vec::new(),
        }
    }

    pub fn load_history(&mut self, db: &Option<Database>) {
        if let Some(db) = db {
            if let Ok(hist) = db.get_history(100) {
                self.history = hist;
                if self.selected >= self.history.len() && !self.history.is_empty() {
                    self.selected = self.history.len() - 1;
                }
            }
        }
    }

    pub fn handle_key(&mut self, key: KeyEvent) -> Result<HistoryEvent> {
        match key.code {
            KeyCode::Up => {
                if self.active_section == HistorySection::Content {
                    if self.selected > 0 {
                        self.selected -= 1;
                    } else {
                        self.active_section = HistorySection::Header;
                    }
                }
                Ok(HistoryEvent::None)
            }
            KeyCode::Down => {
                if self.active_section == HistorySection::Header {
                    self.active_section = HistorySection::Content;
                } else if !self.history.is_empty() && self.selected + 1 < self.history.len() {
                    self.selected += 1;
                }
                Ok(HistoryEvent::None)
            }
            KeyCode::Left if self.active_section == HistorySection::Header => {
                Ok(HistoryEvent::PrevView)
            }
            KeyCode::Right if self.active_section == HistorySection::Header => {
                Ok(HistoryEvent::NextView)
            }
            KeyCode::Char('c') | KeyCode::Char('C') => {
                Ok(HistoryEvent::ClearAll)
            }
            KeyCode::Enter if self.active_section == HistorySection::Content => {
                if !self.history.is_empty() {
                    Ok(HistoryEvent::OpenInPricing(self.selected))
                } else {
                    Ok(HistoryEvent::None)
                }
            }
            KeyCode::Esc => Ok(HistoryEvent::Quit),
            _ => Ok(HistoryEvent::None),
        }
    }

    pub fn render(&self, f: &mut Frame, active: bool, cache_stats: (usize, usize)) {
        let area = f.area();

        let chunks = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(3),  // Header
                Constraint::Min(10),    // Content
                Constraint::Length(1),  // Help bar
            ])
            .split(area);

        // Header Focus Logic
        let header_section_active = active && self.active_section == HistorySection::Header;
        let header_border_type = if header_section_active { BorderType::Thick } else { BorderType::Plain };
        let header_border_color = if header_section_active {
            Color::Green
        } else if active {
            Color::Cyan
        } else {
            Color::DarkGray
        };

        let pricing_style = Style::default().fg(Color::DarkGray);
        #[cfg(feature = "estimate")]
        let estimate_style = Style::default().fg(Color::DarkGray);
        let settings_style = Style::default().fg(Color::DarkGray);
        let history_style = if header_section_active {
            Style::default().fg(Color::Black).bg(Color::Green).add_modifier(Modifier::BOLD)
        } else if active {
            Style::default().fg(Color::Cyan).add_modifier(Modifier::BOLD)
        } else {
            Style::default().fg(Color::DarkGray)
        };

        let mut nav_spans = vec![
            Span::styled("Pricing", pricing_style),
        ];
        #[cfg(feature = "estimate")]
        {
            nav_spans.push(Span::raw(" | "));
            nav_spans.push(Span::styled("Estimate", estimate_style));
        }
        nav_spans.push(Span::raw(" | "));
        nav_spans.push(Span::styled(
            if header_section_active { " > History < " } else { " History " },
            history_style,
        ));
        nav_spans.push(Span::raw(" | "));
        nav_spans.push(Span::styled("Settings", settings_style));
        let header_text = vec![Line::from(nav_spans)];

        let header_title = if header_section_active {
            Line::from(vec![
                Span::styled(" > ", Style::default().fg(Color::Green).add_modifier(Modifier::BOLD)),
                Span::styled(format!("CloudCent CLI v{}", crate::VERSION), Style::default().fg(Color::Green).add_modifier(Modifier::BOLD)),
                Span::styled(" < ", Style::default().fg(Color::Green).add_modifier(Modifier::BOLD)),
            ])
        } else {
            Line::from(format!(" CloudCent CLI v{} ", crate::VERSION))
        };

        let header = Paragraph::new(header_text).block(
            Block::default()
                .borders(Borders::ALL)
                .border_type(header_border_type)
                .title(header_title)
                .title_alignment(Alignment::Center)
                .border_style(Style::default().fg(header_border_color)),
        );

        f.render_widget(header, chunks[0]);

        // Content
        let content_section_active = active && self.active_section == HistorySection::Content;
        let content_border_type = if content_section_active { BorderType::Thick } else { BorderType::Plain };
        let content_border_color = if content_section_active { Color::Green } else { Color::DarkGray };
        let content_title_style = if content_section_active { Style::default().fg(Color::Green).add_modifier(Modifier::BOLD) } else { Style::default().fg(Color::DarkGray) };

        let content_chunks = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Min(10),    // List and Stats
                Constraint::Length(10),  // Price Preview
            ])
            .split(chunks[1]);

        let top_chunks = Layout::default()
            .direction(Direction::Horizontal)
            .constraints([
                Constraint::Percentage(70), // History List
                Constraint::Percentage(30), // Cache Stats
            ])
            .split(content_chunks[0]);

        // History List
        let items: Vec<ListItem> = self.history.iter().enumerate().map(|(i, h)| {
            let style = if i == self.selected && content_section_active {
                Style::default().bg(Color::DarkGray).fg(Color::White)
            } else {
                Style::default()
            };

            let mut spans = vec![
                Span::styled(format!("[{}] ", h.created_at.to_rfc3339().chars().skip(11).take(5).collect::<String>()), Style::default().fg(Color::DarkGray)),
                Span::styled(format!("{} ", h.product_families), Style::default().fg(Color::Green)),
            ];
            
            if !h.regions.is_empty() {
                spans.push(Span::styled(format!("@{} ", h.regions), Style::default().fg(Color::Yellow)));
            }
            
            if !h.attributes.is_empty() {
                spans.push(Span::styled(format!("{{{}}} ", h.attributes), Style::default().fg(Color::Magenta)));
            }
            
            spans.push(Span::styled(format!("-> {} items", h.result_count), Style::default().fg(Color::White)));

            ListItem::new(Line::from(spans)).style(style)
        }).collect();

        let list = List::new(items)
            .block(
                Block::default()
                    .borders(Borders::ALL)
                    .border_type(content_border_type)
                    .title(Line::from(vec![
                        Span::styled(if content_section_active { " > " } else { " " }, content_title_style),
                        Span::styled(" Command History ", content_title_style),
                    ]))
                    .border_style(Style::default().fg(content_border_color)),
            );

        f.render_widget(list, top_chunks[0]);

        // Cache Stats & Actions
        let (cache_count, cache_size) = cache_stats;
        let size_mb = cache_size as f64 / 1024.0 / 1024.0;
        
        let stats_text = vec![
            Line::from(vec![
                Span::styled(" Cache: ", Style::default().fg(Color::Cyan)),
                Span::raw(format!("{} items, {:.2} MB", cache_count, size_mb)),
            ]),
            Line::from(""),
            Line::from(vec![
                Span::styled(" [c] ", Style::default().fg(Color::Red).add_modifier(Modifier::BOLD)),
                Span::raw("clear all (history + cache)"),
            ]),
        ];

        let stats = Paragraph::new(stats_text)
            .block(
                Block::default()
                    .borders(Borders::ALL)
                    .title(" Settings ")
                    .border_style(Style::default().fg(Color::DarkGray)),
            )
            .alignment(Alignment::Left);

        f.render_widget(stats, top_chunks[1]);

        // Price Preview
        let preview_title = if let Some(h) = self.history.get(self.selected) {
            format!(" Price Preview for #{} ", h.id)
        } else {
            " Price Preview ".to_string()
        };

        if self.selected_results.is_empty() {
            f.render_widget(
                Paragraph::new("No results found in cache for this query.")
                    .block(Block::default().borders(Borders::ALL).title(preview_title))
                    .style(Style::default().fg(Color::DarkGray)),
                content_chunks[1],
            );
        } else {
            use ratatui::widgets::{Row, Cell, Table};
            let product_w = self.selected_results.iter()
                .map(|i| i.product.len()).max().unwrap_or(7).max(7) as u16 + 2;
            let region_w = self.selected_results.iter()
                .map(|i| i.region.len()).max().unwrap_or(6).max(6) as u16 + 2;
            let rows: Vec<Row> = self.selected_results.iter().take(5).map(|item| {
                Row::new(vec![
                    Cell::from(item.product.clone()).style(Style::default().fg(Color::Green)),
                    Cell::from(item.region.clone()).style(Style::default().fg(Color::Yellow)),
                    Cell::from(item.min_price.clone().unwrap_or_default()).style(Style::default().fg(Color::White).add_modifier(Modifier::BOLD)),
                ])
            }).collect();

            let table = Table::new(rows, [
                Constraint::Length(product_w),
                Constraint::Length(region_w),
                Constraint::Min(10),
            ])
            .header(Row::new(vec!["Product", "Region", "Price"]).style(Style::default().fg(Color::Yellow)))
            .block(Block::default().borders(Borders::ALL).title(preview_title));

            f.render_widget(table, content_chunks[1]);
        }

        // Help bar
        let help_text = match self.active_section {
            HistorySection::Header => {
                Line::from(vec![
                    Span::styled("[←→] ", Style::default().fg(Color::Cyan)),
                    Span::raw("Switch View  "),
                    Span::styled("[↓] ", Style::default().fg(Color::Yellow)),
                    Span::raw("Go to Content  "),
                    Span::styled("[Esc] ", Style::default().fg(Color::Red)),
                    Span::raw("Quit"),
                ])
            }
            HistorySection::Content => {
                Line::from(vec![
                    Span::styled("[↑↓] ", Style::default().fg(Color::Cyan)),
                    Span::raw("Navigate  "),
                    Span::styled("[Enter] ", Style::default().fg(Color::Green)),
                    Span::raw("Open in Pricing  "),
                    Span::styled("[c] ", Style::default().fg(Color::Red)),
                    Span::raw("clear all  "),
                    Span::styled("[↑] ", Style::default().fg(Color::Yellow)),
                    Span::raw("Go to Header  "),
                    Span::styled("[Esc] ", Style::default().fg(Color::Red)),
                    Span::raw("Quit"),
                ])
            }
        };

        let help = Paragraph::new(help_text);
        f.render_widget(help, chunks[2]);
    }
}
