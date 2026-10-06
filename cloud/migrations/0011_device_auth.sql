-- Explicit operator migration only. Ciphertexts are bound to tenant and flow ID.
CREATE TABLE controlplane_device_auth (
 tenant_id TEXT NOT NULL REFERENCES controlplane_organizations(tenant_id),
 flow_id TEXT NOT NULL CHECK (char_length(flow_id)=43),
 ciphertext BYTEA NOT NULL CHECK (octet_length(ciphertext) BETWEEN 28 AND 131104),
 expires_unix BIGINT NOT NULL CHECK(expires_unix>0),
 version BIGINT NOT NULL DEFAULT 1 CHECK(version>0),
 PRIMARY KEY(tenant_id,flow_id)
);
ALTER TABLE controlplane_device_auth ENABLE ROW LEVEL SECURITY;
ALTER TABLE controlplane_device_auth FORCE ROW LEVEL SECURITY;
CREATE POLICY controlplane_device_auth_tenant ON controlplane_device_auth
 USING(tenant_id=NULLIF(current_setting('dacli.tenant_id',true),''))
 WITH CHECK(tenant_id=NULLIF(current_setting('dacli.tenant_id',true),''));
CREATE INDEX controlplane_device_auth_expiry ON controlplane_device_auth(tenant_id,expires_unix);

-- Stable page identity is independent of delivery state and wall-clock ties.
ALTER TABLE controlplane_envelope_outbox ADD COLUMN delivery_sequence BIGSERIAL;
CREATE UNIQUE INDEX controlplane_outbox_delivery_sequence ON controlplane_envelope_outbox(delivery_sequence);

-- Allocation is serialized before nextval and the lock survives until commit.
-- BIGSERIAL alone orders allocation, not visibility, and would skip late commits.
ALTER TABLE controlplane_envelope_outbox ALTER COLUMN delivery_sequence DROP DEFAULT;
CREATE FUNCTION controlplane_allocate_delivery_sequence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended(NEW.tenant_id || ':' || NEW.project_id, 616));
 NEW.delivery_sequence := nextval('controlplane_envelope_outbox_delivery_sequence_seq');
 RETURN NEW;
END;
$$;
CREATE TRIGGER controlplane_allocate_delivery_sequence BEFORE INSERT ON controlplane_envelope_outbox
 FOR EACH ROW EXECUTE FUNCTION controlplane_allocate_delivery_sequence();
CREATE FUNCTION controlplane_immutable_delivery_sequence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.delivery_sequence IS DISTINCT FROM OLD.delivery_sequence THEN
  RAISE EXCEPTION 'delivery sequence is immutable';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER controlplane_immutable_delivery_sequence BEFORE UPDATE ON controlplane_envelope_outbox
 FOR EACH ROW EXECUTE FUNCTION controlplane_immutable_delivery_sequence();
