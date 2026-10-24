-- Hosted role catalogs store exact signed rolebundle releases for one tenant.
-- This migration is operator-run; application startup never applies it.

CREATE TABLE controlplane_role_catalogs (
    tenant_id TEXT NOT NULL,
    catalog_id TEXT NOT NULL,
    catalog_version BIGINT NOT NULL DEFAULT 1 CHECK (catalog_version = 1),
    team_id TEXT,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, catalog_id),
    FOREIGN KEY (tenant_id) REFERENCES controlplane_organizations (tenant_id),
    FOREIGN KEY (tenant_id, team_id)
        REFERENCES controlplane_teams (tenant_id, team_id)
);

CREATE TABLE controlplane_role_publishers (
    tenant_id TEXT NOT NULL,
    catalog_id TEXT NOT NULL,
    key_id TEXT NOT NULL,
    public_key BYTEA NOT NULL CHECK (octet_length(public_key) = 32),
    enrolled_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, catalog_id, key_id),
    FOREIGN KEY (tenant_id, catalog_id)
        REFERENCES controlplane_role_catalogs (tenant_id, catalog_id)
);

CREATE TABLE controlplane_role_releases (
    tenant_id TEXT NOT NULL,
    catalog_id TEXT NOT NULL,
    role_id TEXT NOT NULL,
    version TEXT NOT NULL,
    content_digest TEXT NOT NULL CHECK (content_digest ~ '^sha256:[0-9a-f]{64}$'),
    release_bytes BYTEA NOT NULL CHECK (octet_length(release_bytes) > 0),
    publisher_key_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, catalog_id, role_id, version),
    UNIQUE (tenant_id, catalog_id, role_id, version, content_digest),
    FOREIGN KEY (tenant_id, catalog_id)
        REFERENCES controlplane_role_catalogs (tenant_id, catalog_id),
    FOREIGN KEY (tenant_id, catalog_id, publisher_key_id)
        REFERENCES controlplane_role_publishers (tenant_id, catalog_id, key_id)
);

CREATE TABLE controlplane_role_lifecycle (
    tenant_id TEXT NOT NULL,
    catalog_id TEXT NOT NULL,
    role_id TEXT NOT NULL,
    version TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    hosted_state TEXT NOT NULL CHECK (hosted_state IN ('draft', 'released', 'deprecated', 'revoked')),
    signed_status_bytes BYTEA,
    signed_status_digest TEXT,
    actor_id TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 1024),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, catalog_id, role_id, sequence),
    FOREIGN KEY (tenant_id, catalog_id, role_id, version, content_digest)
        REFERENCES controlplane_role_releases (tenant_id, catalog_id, role_id, version, content_digest),
    FOREIGN KEY (tenant_id, actor_id)
        REFERENCES controlplane_memberships (tenant_id, account_id),
    CHECK ((signed_status_bytes IS NULL) = (signed_status_digest IS NULL)),
    CHECK (signed_status_digest IS NULL OR signed_status_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK ((hosted_state = 'draft' AND signed_status_bytes IS NULL) OR
           (hosted_state <> 'draft' AND signed_status_bytes IS NOT NULL))
);

CREATE TABLE controlplane_role_pins (
    tenant_id TEXT NOT NULL,
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('organization', 'team', 'project')),
    scope_id TEXT NOT NULL,
    organization_id TEXT,
    team_id TEXT,
    project_id TEXT,
    role_id TEXT NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    generation BIGINT NOT NULL CHECK (generation > 0),
    catalog_id TEXT NOT NULL,
    version TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    previous_catalog_id TEXT,
    previous_version TEXT,
    previous_digest TEXT,
    actor_id TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 1024),
    pinned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, scope_kind, scope_id, role_id, sequence),
    UNIQUE (tenant_id, scope_kind, scope_id, role_id, generation),
    FOREIGN KEY (tenant_id) REFERENCES controlplane_organizations (tenant_id),
    FOREIGN KEY (tenant_id, organization_id)
        REFERENCES controlplane_organizations (tenant_id, organization_id)
        DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (tenant_id, team_id)
        REFERENCES controlplane_teams (tenant_id, team_id)
        DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (tenant_id, project_id)
        REFERENCES controlplane_projects (tenant_id, project_id)
        DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (tenant_id, catalog_id, role_id, version, content_digest)
        REFERENCES controlplane_role_releases (tenant_id, catalog_id, role_id, version, content_digest),
    FOREIGN KEY (tenant_id, previous_catalog_id, role_id, previous_version, previous_digest)
        REFERENCES controlplane_role_releases (tenant_id, catalog_id, role_id, version, content_digest),
    FOREIGN KEY (tenant_id, actor_id)
        REFERENCES controlplane_memberships (tenant_id, account_id),
    CHECK ((previous_catalog_id IS NULL AND previous_version IS NULL AND previous_digest IS NULL) OR
           (previous_catalog_id IS NOT NULL AND previous_version IS NOT NULL AND previous_digest IS NOT NULL)),
    CHECK ((scope_kind = 'organization' AND organization_id IS NOT NULL AND organization_id = scope_id AND team_id IS NULL AND project_id IS NULL) OR
           (scope_kind = 'team' AND organization_id IS NULL AND team_id IS NOT NULL AND team_id = scope_id AND project_id IS NULL) OR
           (scope_kind = 'project' AND organization_id IS NULL AND team_id IS NULL AND project_id IS NOT NULL AND project_id = scope_id)),
    CHECK (scope_kind <> 'organization' OR (scope_id = tenant_id AND organization_id = tenant_id))
);

