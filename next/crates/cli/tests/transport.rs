use core_kernel::{action_specs, execute};
use core_types::{CredentialContext, Outcome, Refusal, Request, PROTOCOL_VERSION};
use serde_json::{json, Value};
use std::cell::Cell;
use std::ffi::OsStr;
use std::io::{self, Read, Write};
use std::process::{Command, Output, Stdio};

fn child(input: &[u8], args: &[&OsStr]) -> Output {
    let mut process = Command::new(env!("CARGO_BIN_EXE_dacli-next"))
        .args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn actual CLI");
    process
        .stdin
        .take()
        .unwrap()
        .write_all(input)
        .expect("write request");
    process.wait_with_output().expect("observe CLI exit")
}
fn public_request() -> Value {
    json!({"protocol_version": 1, "action": "InspectProtocol", "effect": "read_only", "context": {"kind": "public_protocol"}})
}
fn assert_policy_parity(wire: Value, api: Request<'_>, expected: Refusal) {
    let result = execute(api);
    assert_eq!(result, Outcome::Refused(expected));
    let output = child(&serde_json::to_vec(&wire).unwrap(), &[]);
    assert_eq!(output.status.code(), Some(3));
    assert!(output.stderr.is_empty());
    assert_eq!(
        serde_json::from_slice::<Value>(&output.stdout).unwrap(),
        cli::outcome_json(&result)
    );
}
fn inspection(context: CredentialContext<'_>) -> Request<'_> {
    Request {
        protocol_version: PROTOCOL_VERSION,
        action: "InspectProtocol",
        effect: "read_only",
        context,
    }
}

#[test]
fn actual_cli_and_in_process_api_agree_on_each_policy_refusal() {
    let mut wire = public_request();
    wire["action"] = json!("DeleteEverything");
    wire["effect"] = json!("mutation");
    assert_policy_parity(
        wire,
        Request {
            action: "DeleteEverything",
            effect: "mutation",
            ..inspection(CredentialContext::PublicProtocol)
        },
        Refusal::UnregisteredAction,
    );
    let mut wire = public_request();
    wire["effect"] = json!("network");
    assert_policy_parity(
        wire,
        Request {
            effect: "network",
            ..inspection(CredentialContext::PublicProtocol)
        },
        Refusal::UnknownEffect,
    );
    let mut wire = public_request();
    wire["effect"] = json!("mutation");
    assert_policy_parity(
        wire,
        Request {
            effect: "mutation",
            ..inspection(CredentialContext::PublicProtocol)
        },
        Refusal::EffectMismatch,
    );
    let mut wire = public_request();
    wire["context"] = json!({"kind":"credential", "token":""});
    assert_policy_parity(
        wire,
        inspection(CredentialContext::Credential("")),
        Refusal::EmptyCredential,
    );
    let mut wire = public_request();
    wire["context"] = json!({"kind":"credential", "token":" \t\n"});
    assert_policy_parity(
        wire,
        inspection(CredentialContext::Credential(" \t\n")),
        Refusal::EmptyCredential,
    );
    let mut wire = public_request();
    wire.as_object_mut().unwrap().remove("context");
    assert_policy_parity(
        wire,
        inspection(CredentialContext::Missing),
        Refusal::EmptyCredential,
    );
    let mut wire = public_request();
    wire["context"] = json!({"kind":"credential"});
    assert_policy_parity(
        wire,
        inspection(CredentialContext::Missing),
        Refusal::EmptyCredential,
    );
    let mut wire = public_request();
    wire["context"] = json!({"kind":"credential", "token":"UNVERIFIED-CANARY"});
    assert_policy_parity(
        wire,
        inspection(CredentialContext::Credential("UNVERIFIED-CANARY")),
        Refusal::CredentialVerificationUnavailable,
    );
    let mut wire = public_request();
    wire["action"] = json!("CreateTask");
    wire["effect"] = json!("mutation");
    assert_policy_parity(
        wire,
        Request {
            action: "CreateTask",
            effect: "mutation",
            ..inspection(CredentialContext::PublicProtocol)
        },
        Refusal::PublicContextNotAllowed,
    );
    let mut wire = public_request();
    wire["protocol_version"] = json!(2);
    assert_policy_parity(
        wire,
        Request {
            protocol_version: 2,
            ..inspection(CredentialContext::PublicProtocol)
        },
        Refusal::UnsupportedProtocol,
    );
}

#[test]
fn successful_inspection_and_help_are_generated_from_the_executed_catalog() {
    let output = child(&serde_json::to_vec(&public_request()).unwrap(), &[]);
    assert_eq!(output.status.code(), Some(0));
    assert!(output.stderr.is_empty());
    let expected = execute(inspection(CredentialContext::PublicProtocol));
    let value: Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(value, cli::outcome_json(&expected));
    let help = child(b"ignored", &[OsStr::new("--help")]);
    assert_eq!(help.status.code(), Some(0));
    assert!(help.stderr.is_empty());
    let text = String::from_utf8(help.stdout).unwrap();
    for spec in action_specs() {
        assert!(text.contains(&format!(
            "{} [{}; {}; {}] {}",
            spec.name,
            spec.effect.name(),
            spec.authentication.name(),
            spec.availability.name(),
            spec.summary
        )));
        let rows = value["protocol"]["actions"].as_array().unwrap();
        assert_eq!(
            rows.iter().filter(|row| row["name"] == spec.name).count(),
            1
        );
        let row = rows.iter().find(|row| row["name"] == spec.name).unwrap();
        assert_eq!(row["effect"], spec.effect.name());
        assert_eq!(
            row["required_capability"],
            json!(spec.authentication.capability())
        );
    }
}

#[test]
fn malformed_or_credential_shaped_input_is_usage_without_echo() {
    let cases: Vec<Vec<u8>> = vec![
        b"".to_vec(), b"\xff".to_vec(), b"{\"CANARY\": [".to_vec(),
        br#"{"protocol_version":1,"action":"InspectProtocol","action":"CreateTask","effect":"read_only","context":{"kind":"public_protocol"}}"#.to_vec(),
        br#"{"protocol_version":1,"action":"InspectProtocol","effect":"read_only","context":{"kind":"public_protocol","token":"CANARY"}}"#.to_vec(),
        br#"{"protocol_version":1,"action":"InspectProtocol","effect":"read_only","context":{"kind":"public_protocol","token":null}}"#.to_vec(),
        br#"{"protocol_version":1,"action":"InspectProtocol","effect":"read_only","context":{"kind":"public_protocol","extra":"CANARY"}}"#.to_vec(),
        br#"{"protocol_version":1,"action":"InspectProtocol","effect":"read_only","context":{"kind":"public_protocol"},"capability":"owner-CANARY"}"#.to_vec(),
        br#"{"protocol_version":1,"action":"InspectProtocol","effect":"read_only","context":{"kind":"credential","token":123}}"#.to_vec(),
        [serde_json::to_vec(&public_request()).unwrap(), b"\n{}".to_vec()].concat(),
    ];
    for bytes in cases {
        let output = child(&bytes, &[]);
        assert_eq!(output.status.code(), Some(2));
        assert!(output.stderr.is_empty());
        let value: Value = serde_json::from_slice(&output.stdout).unwrap();
        assert_eq!(value, cli::usage_json(cli::UsageFailure::InvalidRequest));
        assert!(!String::from_utf8_lossy(&output.stdout).contains("CANARY"));
    }
}

#[test]
fn credential_bytes_and_unsupported_arguments_never_enter_diagnostics() {
    let mut wire = public_request();
    wire["context"] = json!({"kind":"credential", "token":"SENSITIVE-CANARY"});
    let output = child(&serde_json::to_vec(&wire).unwrap(), &[]);
    assert_eq!(output.status.code(), Some(3));
    assert!(output.stderr.is_empty());
    assert!(!String::from_utf8_lossy(&output.stdout).contains("SENSITIVE-CANARY"));
    let output = child(b"", &[OsStr::new("--credential=SENSITIVE-CANARY")]);
    assert_eq!(output.status.code(), Some(2));
    assert!(output.stderr.is_empty());
    assert_eq!(
        serde_json::from_slice::<Value>(&output.stdout).unwrap(),
        cli::usage_json(cli::UsageFailure::UnsupportedArguments)
    );
    assert!(!String::from_utf8_lossy(&output.stdout).contains("SENSITIVE-CANARY"));
}

struct CountingReader<'a>(&'a Cell<usize>);
impl Read for CountingReader<'_> {
    fn read(&mut self, target: &mut [u8]) -> io::Result<usize> {
        target.fill(b' ');
        self.0.set(self.0.get() + target.len());
        Ok(target.len())
    }
}
#[test]
fn stdin_is_bounded_before_json_composition() {
    let read = Cell::new(0);
    let mut bytes = Vec::new();
    assert_eq!(cli::run(&[], CountingReader(&read), &mut bytes), 2);
    assert_eq!(read.get(), cli::MAXIMUM_REQUEST_BYTES + 1);
    assert_eq!(
        serde_json::from_slice::<Value>(&bytes).unwrap(),
        cli::usage_json(cli::UsageFailure::RequestTooLarge)
    );
    let mut oversized = serde_json::to_vec(&public_request()).unwrap();
    oversized.extend(vec![b' '; cli::MAXIMUM_REQUEST_BYTES]);
    assert_eq!(
        cli::dispatch_json(&oversized),
        Err(cli::UsageFailure::RequestTooLarge)
    );
}
struct FailedRead;
impl Read for FailedRead {
    fn read(&mut self, _: &mut [u8]) -> io::Result<usize> {
        Err(io::Error::other("PRIVATE-CANARY"))
    }
}
struct FailedWrite;
impl Write for FailedWrite {
    fn write(&mut self, _: &[u8]) -> io::Result<usize> {
        Err(io::Error::other("PRIVATE-CANARY"))
    }
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}
#[test]
fn transport_failures_never_report_success_or_private_diagnostics() {
    let mut bytes = Vec::new();
    assert_eq!(cli::run(&[], FailedRead, &mut bytes), 1);
    assert!(!String::from_utf8_lossy(&bytes).contains("PRIVATE-CANARY"));
    assert_eq!(
        cli::run(
            &[],
            serde_json::to_vec(&public_request()).unwrap().as_slice(),
            FailedWrite
        ),
        1
    );
}
