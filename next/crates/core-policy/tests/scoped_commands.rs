//! Semantic authorization tests use synthetic canonical facts, never credentials
//! accepted by a backend. Positive results do not activate a gateway (#1258).
use core_kernel::ActionId;
use core_policy::{admit, attenuate, ResourceFacts, TargetLineage};
use gateway_contracts::*;
use store_contracts::{CreateTask, TaskReference, TaskScope};

const ALL: [ActionId; 6] = [
    ActionId::CreateTask,
    ActionId::ReadTask,
    ActionId::QueryTasks,
    ActionId::ObserveCreateTask,
    ActionId::DelegateSession,
    ActionId::RevokeSession,
];
fn time(value: u64) -> TimestampMillis {
    TimestampMillis::new(value).unwrap()
}
fn scope() -> TaskScope {
    TaskScope {
        authority_id: "authority".into(),
        workspace_id: "workspace".into(),
        project_id: "project".into(),
    }
}
fn exact(scope: TaskScope) -> ExactScope {
    ExactScope::new(scope).unwrap()
}
fn grants(actions: &[ActionId]) -> GrantSet {
    GrantSet::new(
        actions
            .iter()
            .map(|action| Grant::new(*action, action.spec().effect).unwrap())
            .collect(),
    )
    .unwrap()
}
fn session_id(value: &str) -> SessionId {
    SessionId::new(value.into()).unwrap()
}
fn actor_id(value: &str) -> ActorId {
    ActorId::new(value.into()).unwrap()
}
fn credential_id(value: &str) -> CredentialId {
    CredentialId::new(value.into()).unwrap()
}
fn generation(value: u64) -> CredentialGeneration {
    CredentialGeneration::new(value).unwrap()
}
#[derive(Clone)]
struct Row {
    id: String,
    actor: String,
    parent: Option<String>,
    scope: TaskScope,
    actions: Vec<ActionId>,
    issued: u64,
    expires: u64,
    depth: u8,
    revoked: bool,
}
impl Row {
    fn new(name: &str) -> Self {
        Self {
            id: name.into(),
            actor: format!("{name}-actor"),
            parent: None,
            scope: scope(),
            actions: ALL.to_vec(),
            issued: 10,
            expires: 1000,
            depth: 3,
            revoked: false,
        }
    }
    fn record(&self) -> SessionRecord {
        SessionRecord::new(
            session_id(&self.id),
            actor_id(&self.actor),
            self.parent.as_deref().map(session_id),
            exact(self.scope.clone()),
            grants(&self.actions),
            SessionLifetime::new(time(self.issued), time(self.expires)).unwrap(),
            SessionState::new(self.revoked, DelegationDepth::new(self.depth).unwrap()),
        )
    }
}
struct Fixture {
    current: Row,
    ancestors: Vec<Row>,
    kind: ActorKind,
    authority: String,
    canonical_credential: CredentialRecord,
    actor: String,
    evidence: CredentialEvidence,
}
impl Fixture {
    fn root(kind: ActorKind) -> Self {
        let current = Row::new("caller");
        Self {
            actor: current.actor.clone(),
            canonical_credential: CredentialRecord::new(
                AuthorityId::new("authority".into()).unwrap(),
                credential_id("credential"),
                session_id(&current.id),
                generation(7),
            ),
            current,
            ancestors: vec![],
            kind,
            authority: "authority".into(),
            evidence: CredentialEvidence::Matched(CredentialMatch::new(
                credential_id("credential"),
                session_id("caller"),
                generation(7),
            )),
        }
    }
    fn delegated() -> Self {
        let mut fixture = Self::root(ActorKind::Agent);
        fixture.current.parent = Some("parent".into());
        fixture.current.depth = 1;
        let mut parent = Row::new("parent");
        parent.parent = Some("root".into());
        parent.depth = 2;
        parent.issued = 5;
        parent.expires = 1500;
        let mut root = Row::new("root");
        root.issued = 1;
        root.expires = 2000;
        fixture.ancestors = vec![parent, root];
        fixture
    }
    fn facts(&self) -> AuthorityFacts {
        AuthorityFacts::new(
            AuthorityId::new(self.authority.clone()).unwrap(),
            RevocationEpoch::new(4).unwrap(),
            ActorRecord::new(actor_id(&self.actor), self.kind),
            self.current.record(),
            self.canonical_credential.clone(),
            self.ancestors.iter().map(Row::record).collect(),
        )
        .unwrap()
    }
    fn read(&self) -> Result<(), PolicyRefusal> {
        admit(
            &self.facts(),
            &self.evidence,
            &read(scope()),
            ResourceFacts::None,
            time(100),
        )
    }
    fn narrow(&self, requested: RequestedGrant, kind: ActorKind) -> Result<(), PolicyRefusal> {
        attenuate(&self.facts(), &self.evidence, &requested, kind, time(100))
    }
}
fn read(scope: TaskScope) -> PreparedCommand {
    PreparedCommand::new(CommandInput::ReadTask {
        reference: TaskReference {
            scope,
            task_id: "task".into(),
        },
        expected_version: None,
    })
    .unwrap()
}
fn task() -> CreateTask {
    CreateTask {
        scope: scope(),
        operation_key: "operation".into(),
        task_id: "task".into(),
        expected_version: 0,
        title: "private-title-canary".into(),
        body: b"private-body-canary".to_vec(),
        artifact_references: vec![],
    }
}
fn requested() -> RequestedGrant {
    request(scope(), &[ActionId::ReadTask], 500, 0)
}
fn request(scope: TaskScope, actions: &[ActionId], expires: u64, depth: u8) -> RequestedGrant {
    RequestedGrant::new(
        exact(scope),
        grants(actions),
        time(expires),
        DelegationDepth::new(depth).unwrap(),
    )
}
fn revoke(target: &str) -> PreparedCommand {
    PreparedCommand::new(CommandInput::RevokeSession {
        scope: exact(scope()),
        operation_key: OperationKey::new("operation".into()).unwrap(),
        target: session_id(target),
    })
    .unwrap()
}
fn revoke_target(
    fixture: &Fixture,
    target: &SessionRecord,
    ancestors: &[SessionRecord],
) -> Result<(), PolicyRefusal> {
    admit(
        &fixture.facts(),
        &fixture.evidence,
        &revoke(target.id().as_str()),
        ResourceFacts::RevocationTarget(TargetLineage::new(target, ancestors).unwrap()),
        time(100),
    )
}
fn child_of(fixture: &Fixture) -> Row {
    let mut child = Row::new("target");
    child.parent = Some(fixture.current.id.clone());
    child.issued = 20;
    child.expires = 900;
    child.depth = fixture.current.depth - 1;
    child.actions = vec![ActionId::ReadTask];
    child
}
fn target_ancestors(fixture: &Fixture) -> Vec<SessionRecord> {
    core::iter::once(&fixture.current)
        .chain(&fixture.ancestors)
        .map(Row::record)
        .collect()
}

