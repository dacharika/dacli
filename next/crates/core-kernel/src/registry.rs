//! One registration source for both protocol views (#1258).
//! V2 metadata describes syntax only: it is not a verifier or available gateway.
use crate::Implementation;
use core_types::{ActionSpec, Authentication, Availability, Effect};

pub const SCOPED_PROTOCOL_VERSION: u16 = 2;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct ScopedActionSpec {
    pub id: ActionId,
    pub name: &'static str,
    pub summary: &'static str,
    pub payload: PayloadKind,
    pub effect: Effect,
    pub required_permission: Option<ActionId>,
    pub capability: Option<&'static str>,
    pub availability: Availability,
    pub request_schema_version: u16,
}

pub(crate) struct RegisteredAction {
    pub scoped: ScopedActionSpec,
    pub legacy: Option<ActionSpec>,
    pub implementation: Implementation,
}

macro_rules! action_registry {
    ($( $id:ident, $name:literal, $payload:literal, $effect:ident, $cap:expr,
        $summary:literal, $legacy:expr, $legacy_availability:ident, $implementation:expr; )+) => {
        /// Variant discriminants are internal registry indexes, never wire or
        /// storage IDs. Canonical encoding uses exact stable action names.
        #[derive(Clone, Copy, Debug, Eq, PartialEq)]
        pub enum ActionId { $( $id, )+ }
        #[derive(Clone, Copy, Debug, Eq, PartialEq)]
        pub enum PayloadKind { $( $id, )+ }
        impl ActionId {
            pub fn decode(name: &str) -> Option<Self> {
                match name { $( $name => Some(Self::$id), )+ _ => None }
            }
            pub fn spec(self) -> &'static ScopedActionSpec {
                &REGISTRY[self as usize].scoped
            }
            pub fn name(self) -> &'static str { self.spec().name }
        }
        impl PayloadKind {
            pub fn decode(name: &str) -> Option<Self> {
                match name { $( $payload => Some(Self::$id), )+ _ => None }
            }
            pub fn action(self) -> ActionId {
                match self { $( Self::$id => ActionId::$id, )+ }
            }
            pub const fn name(self) -> &'static str {
                match self { $( Self::$id => $payload, )+ }
            }
        }
        pub(crate) static REGISTRY: &[RegisteredAction] = &[
            $( RegisteredAction {
                scoped: ScopedActionSpec {
                    id: ActionId::$id, name: $name, summary: $summary, payload: PayloadKind::$id,
                    effect: Effect::$effect,
                    required_permission: if ($cap).is_some() { Some(ActionId::$id) } else { None },
                    capability: $cap,
                    availability: Availability::Unavailable,
                    request_schema_version: SCOPED_PROTOCOL_VERSION,
                },
                legacy: if $legacy { Some(ActionSpec {
                    name: $name, summary: $summary, effect: Effect::$effect,
                    authentication: match $cap {
                        Some(capability) => Authentication::VerifiedCapability(capability),
                        None => Authentication::PublicProtocol,
                    },
                    availability: Availability::$legacy_availability,
                    request_schema_version: core_types::PROTOCOL_VERSION,
                }) } else { None },
                implementation: $implementation,
            }, )+
        ];
    };
}

action_registry! {
    InspectProtocol, "InspectProtocol", "inspect_protocol", ReadOnly, None::<&'static str>,
    "Inspect this static protocol and action catalog.", true, Available,
    Implementation::Query(crate::inspect_protocol);
    CreateTask, "CreateTask", "create_task", Mutation, Some("tasks.create"),
    "Unavailable: production task transport and scoped authentication are not implemented.", true, Unavailable, Implementation::Unavailable;
    ReadTask, "ReadTask", "read_task", ReadOnly, Some("tasks.read"),
    "Read a task in an authenticated exact scope.", false, Unavailable, Implementation::Unavailable;
    QueryTasks, "QueryTasks", "query_tasks", ReadOnly, Some("tasks.query"),
    "Query tasks in an authenticated exact scope.", false, Unavailable, Implementation::Unavailable;
    ObserveCreateTask, "ObserveCreateTask", "observe_create_task", ReadOnly, Some("tasks.observe_create"),
    "Observe the same actor's original create operation without executing it.", false, Unavailable, Implementation::Unavailable;
    DelegateSession, "DelegateSession", "delegate_session", Mutation, Some("sessions.delegate"),
    "Issue a narrowing child session in a new fixture authority.", false, Unavailable, Implementation::Unavailable;
    RevokeSession, "RevokeSession", "revoke_session", Mutation, Some("sessions.revoke"),
    "Revoke a session under current scoped authority.", false, Unavailable, Implementation::Unavailable;
}

pub fn scoped_action_specs() -> impl ExactSizeIterator<Item = &'static ScopedActionSpec> {
    REGISTRY.iter().map(|registered| &registered.scoped)
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum RegistryError {
    UnregisteredAction,
    UnknownEffect,
    EffectMismatch,
    UnknownPayload,
    PayloadMismatch,
}

/// Syntax resolution is deliberately not authentication or permission admission.
pub fn resolve_scoped(
    action: &str,
    effect: &str,
    payload: &str,
) -> Result<&'static ScopedActionSpec, RegistryError> {
    let id = ActionId::decode(action).ok_or(RegistryError::UnregisteredAction)?;
    let spec = id.spec();
    let effect = Effect::decode(effect).ok_or(RegistryError::UnknownEffect)?;
    if effect != spec.effect {
        return Err(RegistryError::EffectMismatch);
    }
    let payload = PayloadKind::decode(payload).ok_or(RegistryError::UnknownPayload)?;
    if payload != spec.payload {
        return Err(RegistryError::PayloadMismatch);
    }
    Ok(spec)
}

pub(crate) struct LegacySpecs {
    next: usize,
    remaining: usize,
}
impl LegacySpecs {
    pub fn new() -> Self {
        Self {
            next: 0,
            remaining: REGISTRY
                .iter()
                .filter(|entry| entry.legacy.is_some())
                .count(),
        }
    }
}
impl Iterator for LegacySpecs {
    type Item = &'static ActionSpec;
    fn next(&mut self) -> Option<Self::Item> {
        while let Some(entry) = REGISTRY.get(self.next) {
            self.next += 1;
            if let Some(spec) = entry.legacy.as_ref() {
                self.remaining -= 1;
                return Some(spec);
            }
        }
        None
    }
    fn size_hint(&self) -> (usize, Option<usize>) {
        (self.remaining, Some(self.remaining))
    }
}
impl ExactSizeIterator for LegacySpecs {}