CREATE TABLE controlplane_role_audit_events (
    tenant_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    actor_device_id TEXT,
    action TEXT NOT NULL CHECK (char_length(action) BETWEEN 1 AND 128),
    target_id TEXT NOT NULL,
    action_digest BYTEA NOT NULL CHECK (octet_length(action_digest) = 32),
    reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 1024),
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, correlation_id),
    FOREIGN KEY (tenant_id) REFERENCES controlplane_organizations (tenant_id),
    FOREIGN KEY (tenant_id, actor_id)
        REFERENCES controlplane_memberships (tenant_id, account_id),
    FOREIGN KEY (tenant_id, actor_device_id)
        REFERENCES controlplane_devices (tenant_id, device_id)
);

CREATE FUNCTION controlplane_refuse_role_registry_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'hosted role registry objects and history are immutable';
END;
$$;

CREATE TRIGGER controlplane_role_catalogs_immutable
BEFORE UPDATE OR DELETE ON controlplane_role_catalogs
FOR EACH ROW EXECUTE FUNCTION controlplane_refuse_role_registry_mutation();
CREATE TRIGGER controlplane_role_publishers_immutable
BEFORE UPDATE OR DELETE ON controlplane_role_publishers
FOR EACH ROW EXECUTE FUNCTION controlplane_refuse_role_registry_mutation();
CREATE TRIGGER controlplane_role_releases_immutable
BEFORE UPDATE OR DELETE ON controlplane_role_releases
FOR EACH ROW EXECUTE FUNCTION controlplane_refuse_role_registry_mutation();
CREATE TRIGGER controlplane_role_lifecycle_append_only
BEFORE UPDATE OR DELETE ON controlplane_role_lifecycle
FOR EACH ROW EXECUTE FUNCTION controlplane_refuse_role_registry_mutation();
CREATE TRIGGER controlplane_role_pins_append_only
BEFORE UPDATE OR DELETE ON controlplane_role_pins
FOR EACH ROW EXECUTE FUNCTION controlplane_refuse_role_registry_mutation();
CREATE TRIGGER controlplane_role_audit_append_only
BEFORE UPDATE OR DELETE ON controlplane_role_audit_events
FOR EACH ROW EXECUTE FUNCTION controlplane_refuse_role_registry_mutation();

ALTER TABLE controlplane_role_catalogs ENABLE ROW LEVEL SECURITY;
ALTER TABLE controlplane_role_catalogs FORCE ROW LEVEL SECURITY;
ALTER TABLE controlplane_role_publishers ENABLE ROW LEVEL SECURITY;
ALTER TABLE controlplane_role_publishers FORCE ROW LEVEL SECURITY;
ALTER TABLE controlplane_role_releases ENABLE ROW LEVEL SECURITY;
ALTER TABLE controlplane_role_releases FORCE ROW LEVEL SECURITY;
ALTER TABLE controlplane_role_lifecycle ENABLE ROW LEVEL SECURITY;
ALTER TABLE controlplane_role_lifecycle FORCE ROW LEVEL SECURITY;
ALTER TABLE controlplane_role_pins ENABLE ROW LEVEL SECURITY;
ALTER TABLE controlplane_role_pins FORCE ROW LEVEL SECURITY;
ALTER TABLE controlplane_role_audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE controlplane_role_audit_events FORCE ROW LEVEL SECURITY;

CREATE POLICY controlplane_role_catalogs_tenant ON controlplane_role_catalogs
    USING (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''));
CREATE POLICY controlplane_role_publishers_tenant ON controlplane_role_publishers
    USING (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''));
CREATE POLICY controlplane_role_releases_tenant ON controlplane_role_releases
    USING (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''));
CREATE POLICY controlplane_role_lifecycle_tenant ON controlplane_role_lifecycle
    USING (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''));
CREATE POLICY controlplane_role_pins_tenant ON controlplane_role_pins
    USING (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''));
CREATE POLICY controlplane_role_audit_events_tenant ON controlplane_role_audit_events
    USING (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('dacli.tenant_id', true), ''));