#[test]
fn exact_current_facts_allow_each_explicit_action_without_activating_dispatch() {
    let fixture = Fixture::root(ActorKind::Agent);
    let commands = [
        PreparedCommand::new(CommandInput::CreateTask(task())).unwrap(),
        read(scope()),
        PreparedCommand::new(CommandInput::QueryTasks {
            scope: scope(),
            limit: 16,
        })
        .unwrap(),
        PreparedCommand::new(CommandInput::ObserveCreateTask(task())).unwrap(),
        PreparedCommand::new(CommandInput::DelegateSession {
            operation_key: OperationKey::new("operation".into()).unwrap(),
            requested: requested(),
        })
        .unwrap(),
    ];
    for command in commands {
        assert_eq!(
            admit(
                &fixture.facts(),
                &fixture.evidence,
                &command,
                ResourceFacts::None,
                time(100)
            ),
            Ok(())
        );
        assert_eq!(
            command.action().spec().availability,
            core_types::Availability::Unavailable
        );
    }
    let facts = fixture.facts();
    assert_eq!(
        revoke_target(&fixture, facts.session(), facts.ancestors()),
        Ok(())
    );
}

#[test]
fn parent_first_complete_three_generation_chain_is_admitted() {
    assert_eq!(Fixture::delegated().read(), Ok(()));
}
#[test]
fn missing_evidence_refuses_even_caller_created_owner_facts() {
    let mut fixture = Fixture::root(ActorKind::Owner);
    fixture.evidence = CredentialEvidence::Missing;
    assert_eq!(fixture.read(), Err(PolicyRefusal::EmptyCredential));
}
#[test]
fn invalid_evidence_refuses_even_caller_created_owner_facts() {
    let mut fixture = Fixture::root(ActorKind::Owner);
    fixture.evidence = CredentialEvidence::Invalid;
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidCredential));
}
#[test]
fn matched_credential_id_must_equal_canonical_record() {
    let mut fixture = Fixture::root(ActorKind::Owner);
    fixture.evidence = CredentialEvidence::Matched(CredentialMatch::new(
        credential_id("other"),
        session_id("caller"),
        generation(7),
    ));
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidCredential));
}
#[test]
fn matched_session_must_equal_canonical_credential_session() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.evidence = CredentialEvidence::Matched(CredentialMatch::new(
        credential_id("credential"),
        session_id("other"),
        generation(7),
    ));
    assert_eq!(fixture.read(), Err(PolicyRefusal::SessionMismatch));
}
#[test]
fn canonical_credential_must_link_current_session() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.current.id = "unlinked".into();
    assert_eq!(fixture.read(), Err(PolicyRefusal::SessionMismatch));
}
#[test]
fn credential_generation_must_equal_current_canonical_generation() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.evidence = CredentialEvidence::Matched(CredentialMatch::new(
        credential_id("credential"),
        session_id("caller"),
        generation(6),
    ));
    assert_eq!(fixture.read(), Err(PolicyRefusal::GenerationMismatch));
}
#[test]
fn canonical_credential_authority_must_equal_selected_authority() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.canonical_credential = CredentialRecord::new(
        AuthorityId::new("foreign".into()).unwrap(),
        credential_id("credential"),
        session_id("caller"),
        generation(7),
    );
    assert_eq!(fixture.read(), Err(PolicyRefusal::AuthorityMismatch));
}
#[test]
fn current_session_authority_must_equal_selected_authority() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.current.scope.authority_id = "foreign".into();
    // The command agrees with the session; selected-authority binding must
    // independently reject a canonical row from a different authority.
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &read(fixture.current.scope.clone()),
            ResourceFacts::None,
            time(100)
        ),
        Err(PolicyRefusal::AuthorityMismatch)
    );
}
#[test]
fn current_actor_must_equal_session_actor() {
    let mut fixture = Fixture::root(ActorKind::Owner);
    fixture.actor = "unlinked-owner".into();
    assert_eq!(fixture.read(), Err(PolicyRefusal::ActorMismatch));
}
#[test]
fn owner_kind_does_not_bypass_explicit_read_permission() {
    let mut fixture = Fixture::root(ActorKind::Owner);
    fixture.current.actions = vec![ActionId::QueryTasks];
    assert_eq!(fixture.read(), Err(PolicyRefusal::MissingPermission));
}
#[test]
fn delegated_actor_can_never_claim_owner_kind() {
    let mut fixture = Fixture::delegated();
    fixture.kind = ActorKind::Owner;
    assert_eq!(fixture.read(), Err(PolicyRefusal::OwnerKindNotDelegable));
}
#[test]
fn command_authority_must_match_exact_session_scope() {
    let fixture = Fixture::root(ActorKind::Owner);
    let mut foreign = scope();
    foreign.authority_id = "other".into();
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &read(foreign),
            ResourceFacts::None,
            time(100)
        ),
        Err(PolicyRefusal::AuthorityMismatch)
    );
}
#[test]
fn command_workspace_must_match_exact_session_scope() {
    let fixture = Fixture::root(ActorKind::Owner);
    let mut foreign = scope();
    foreign.workspace_id = "other".into();
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &read(foreign),
            ResourceFacts::None,
            time(100)
        ),
        Err(PolicyRefusal::ScopeDenied)
    );
}
#[test]
fn command_project_must_match_exact_session_scope() {
    let fixture = Fixture::root(ActorKind::Owner);
    let mut foreign = scope();
    foreign.project_id = "Project".into();
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &read(foreign),
            ResourceFacts::None,
            time(100)
        ),
        Err(PolicyRefusal::ScopeDenied)
    );
}
#[test]
fn exact_scope_compares_unicode_bytes_without_normalization() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.current.scope.project_id = "é".into();
    let composed = fixture.current.scope.clone();
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &read(composed.clone()),
            ResourceFacts::None,
            time(100)
        ),
        Ok(())
    );
    let mut decomposed = composed;
    decomposed.project_id = "e\u{301}".into();
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &read(decomposed),
            ResourceFacts::None,
            time(100)
        ),
        Err(PolicyRefusal::ScopeDenied)
    );
}
#[test]
fn current_session_revocation_refuses() {
    let mut fixture = Fixture::root(ActorKind::Owner);
    fixture.current.revoked = true;
    assert_eq!(fixture.read(), Err(PolicyRefusal::Revoked));
}
#[test]
fn current_session_expiry_is_exclusive_at_boundary() {
    let mut fixture = Fixture::root(ActorKind::Owner);
    fixture.current.expires = 100;
    assert_eq!(fixture.read(), Err(PolicyRefusal::Expired));
    fixture.current.expires = 101;
    assert_eq!(fixture.read(), Ok(()));
}
#[test]
fn future_issued_current_session_refuses_as_invalid_lineage() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.current.issued = 101;
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidLineage));
}
#[test]
fn every_ancestor_revocation_invalidates_grandchild() {
    for index in 0..2 {
        let mut fixture = Fixture::delegated();
        fixture.ancestors[index].revoked = true;
        assert_eq!(
            fixture.read(),
            Err(PolicyRefusal::Revoked),
            "ancestor {index}"
        );
    }
}
#[test]
fn every_ancestor_expiry_invalidates_grandchild() {
    for index in 0..2 {
        let mut fixture = Fixture::delegated();
        fixture.ancestors[index].expires = 100;
        // Active checks precede historical attenuation so each expired ancestor
        // has the stable Expired cause even if its child outlives that row.
        let facts = fixture.facts();
        assert_eq!(
            admit(
                &facts,
                &fixture.evidence,
                &read(scope()),
                ResourceFacts::None,
                time(100)
            ),
            Err(PolicyRefusal::Expired),
            "ancestor {index}"
        );
    }
}
#[test]
fn every_future_issued_ancestor_refuses_as_invalid_lineage() {
    for index in 0..2 {
        let mut fixture = Fixture::delegated();
        fixture.ancestors[index].issued = 101;
        assert_eq!(
            fixture.read(),
            Err(PolicyRefusal::InvalidLineage),
            "ancestor {index}"
        );
    }
}
#[test]
fn missing_parent_record_refuses() {
    let mut fixture = Fixture::delegated();
    fixture.ancestors.remove(0);
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidLineage));
}
#[test]
fn truncated_lineage_refuses_missing_root() {
    let mut fixture = Fixture::delegated();
    fixture.ancestors.pop();
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidLineage));
}
#[test]
fn extra_ancestor_of_root_refuses() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.ancestors.push(Row::new("unrelated"));
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidLineage));
}
#[test]
fn root_first_ancestor_order_refuses() {
    let mut fixture = Fixture::delegated();
    fixture.ancestors.reverse();
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidLineage));
}
#[test]
fn cyclic_lineage_refuses() {
    let mut fixture = Fixture::delegated();
    fixture.ancestors[1].parent = Some("caller".into());
    fixture.ancestors.push(fixture.current.clone());
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidLineage));
}
#[test]
fn existing_child_cannot_rebind_an_ancestor_actor() {
    let mut fixture = Fixture::delegated();
    fixture.ancestors[1].actor = fixture.current.actor.clone();
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidLineage));
}
#[test]
fn existing_ancestor_must_keep_exact_scope() {
    let mut fixture = Fixture::delegated();
    fixture.ancestors[1].scope.project_id = "foreign".into();
    assert_eq!(fixture.read(), Err(PolicyRefusal::ScopeDenied));
}
#[test]
fn existing_parent_must_hold_explicit_delegation_permission() {
    let mut fixture = Fixture::delegated();
    fixture.current.actions = vec![ActionId::ReadTask];
    fixture.ancestors[0].actions = vec![ActionId::ReadTask];
    assert_eq!(fixture.read(), Err(PolicyRefusal::MissingPermission));
}
#[test]
fn existing_child_actions_cannot_exceed_parent_grants() {
    let mut fixture = Fixture::delegated();
    fixture.ancestors[0]
        .actions
        .retain(|action| *action != ActionId::CreateTask);
    assert_eq!(fixture.read(), Err(PolicyRefusal::DelegationAmplification));
}
#[test]
fn existing_child_expiry_cannot_exceed_parent_expiry() {
    let mut fixture = Fixture::delegated();
    fixture.current.expires = 1501;
    assert_eq!(fixture.read(), Err(PolicyRefusal::DelegationAmplification));
}
#[test]
fn existing_child_issuance_cannot_predate_parent() {
    let mut fixture = Fixture::delegated();
    fixture.current.issued = 4;
    assert_eq!(fixture.read(), Err(PolicyRefusal::DelegationAmplification));
}
#[test]
fn existing_child_remaining_depth_must_be_strictly_less_than_parent() {
    let mut fixture = Fixture::delegated();
    fixture.current.depth = fixture.ancestors[0].depth;
    assert_eq!(fixture.read(), Err(PolicyRefusal::DelegationAmplification));
}
#[test]
fn eight_ancestors_allow_leaf_but_no_ninth_delegation() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.current.parent = Some("ancestor-1".into());
    fixture.current.depth = 0;
    for depth in 1..=8 {
        let mut ancestor = Row::new(&format!("ancestor-{depth}"));
        ancestor.depth = depth;
        ancestor.parent = (depth < 8).then(|| format!("ancestor-{}", depth + 1));
        fixture.ancestors.push(ancestor);
    }
    assert_eq!(fixture.read(), Ok(()));
    assert_eq!(
        fixture.narrow(requested(), ActorKind::Agent),
        Err(PolicyRefusal::DelegationAmplification)
    );
    let rows: Vec<_> = core::iter::once(&fixture.current)
        .chain(&fixture.ancestors)
        .map(Row::record)
        .collect();
    assert!(matches!(
        TargetLineage::new(&rows[0], &rows),
        Err(PolicyRefusal::InvalidLineage)
    ));
}

