use anyhow::Result;
use crossterm::event::KeyEvent;
use ratatui::{
    layout::{Alignment, Constraint, Direction, Layout},
    style::{Color, Modifier, Style},
    text::{Line, Span},
    widgets::{Block, BorderType, Borders, Paragraph},
    Frame,
};

use crate::api::Config;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SettingsSection {
    Header,
    Content,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SettingsEvent {
    None,
    Quit,
    PrevView,
    NextView,
}

pub struct SettingsView {
    pub active_section: SettingsSection,
}

impl SettingsView {
    pub fn new() -> Self {
        Self {
            active_section: SettingsSection::Content,
        }
    }

    pub fn handle_key(&mut self, key: KeyEvent) -> Result<SettingsEvent> {
        use crossterm::event::KeyCode;
        match key.code {
            KeyCode::Up => {
                self.active_section = match self.active_section {
                    SettingsSection::Content => SettingsSection::Header,
                    SettingsSection::Header => SettingsSection::Header,
                };
                Ok(SettingsEvent::None)
            }
            KeyCode::Down => {
                self.active_section = match self.active_section {
                    SettingsSection::Header => SettingsSection::Content,
                    SettingsSection::Content => SettingsSection::Content,
                };
                Ok(SettingsEvent::None)
            }
            KeyCode::Left if self.active_section == SettingsSection::Header => {
                Ok(SettingsEvent::PrevView)
            }
            KeyCode::Right if self.active_section == SettingsSection::Header => {
                Ok(SettingsEvent::NextView)
            }
            KeyCode::Esc => Ok(SettingsEvent::Quit),
            _ => Ok(SettingsEvent::None),
        }
    }

    pub fn render(&self, f: &mut Frame, config: Option<&Config>, config_path: &str, active: bool) {
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
        let header_section_active = active && self.active_section == SettingsSection::Header;
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
        let settings_style = if header_section_active {
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
        nav_spans.push(Span::styled("History", Style::default().fg(Color::DarkGray)));
        nav_spans.push(Span::raw(" | "));
        nav_spans.push(Span::styled(
            if header_section_active { " > Settings < " } else { " Settings " },
            settings_style,
        ));
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
        let content_text = if let Some(cfg) = config {
            let masked_key = cfg
                .api_key
                .as_ref()
                .map(|k| {
                    let chars: Vec<char> = k.chars().collect();
                    if chars.len() > 12 {
                        format!("{}...{}", 
                            chars.iter().take(8).collect::<String>(),
                            chars.iter().skip(chars.len() - 4).collect::<String>()
                        )
                    } else {
                        "****".to_string()
                    }
                })
                .unwrap_or_else(|| "Not set".to_string());

            vec![
                Line::from(""),
                Line::from(Span::styled(
                    "Configuration",
                    Style::default()
                        .fg(Color::Yellow)
                        .add_modifier(Modifier::BOLD),
                )),
                Line::from(""),
                Line::from(vec![
                    Span::styled("CLI ID:        ", Style::default().fg(Color::Cyan)),
                    Span::styled(&cfg.cli_id, Style::default().fg(Color::White)),
                ]),
                Line::from(""),
                Line::from(vec![
                    Span::styled("API Key:       ", Style::default().fg(Color::Cyan)),
                    Span::styled(masked_key, Style::default().fg(Color::White)),
                ]),
                Line::from(""),
                Line::from(vec![
                    Span::styled("Config Path:   ", Style::default().fg(Color::Cyan)),
                    Span::styled(config_path, Style::default().fg(Color::White)),
                ]),
                Line::from(""),
                Line::from(""),
                Line::from(Span::styled(
                    "Status: Authenticated",
                    Style::default().fg(Color::Green),
                )),
            ]
        } else {
            vec![
                Line::from(""),
                Line::from(Span::styled(
                    "Configuration",
                    Style::default()
                        .fg(Color::Yellow)
                        .add_modifier(Modifier::BOLD),
                )),
                Line::from(""),
                Line::from(Span::styled(
                    "Status: Not authenticated",
                    Style::default().fg(Color::Red),
                )),
                Line::from(""),
                Line::from(Span::styled(
                    "Please restart and authenticate to use CloudCent CLI.",
                    Style::default().fg(Color::DarkGray),
                )),
            ]
        };

        // Content Focus Logic
        let content_section_active = active && self.active_section == SettingsSection::Content;
        let content_border_type = if content_section_active { BorderType::Thick } else { BorderType::Plain };
        let content_border_color = if content_section_active { Color::Green } else { Color::DarkGray };
        let content_title_style = if content_section_active { Style::default().fg(Color::Green).add_modifier(Modifier::BOLD) } else { Style::default().fg(Color::DarkGray) };

        let content = Paragraph::new(content_text)
            .block(
                Block::default()
                    .borders(Borders::ALL)
                    .border_type(content_border_type)
                    .title(Line::from(vec![
                        Span::styled(if content_section_active { " > " } else { " " }, content_title_style),
                        Span::styled(" Settings ", content_title_style),
                    ]))
                    .border_style(Style::default().fg(content_border_color)),
            )
            .alignment(Alignment::Left);

        f.render_widget(content, chunks[1]);

        // Help bar
        let help_text = match self.active_section {
            SettingsSection::Header => {
                Line::from(vec![
                    Span::styled("[←→] ", Style::default().fg(Color::Cyan)),
                    Span::raw("Switch View  "),
                    Span::styled("[↓] ", Style::default().fg(Color::Yellow)),
                    Span::raw("Go to Content  "),
                    Span::styled("[Esc] ", Style::default().fg(Color::Red)),
                    Span::raw("Quit"),
                ])
            }
            SettingsSection::Content => {
                Line::from(vec![
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
