//! Pure command descriptions and outcomes for the first greenfield slice (#1256).
//! No credential string, source record or host label is an authenticated grant.
#![no_std]
#![forbid(unsafe_code)]

extern crate alloc;
use alloc::vec::Vec;
use core::fmt;

pub const PROTOCOL_VERSION: u16 = 1;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Effect {
    ReadOnly,
    Mutation,
}
impl Effect {
    pub fn decode(value: &str) -> Option<Self> {
        match value {
            "read_only" => Some(Self::ReadOnly),
            "mutation" => Some(Self::Mutation),
            _ => None,
        }
    }
    pub const fn name(self) -> &'static str {
        match self {
            Self::ReadOnly => "read_only",
            Self::Mutation => "mutation",
        }
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Authentication {
    /// Static protocol discovery only; never workspace or authority data.
    PublicProtocol,
    VerifiedCapability(&'static str),
}
impl Authentication {
    pub const fn name(self) -> &'static str {
        match self {
            Self::PublicProtocol => "public_protocol",
            Self::VerifiedCapability(_) => "verified_capability",
        }
    }
    pub const fn capability(self) -> Option<&'static str> {
        match self {
            Self::PublicProtocol => None,
            Self::VerifiedCapability(capability) => Some(capability),
        }
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Availability {
    Available,
    Unavailable,
}
impl Availability {
    pub const fn name(self) -> &'static str {
        match self {
            Self::Available => "available",
            Self::Unavailable => "unavailable",
        }
    }
}

/// One catalog describes decoding, effect classification, help and discovery.
/// Stateful expected-version, preview, idempotency and recovery rules are absent
/// rather than invented for the unavailable task writer in this first slice.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct ActionSpec {
    pub name: &'static str,
    pub summary: &'static str,
    pub effect: Effect,
    pub authentication: Authentication,
    pub availability: Availability,
    pub request_schema_version: u16,
}

/// Explicit public discovery is distinct from a missing or empty credential.
/// Token bytes are borrowed and redacted even in diagnostic formatting.
#[derive(Clone, Copy)]
pub enum CredentialContext<'a> {
    Missing,
    PublicProtocol,
    Credential(&'a str),
}
impl fmt::Debug for CredentialContext<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Missing => f.write_str("Missing"),
            Self::PublicProtocol => f.write_str("PublicProtocol"),
            Self::Credential(_) => f.write_str("Credential([REDACTED])"),
        }
    }
}

#[derive(Clone, Copy, Debug)]
pub struct Request<'a> {
    pub protocol_version: u16,
    pub action: &'a str,
    /// An untrusted declaration; the catalog's effect is authoritative.
    pub effect: &'a str,
    pub context: CredentialContext<'a>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Refusal {
    UnsupportedProtocol,
    UnregisteredAction,
    UnknownEffect,
    EffectMismatch,
    EmptyCredential,
    CredentialVerificationUnavailable,
    PublicContextNotAllowed,
    ActionUnavailable,
}
impl Refusal {
    pub const fn code(self) -> &'static str {
        match self {
            Self::UnsupportedProtocol => "unsupported_protocol",
            Self::UnregisteredAction => "unregistered_action",
            Self::UnknownEffect => "unknown_effect",
            Self::EffectMismatch => "effect_mismatch",
            Self::EmptyCredential => "empty_credential",
            Self::CredentialVerificationUnavailable => "credential_verification_unavailable",
            Self::PublicContextNotAllowed => "public_context_not_allowed",
            Self::ActionUnavailable => "action_unavailable",
        }
    }
    pub const fn message(self) -> &'static str {
        match self {
            Self::UnsupportedProtocol => "This protocol version is unsupported.",
            Self::UnregisteredAction => "This action is not registered.",
            Self::UnknownEffect => "This effect classification is unknown.",
            Self::EffectMismatch => "The declared effect does not match the registered action.",
            Self::EmptyCredential => {
                "A missing or empty credential is not public discovery or authority."
            }
            Self::CredentialVerificationUnavailable => {
                "Credential verification is unavailable in this slice."
            }
            Self::PublicContextNotAllowed => {
                "Public protocol discovery cannot authorize this action."
            }
            Self::ActionUnavailable => "This registered action is unavailable in this slice.",
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ProtocolDescription {
    pub protocol_version: u16,
    pub actions: Vec<ActionSpec>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum Outcome {
    Protocol(ProtocolDescription),
    Refused(Refusal),
}
impl Outcome {
    /// Preserve the legacy distinction between policy refusal and failure.
    pub const fn exit_code(&self) -> u8 {
        match self {
            Self::Protocol(_) => 0,
            Self::Refused(_) => 3,
        }
    }
}
