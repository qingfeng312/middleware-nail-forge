use anyhow::Result;
use serde::Serialize;
use std::time::Instant;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;

use crate::{BUILD_PROFILE, VERSION};

const HEALTH_FEATURES: &[&str] = &[
    "service_registry",
    "service_discovery",
    "message_broker",
    "connector_bridge",
];

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct HealthResponse {
    pub status: &'static str,
    pub version: &'static str,
    pub commit: String,
    pub uptime_seconds: u64,
    pub features: Vec<String>,
}

pub fn normalize_commit(value: Option<String>) -> String {
    value
        .map(|commit| commit.trim().to_string())
        .filter(|commit| !commit.is_empty())
        .unwrap_or_else(|| "unknown".to_string())
}

pub fn current_commit() -> String {
    normalize_commit(std::env::var("GIT_COMMIT").ok())
}

pub fn enabled_features() -> Vec<String> {
    let mut features: Vec<String> = HEALTH_FEATURES
        .iter()
        .map(|feature| (*feature).to_string())
        .collect();
    features.push(format!("build_profile:{BUILD_PROFILE}"));
    features
}

pub fn health_response(started_at: Instant) -> HealthResponse {
    HealthResponse {
        status: "ok",
        version: VERSION,
        commit: current_commit(),
        uptime_seconds: started_at.elapsed().as_secs(),
        features: enabled_features(),
    }
}

pub fn render_health_json(started_at: Instant) -> Result<String> {
    Ok(serde_json::to_string(&health_response(started_at))?)
}

pub async fn serve_health(listener: TcpListener, started_at: Instant) -> Result<()> {
    loop {
        let (mut socket, _) = listener.accept().await?;
        tokio::spawn(async move {
            let mut buffer = [0_u8; 1024];
            let read_len = match socket.read(&mut buffer).await {
                Ok(len) => len,
                Err(_) => return,
            };

            let request = std::str::from_utf8(&buffer[..read_len]).unwrap_or("");
            let path = request
                .split_whitespace()
                .nth(1)
                .and_then(|path| path.split('?').next())
                .unwrap_or("/");

            let (status_line, body) = if path == "/health" {
                match render_health_json(started_at) {
                    Ok(body) => ("HTTP/1.1 200 OK", body),
                    Err(_) => (
                        "HTTP/1.1 500 Internal Server Error",
                        r#"{"status":"error"}"#.to_string(),
                    ),
                }
            } else {
                ("HTTP/1.1 404 Not Found", r#"{"error":"not_found"}"#.to_string())
            };

            let response = format!(
                "{status_line}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            );
            let _ = socket.write_all(response.as_bytes()).await;
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    #[test]
    fn normalizes_git_commit_from_env_value() {
        assert_eq!(normalize_commit(Some("abc123\n".to_string())), "abc123");
        assert_eq!(normalize_commit(Some("   ".to_string())), "unknown");
        assert_eq!(normalize_commit(None), "unknown");
    }

    #[test]
    fn health_response_contains_required_fields() {
        let started_at = Instant::now() - Duration::from_secs(42);
        let response = health_response(started_at);

        assert_eq!(response.status, "ok");
        assert_eq!(response.version, "0.1.0");
        assert!(response.uptime_seconds >= 42);
        assert!(response
            .features
            .iter()
            .any(|feature| feature == "service_registry"));
        assert!(response
            .features
            .iter()
            .any(|feature| feature == "message_broker"));
    }

    #[test]
    fn health_json_has_stable_shape() {
        let value: serde_json::Value =
            serde_json::from_str(&render_health_json(Instant::now()).unwrap()).unwrap();

        assert_eq!(value["status"], "ok");
        assert!(value["version"].is_string());
        assert!(value["commit"].is_string());
        assert!(value["uptime_seconds"].is_u64());
        assert!(value["features"]
            .as_array()
            .is_some_and(|features| !features.is_empty()));
    }
}
