use anyhow::{bail, Result};
use clap::Parser;
use tent_backend::discovery::ServiceDiscovery;
use tent_backend::messaging::MessageBroker;
use tent_backend::registry::ServiceRegistry;
use tracing_subscriber::EnvFilter;

#[derive(Parser, Debug)]
#[command(name = "tent-backend")]
#[command(about = "Tent of Trials Backend - Distributed Microservices Framework", long_about = None)]
struct Cli {

    #[arg(short, long, default_value = "node-0")]
    node_id: String,

    #[arg(short, long)]
    consensus: bool,

    #[arg(long, default_value_t = 10000)]
    max_connections: u32,

    #[arg(short, long, default_value = "/etc/tent/config.toml")]
    config: String,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum LogFormat {
    Text,
    Json,
}

impl LogFormat {
    fn from_env_value(value: Option<&str>) -> Result<Self> {
        match value.map(str::trim).filter(|value| !value.is_empty()) {
            None => Ok(Self::Text),
            Some("text") => Ok(Self::Text),
            Some("json") => Ok(Self::Json),
            Some(value) => bail!(
                "invalid TOT_LOG_FORMAT value '{value}'; expected 'text' or 'json'"
            ),
        }
    }

    fn from_env() -> Result<Self> {
        let value = std::env::var("TOT_LOG_FORMAT").ok();
        Self::from_env_value(value.as_deref())
    }

    fn as_str(self) -> &'static str {
        match self {
            Self::Text => "text",
            Self::Json => "json",
        }
    }
}

fn env_filter() -> EnvFilter {
    EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into())
}

fn init_tracing(log_format: LogFormat) {
    match log_format {
        LogFormat::Text => tracing_subscriber::fmt()
            .with_env_filter(env_filter())
            .init(),
        LogFormat::Json => tracing_subscriber::fmt()
            .with_env_filter(env_filter())
            .json()
            .init(),
    }
}

#[tokio::main]
// What the fuck is this main function even doing anymore.
// It's 30 lines of config loading and then it spawns a server.
// Actually it's like 50 lines. Still too fucking many.
async fn main() -> Result<()> {
    let log_format = LogFormat::from_env()?;
    init_tracing(log_format);

    let cli = Cli::parse();

    tracing::info!(
        log_format = log_format.as_str(),
        node_id = %cli.node_id,
        consensus = %cli.consensus,
        max_connections = %cli.max_connections,
        config = %cli.config,
        "initializing tent backend orchestration framework"
    );

    let config = tent_backend::config::load_config(&cli.config).await?;
    let registry = ServiceRegistry::new(config.registry.clone());
    let discovery = ServiceDiscovery::new(config.discovery.clone());
    let broker = MessageBroker::new(config.messaging.clone());

    registry.initialize().await?;
    discovery.announce(&cli.node_id).await?;
    broker.connect().await?;

    tracing::info!("all subsystems initialized successfully, entering main loop");

    let mut signal = tokio::signal::unix::signal(
        tokio::signal::unix::SignalKind::terminate(),
    )?;

    tokio::select! {
        _ = signal.recv() => {
            tracing::info!("received SIGTERM, initiating graceful shutdown");
        }
        _ = tokio::signal::ctrl_c() => {
            tracing::info!("received SIGINT, initiating graceful shutdown");
        }
    }

    broker.disconnect().await?;
    discovery.withdraw(&cli.node_id).await?;
    registry.shutdown().await?;

    tracing::info!("shutdown complete");
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::LogFormat;

    #[test]
    fn default_log_format_is_text() {
        assert_eq!(LogFormat::from_env_value(None).unwrap(), LogFormat::Text);
        assert_eq!(LogFormat::from_env_value(Some("")).unwrap(), LogFormat::Text);
        assert_eq!(LogFormat::from_env_value(Some("   ")).unwrap(), LogFormat::Text);
    }

    #[test]
    fn accepts_supported_log_formats() {
        assert_eq!(LogFormat::from_env_value(Some("text")).unwrap(), LogFormat::Text);
        assert_eq!(LogFormat::from_env_value(Some("json")).unwrap(), LogFormat::Json);
        assert_eq!(LogFormat::Text.as_str(), "text");
        assert_eq!(LogFormat::Json.as_str(), "json");
    }

    #[test]
    fn rejects_invalid_log_format_values() {
        let error = LogFormat::from_env_value(Some("xml")).unwrap_err();

        assert!(
            error
                .to_string()
                .contains("invalid TOT_LOG_FORMAT value 'xml'")
        );
    }
}
