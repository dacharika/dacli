//! Bounded JSON transport for the pure kernel. Parser diagnostics, raw requests
//! and credential bytes are never copied into output or error messages.
#![forbid(unsafe_code)]

use core_types::{ActionSpec, CredentialContext, Outcome, Request, PROTOCOL_VERSION};
use serde::Deserialize;
use serde_json::{json, Value};
use std::io::{self, Read, Write};

pub const MAXIMUM_REQUEST_BYTES: usize = 64 * 1024;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct WireRequest {
    protocol_version: u16,
    action: String,
    effect: String,
    #[serde(default)]
    context: Option<WireContext>,
}
#[derive(Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
enum WireContext {
    // An empty struct is intentional: Serde's tagged unit variant accepted
    // extra token fields despite deny_unknown_fields (actual #1256 red test).
    PublicProtocol {},
    Credential { token: Option<String> },
}
impl WireRequest {
    fn request(&self) -> Request<'_> {
        let context = match self.context.as_ref() {
            Some(WireContext::PublicProtocol {}) => CredentialContext::PublicProtocol,
            Some(WireContext::Credential { token: Some(token) }) => {
                CredentialContext::Credential(token)
            }
            Some(WireContext::Credential { token: None }) | None => CredentialContext::Missing,
        };
        Request {
            protocol_version: self.protocol_version,
            action: &self.action,
            effect: &self.effect,
            context,
        }
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum UsageFailure {
    InvalidRequest,
    RequestTooLarge,
    UnsupportedArguments,
}
impl UsageFailure {
    pub const fn code(self) -> &'static str {
        match self {
            Self::InvalidRequest => "invalid_request",
            Self::RequestTooLarge => "request_too_large",
            Self::UnsupportedArguments => "unsupported_arguments",
        }
    }
}

/// Parses one complete document. Unknown/duplicate fields, trailing documents,
/// invalid UTF-8 and credential-shaped extras are usage failures, not grants.
pub fn dispatch_json(bytes: &[u8]) -> Result<Outcome, UsageFailure> {
    if bytes.len() > MAXIMUM_REQUEST_BYTES {
        return Err(UsageFailure::RequestTooLarge);
    }
    let request: WireRequest =
        serde_json::from_slice(bytes).map_err(|_| UsageFailure::InvalidRequest)?;
    Ok(core_kernel::execute(request.request()))
}

pub fn outcome_json(outcome: &Outcome) -> Value {
    match outcome {
        Outcome::Refused(refusal) => json!({
            "schema_version": PROTOCOL_VERSION,
            "status": "refused",
            "refusal": {"code": refusal.code(), "message": refusal.message()},
        }),
        Outcome::Protocol(protocol) => json!({
            "schema_version": PROTOCOL_VERSION,
            "status": "ok",
            "protocol": {
                "version": protocol.protocol_version,
                "actions": protocol.actions.iter().map(spec_json).collect::<Vec<_>>(),
            },
        }),
    }
}

fn spec_json(spec: &ActionSpec) -> Value {
    json!({
        "name": spec.name,
        "summary": spec.summary,
        "effect": spec.effect.name(),
        "authentication": spec.authentication.name(),
        "required_capability": spec.authentication.capability(),
        "availability": spec.availability.name(),
        "request_schema_version": spec.request_schema_version,
    })
}

pub fn usage_json(failure: UsageFailure) -> Value {
    json!({"schema_version": PROTOCOL_VERSION, "status": "usage", "error": {"code": failure.code()}})
}
fn failure_json() -> Value {
    json!({"schema_version": PROTOCOL_VERSION, "status": "failure", "error": {"code": "transport_failure"}})
}

/// Help comes from the same ActionSpec registration table as execution and JSON.
pub fn help_text() -> String {
    let mut text = String::from(
        "dacli-next\n\nRead one bounded protocol JSON request from stdin.\n\nActions:\n",
    );
    for spec in core_kernel::action_specs() {
        text.push_str(&format!(
            "  {} [{}; {}; {}] {}\n",
            spec.name,
            spec.effect.name(),
            spec.authentication.name(),
            spec.availability.name(),
            spec.summary
        ));
    }
    text.push_str(
        "\n--help prints this catalog-derived text. Credentials are never accepted in arguments.\n",
    );
    text
}

/// Only this transport owns stdin/stdout. The kernel cannot open a file, spawn
/// a process or perform task state changes, including when a gate is refused.
pub fn run<R: Read, W: Write>(args: &[String], input: R, mut output: W) -> u8 {
    if args == ["--help"] {
        return if output.write_all(help_text().as_bytes()).is_ok() {
            0
        } else {
            1
        };
    }
    if !args.is_empty() {
        return emit(
            &mut output,
            &usage_json(UsageFailure::UnsupportedArguments),
            2,
        );
    }
    let mut bytes = Vec::new();
    if input
        .take((MAXIMUM_REQUEST_BYTES + 1) as u64)
        .read_to_end(&mut bytes)
        .is_err()
    {
        return emit(&mut output, &failure_json(), 1);
    }
    match dispatch_json(&bytes) {
        Ok(outcome) => emit(&mut output, &outcome_json(&outcome), outcome.exit_code()),
        Err(failure) => emit(&mut output, &usage_json(failure), 2),
    }
}

fn emit(output: &mut impl Write, value: &Value, exit_code: u8) -> u8 {
    if serde_json::to_writer(&mut *output, value).is_err() || output.write_all(b"\n").is_err() {
        return 1;
    }
    exit_code
}

pub fn main_entry() -> u8 {
    // Skip only argv[0]. Never format an unsupported argument into diagnostics.
    let args: Result<Vec<String>, _> = std::env::args_os()
        .skip(1)
        .map(|value| value.into_string())
        .collect();
    match args {
        Ok(args) => run(&args, io::stdin().lock(), io::stdout().lock()),
        Err(_) => emit(
            &mut io::stdout().lock(),
            &usage_json(UsageFailure::UnsupportedArguments),
            2,
        ),
    }
}