#[test]
fn read_only_query_permission_never_implies_create_or_management() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.current.actions = vec![ActionId::QueryTasks];
    let query = PreparedCommand::new(CommandInput::QueryTasks {
        scope: scope(),
        limit: 1,
    })
    .unwrap();
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &query,
            ResourceFacts::None,
            time(100)
        ),
        Ok(())
    );
    let create = PreparedCommand::new(CommandInput::CreateTask(task())).unwrap();
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &create,
            ResourceFacts::None,
            time(100)
        ),
        Err(PolicyRefusal::MissingPermission)
    );
    assert_eq!(
        fixture.narrow(requested(), ActorKind::Agent),
        Err(PolicyRefusal::MissingPermission)
    );
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &revoke("caller"),
            ResourceFacts::None,
            time(100)
        ),
        Err(PolicyRefusal::MissingPermission)
    );
}
#[test]
fn create_permission_never_implies_observation_permission() {
    let mut fixture = Fixture::root(ActorKind::Owner);
    fixture.current.actions = vec![ActionId::CreateTask];
    let observe = PreparedCommand::new(CommandInput::ObserveCreateTask(task())).unwrap();
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &observe,
            ResourceFacts::None,
            time(100)
        ),
        Err(PolicyRefusal::MissingPermission)
    );
}
#[test]
fn narrowed_child_can_keep_equal_expiry_and_explicit_management_subset() {
    let fixture = Fixture::root(ActorKind::Owner);
    assert_eq!(
        fixture.narrow(
            request(
                scope(),
                &[
                    ActionId::ReadTask,
                    ActionId::DelegateSession,
                    ActionId::RevokeSession
                ],
                1000,
                2
            ),
            ActorKind::Agent
        ),
        Ok(())
    );
}
#[test]
fn standalone_attenuation_requires_current_matched_evidence() {
    let mut fixture = Fixture::root(ActorKind::Owner);
    fixture.evidence = CredentialEvidence::Invalid;
    assert_eq!(
        fixture.narrow(requested(), ActorKind::Agent),
        Err(PolicyRefusal::InvalidCredential)
    );
}
#[test]
fn standalone_attenuation_requires_explicit_delegate_permission() {
    let mut fixture = Fixture::root(ActorKind::Owner);
    fixture.current.actions = vec![ActionId::ReadTask];
    assert_eq!(
        fixture.narrow(requested(), ActorKind::Agent),
        Err(PolicyRefusal::MissingPermission)
    );
}
#[test]
fn child_owner_kind_is_never_delegable() {
    let fixture = Fixture::root(ActorKind::Owner);
    assert_eq!(
        fixture.narrow(requested(), ActorKind::Owner),
        Err(PolicyRefusal::OwnerKindNotDelegable)
    );
}
#[test]
fn requested_child_authority_cannot_expand_parent_scope() {
    let fixture = Fixture::root(ActorKind::Owner);
    let mut foreign = scope();
    foreign.authority_id = "foreign".into();
    assert_eq!(
        fixture.narrow(
            request(foreign, &[ActionId::ReadTask], 500, 0),
            ActorKind::Agent
        ),
        Err(PolicyRefusal::AuthorityMismatch)
    );
}
#[test]
fn requested_child_workspace_cannot_expand_parent_scope() {
    let fixture = Fixture::root(ActorKind::Owner);
    let mut foreign = scope();
    foreign.workspace_id = "foreign".into();
    assert_eq!(
        fixture.narrow(
            request(foreign, &[ActionId::ReadTask], 500, 0),
            ActorKind::Agent
        ),
        Err(PolicyRefusal::ScopeDenied)
    );
}
#[test]
fn requested_child_project_cannot_expand_parent_scope() {
    let fixture = Fixture::root(ActorKind::Owner);
    let mut foreign = scope();
    foreign.project_id = "foreign".into();
    assert_eq!(
        fixture.narrow(
            request(foreign, &[ActionId::ReadTask], 500, 0),
            ActorKind::Agent
        ),
        Err(PolicyRefusal::ScopeDenied)
    );
}
#[test]
fn requested_child_actions_cannot_amplify_parent_grants() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.current.actions = vec![ActionId::ReadTask, ActionId::DelegateSession];
    assert_eq!(
        fixture.narrow(
            request(scope(), &[ActionId::CreateTask], 500, 0),
            ActorKind::Agent
        ),
        Err(PolicyRefusal::DelegationAmplification)
    );
}
#[test]
fn requested_child_expiry_cannot_exceed_parent_expiry() {
    let fixture = Fixture::root(ActorKind::Owner);
    assert_eq!(
        fixture.narrow(
            request(scope(), &[ActionId::ReadTask], 1001, 0),
            ActorKind::Agent
        ),
        Err(PolicyRefusal::DelegationAmplification)
    );
}
#[test]
fn requested_child_expiry_must_be_after_admission() {
    let fixture = Fixture::root(ActorKind::Agent);
    assert_eq!(
        fixture.narrow(
            request(scope(), &[ActionId::ReadTask], 100, 0),
            ActorKind::Agent
        ),
        Err(PolicyRefusal::Expired)
    );
}
#[test]
fn requested_child_remaining_depth_must_be_strictly_less_than_parent() {
    let fixture = Fixture::root(ActorKind::Agent);
    assert_eq!(
        fixture.narrow(
            request(scope(), &[ActionId::ReadTask], 500, 3),
            ActorKind::Agent
        ),
        Err(PolicyRefusal::DelegationAmplification)
    );
}
#[test]
fn zero_remaining_depth_cannot_issue_even_a_zero_depth_child() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.current.depth = 0;
    assert_eq!(
        fixture.narrow(requested(), ActorKind::Agent),
        Err(PolicyRefusal::DelegationAmplification)
    );
}
#[test]
fn delegate_command_checks_attenuation_without_separate_caller_step() {
    let fixture = Fixture::root(ActorKind::Owner);
    let command = PreparedCommand::new(CommandInput::DelegateSession {
        operation_key: OperationKey::new("operation".into()).unwrap(),
        requested: request(scope(), &[ActionId::ReadTask], 1001, 0),
    })
    .unwrap();
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &command,
            ResourceFacts::None,
            time(100)
        ),
        Err(PolicyRefusal::DelegationAmplification)
    );
}

