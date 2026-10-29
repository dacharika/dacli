use core_kernel::{
    action_specs, execute, resolve_scoped, scoped_action_specs, ActionId, PayloadKind,
    RegistryError, SCOPED_PROTOCOL_VERSION,
};
use core_types::{Availability, CredentialContext, Effect, Outcome, Refusal, Request};

#[test]
fn one_registry_drives_scoped_names_payloads_effects_permissions_and_discovery() {
    let specs: Vec<_> = scoped_action_specs().collect();
    assert_eq!(specs.len(), 7);
    let mut names = std::collections::BTreeSet::new();
    for spec in specs {
        assert!(names.insert(spec.name));
        assert_eq!(ActionId::decode(spec.name), Some(spec.id));
        assert_eq!(spec.id.spec(), spec);
        assert_eq!(PayloadKind::decode(spec.payload.name()), Some(spec.payload));
        assert_eq!(spec.payload.action(), spec.id);
        assert_eq!(
            resolve_scoped(spec.name, spec.effect.name(), spec.payload.name()),
            Ok(spec)
        );
        assert_eq!(spec.request_schema_version, SCOPED_PROTOCOL_VERSION);
        assert_eq!(spec.availability, Availability::Unavailable);
        if spec.id == ActionId::InspectProtocol {
            assert_eq!(spec.required_permission, None);
            assert_eq!(spec.capability, None);
        } else {
            assert_eq!(spec.required_permission, Some(spec.id));
            assert!(spec.capability.is_some());
        }
    }
}

#[test]
fn scoped_resolver_refuses_forged_effect_payload_and_unknown_metadata() {
    assert_eq!(
        resolve_scoped("CreateTask", "read_only", "create_task"),
        Err(RegistryError::EffectMismatch)
    );
    assert_eq!(
        resolve_scoped("CreateTask", "mutation", "query_tasks"),
        Err(RegistryError::PayloadMismatch)
    );
    assert_eq!(
        resolve_scoped("MissingAction", "mutation", "create_task"),
        Err(RegistryError::UnregisteredAction)
    );
    assert_eq!(
        resolve_scoped("CreateTask", "maybe", "create_task"),
        Err(RegistryError::UnknownEffect)
    );
    assert_eq!(
        resolve_scoped("CreateTask", "mutation", "missing_payload"),
        Err(RegistryError::UnknownPayload)
    );
}

#[test]
fn v1_discovery_and_refusals_do_not_gain_scoped_routes_or_authenticated_dispatch() {
    let legacy: Vec<_> = action_specs().collect();
    assert_eq!(legacy.len(), 2);
    assert_eq!(legacy[0].name, "InspectProtocol");
    assert_eq!(legacy[1].name, "CreateTask");
    assert_eq!(legacy[0].effect, Effect::ReadOnly);
    assert_eq!(legacy[0].availability, Availability::Available);
    assert_eq!(legacy[1].availability, Availability::Unavailable);
    assert_eq!(
        legacy[1].summary,
        "Unavailable: production task transport and scoped authentication are not implemented."
    );
    for spec in scoped_action_specs() {
        let expected = if spec.id == ActionId::InspectProtocol || spec.id == ActionId::CreateTask {
            Refusal::CredentialVerificationUnavailable
        } else {
            Refusal::UnregisteredAction
        };
        assert_eq!(
            execute(Request {
                protocol_version: 1,
                action: spec.name,
                effect: spec.effect.name(),
                context: CredentialContext::Credential("synthetic-never-verified")
            }),
            Outcome::Refused(expected)
        );
        assert_eq!(
            execute(Request {
                protocol_version: 2,
                action: spec.name,
                effect: spec.effect.name(),
                context: CredentialContext::PublicProtocol
            }),
            Outcome::Refused(Refusal::UnsupportedProtocol)
        );
    }
    let Outcome::Protocol(protocol) = execute(Request {
        protocol_version: 1,
        action: "InspectProtocol",
        effect: "read_only",
        context: CredentialContext::PublicProtocol,
    }) else {
        panic!("static discovery refused");
    };
    assert_eq!(protocol.actions.len(), 2);
    assert_eq!(
        protocol.actions,
        legacy.into_iter().copied().collect::<Vec<_>>()
    );
}