#[test]
fn revoke_command_cannot_admit_an_unknown_target() {
    let fixture = Fixture::root(ActorKind::Owner);
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &revoke("unknown"),
            ResourceFacts::None,
            time(100)
        ),
        Err(PolicyRefusal::InvalidLineage)
    );
}
#[test]
fn unrelated_target_facts_are_rejected_for_nonrevocation_commands() {
    let fixture = Fixture::root(ActorKind::Owner);
    let target = child_of(&fixture).record();
    let ancestors = target_ancestors(&fixture);
    let resource =
        ResourceFacts::RevocationTarget(TargetLineage::new(&target, &ancestors).unwrap());
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &read(scope()),
            resource,
            time(100)
        ),
        Err(PolicyRefusal::InvalidLineage)
    );
}
#[test]
fn revocation_target_must_match_prepared_target_identifier() {
    let fixture = Fixture::root(ActorKind::Owner);
    let target = child_of(&fixture).record();
    let ancestors = target_ancestors(&fixture);
    let resource =
        ResourceFacts::RevocationTarget(TargetLineage::new(&target, &ancestors).unwrap());
    assert_eq!(
        admit(
            &fixture.facts(),
            &fixture.evidence,
            &revoke("different"),
            resource,
            time(100)
        ),
        Err(PolicyRefusal::SessionMismatch)
    );
}
#[test]
fn agent_can_revoke_self_and_actual_descendant() {
    let fixture = Fixture::delegated();
    let facts = fixture.facts();
    assert_eq!(
        revoke_target(&fixture, facts.session(), facts.ancestors()),
        Ok(())
    );
    let target = child_of(&fixture).record();
    assert_eq!(
        revoke_target(&fixture, &target, &target_ancestors(&fixture)),
        Ok(())
    );
}
#[test]
fn agent_cannot_revoke_a_sibling_with_same_scope() {
    let fixture = Fixture::delegated();
    let mut sibling = child_of(&fixture);
    sibling.parent = fixture.current.parent.clone();
    sibling.depth = fixture.current.depth;
    let ancestors: Vec<_> = fixture.ancestors.iter().map(Row::record).collect();
    assert_eq!(
        revoke_target(&fixture, &sibling.record(), &ancestors),
        Err(PolicyRefusal::ScopeDenied)
    );
}
#[test]
fn agent_cannot_revoke_its_ancestor() {
    let fixture = Fixture::delegated();
    let facts = fixture.facts();
    assert_eq!(
        revoke_target(&fixture, &facts.ancestors()[0], &facts.ancestors()[1..]),
        Err(PolicyRefusal::ScopeDenied)
    );
}
#[test]
fn root_owner_with_explicit_grant_can_revoke_unrelated_same_scope_target() {
    let fixture = Fixture::root(ActorKind::Owner);
    let mut target = Row::new("unrelated-root");
    target.actions = vec![ActionId::ReadTask];
    assert_eq!(revoke_target(&fixture, &target.record(), &[]), Ok(()));
}
#[test]
fn root_owner_kind_does_not_replace_revoke_permission() {
    let mut fixture = Fixture::root(ActorKind::Owner);
    fixture.current.actions = vec![ActionId::ReadTask];
    assert_eq!(
        revoke_target(&fixture, &Row::new("unrelated-root").record(), &[]),
        Err(PolicyRefusal::MissingPermission)
    );
}
#[test]
fn owner_cannot_revoke_foreign_authority_target() {
    let fixture = Fixture::root(ActorKind::Owner);
    let mut target = Row::new("target");
    target.scope.authority_id = "foreign".into();
    assert_eq!(
        revoke_target(&fixture, &target.record(), &[]),
        Err(PolicyRefusal::AuthorityMismatch)
    );
}
#[test]
fn owner_cannot_revoke_foreign_project_target() {
    let fixture = Fixture::root(ActorKind::Owner);
    let mut target = Row::new("target");
    target.scope.project_id = "foreign".into();
    assert_eq!(
        revoke_target(&fixture, &target.record(), &[]),
        Err(PolicyRefusal::ScopeDenied)
    );
}
#[test]
fn revoke_target_requires_complete_canonical_chain() {
    let fixture = Fixture::root(ActorKind::Owner);
    let target = child_of(&fixture).record();
    assert_eq!(
        revoke_target(&fixture, &target, &[]),
        Err(PolicyRefusal::InvalidLineage)
    );
}
#[test]
fn revocation_rejects_conflicting_current_row_in_descendant_ancestry() {
    let fixture = Fixture::delegated();
    let target = child_of(&fixture).record();
    let mut ancestors = target_ancestors(&fixture);
    let mut altered_caller = fixture.current.clone();
    altered_caller.expires -= 1;
    ancestors[0] = altered_caller.record();
    assert_eq!(
        revoke_target(&fixture, &target, &ancestors),
        Err(PolicyRefusal::InvalidLineage)
    );
}
#[test]
fn revocation_rejects_conflicting_shared_ancestor_row() {
    let fixture = Fixture::delegated();
    let target = child_of(&fixture).record();
    let mut ancestors = target_ancestors(&fixture);
    let mut altered_root = fixture.ancestors[1].clone();
    altered_root.expires -= 1;
    ancestors[2] = altered_root.record();
    assert_eq!(
        revoke_target(&fixture, &target, &ancestors),
        Err(PolicyRefusal::InvalidLineage)
    );
}
#[test]
fn revocation_rejects_conflicting_self_target_record() {
    let fixture = Fixture::delegated();
    let mut altered = fixture.current.clone();
    altered.expires -= 1;
    let ancestors: Vec<_> = fixture.ancestors.iter().map(Row::record).collect();
    assert_eq!(
        revoke_target(&fixture, &altered.record(), &ancestors),
        Err(PolicyRefusal::InvalidLineage)
    );
}
#[test]
fn revoked_target_remains_revocable_by_live_ancestor() {
    let fixture = Fixture::delegated();
    let mut target = child_of(&fixture);
    target.revoked = true;
    assert_eq!(
        revoke_target(&fixture, &target.record(), &target_ancestors(&fixture)),
        Ok(())
    );
}
#[test]
fn expired_target_remains_revocable_by_live_ancestor() {
    let fixture = Fixture::delegated();
    let mut target = child_of(&fixture);
    target.expires = 100;
    assert_eq!(
        revoke_target(&fixture, &target.record(), &target_ancestors(&fixture)),
        Ok(())
    );
}
#[test]
fn inactive_target_ancestor_does_not_block_root_revocation() {
    let fixture = Fixture::root(ActorKind::Owner);
    let mut parent = child_of(&fixture);
    parent.id = "inactive-parent".into();
    parent.actor = "inactive-parent-actor".into();
    parent.depth = 1;
    parent.expires = 100;
    parent.revoked = true;
    parent.actions = vec![ActionId::ReadTask, ActionId::DelegateSession];
    let mut target = child_of(&fixture);
    target.parent = Some(parent.id.clone());
    target.depth = 0;
    target.expires = 90;
    target.issued = 30;
    assert_eq!(
        revoke_target(
            &fixture,
            &target.record(),
            &[parent.record(), fixture.current.record()]
        ),
        Ok(())
    );
}
#[test]
fn revoked_caller_cannot_use_live_target_as_authority() {
    let mut fixture = Fixture::delegated();
    let target = child_of(&fixture).record();
    fixture.current.revoked = true;
    assert_eq!(
        revoke_target(&fixture, &target, &target_ancestors(&fixture)),
        Err(PolicyRefusal::Revoked)
    );
}

#[test]
fn future_issued_target_remains_revocable_by_live_ancestor() {
    let fixture = Fixture::delegated();
    let mut target = child_of(&fixture);
    target.issued = 101;
    assert_eq!(
        revoke_target(&fixture, &target.record(), &target_ancestors(&fixture)),
        Ok(())
    );
}
#[test]
fn future_issued_target_ancestor_does_not_block_root_revocation() {
    let fixture = Fixture::root(ActorKind::Owner);
    let mut parent = child_of(&fixture);
    parent.id = "future-parent".into();
    parent.actor = "future-parent-actor".into();
    parent.depth = 1;
    parent.issued = 101;
    parent.actions = vec![ActionId::ReadTask, ActionId::DelegateSession];
    let mut target = child_of(&fixture);
    target.parent = Some(parent.id.clone());
    target.depth = 0;
    target.issued = 102;
    assert_eq!(
        revoke_target(
            &fixture,
            &target.record(),
            &[parent.record(), fixture.current.record()]
        ),
        Ok(())
    );
}
#[test]
fn repeated_ancestor_session_identifier_refuses_conflicting_rows() {
    let mut fixture = Fixture::delegated();
    fixture.ancestors[1].parent = Some("root".into());
    let mut duplicate = fixture.ancestors[1].clone();
    duplicate.actor = "different-root-actor".into();
    duplicate.parent = None;
    duplicate.depth = 4;
    fixture.ancestors.push(duplicate);
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidLineage));
}
#[test]
fn existing_ancestors_cannot_share_actor_identity() {
    let mut fixture = Fixture::delegated();
    fixture.ancestors[1].actor = fixture.ancestors[0].actor.clone();
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidLineage));
}

#[test]
fn root_agent_cannot_revoke_unrelated_same_scope_target() {
    let fixture = Fixture::root(ActorKind::Agent);
    assert_eq!(
        revoke_target(&fixture, &Row::new("unrelated-root").record(), &[]),
        Err(PolicyRefusal::ScopeDenied)
    );
}
#[test]
fn issued_at_boundary_is_live_until_expiry() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.current.issued = 100;
    assert_eq!(fixture.read(), Ok(()));
}

#[test]
fn session_cannot_list_its_own_identifier_as_distinct_parent() {
    let mut fixture = Fixture::root(ActorKind::Agent);
    fixture.current.parent = Some("caller".into());
    fixture.current.depth = 1;
    let mut conflicting_parent = Row::new("caller");
    conflicting_parent.actor = "other-parent-actor".into();
    conflicting_parent.issued = 1;
    conflicting_parent.expires = 2000;
    fixture.ancestors.push(conflicting_parent);
    assert_eq!(fixture.read(), Err(PolicyRefusal::InvalidLineage));
}
