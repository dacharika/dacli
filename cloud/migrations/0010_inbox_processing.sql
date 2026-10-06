-- Explicit operator migration. Production startup only checks this catalog.
CREATE TABLE controlplane_envelope_inbox_jobs (
 tenant_id TEXT NOT NULL, project_id TEXT NOT NULL, event_id TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','processing','processed','dead-letter')),
 attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),
 fence BIGINT NOT NULL DEFAULT 0 CHECK(fence>=0),
 next_attempt_unix_milli BIGINT NOT NULL, lease_until_unix_milli BIGINT NOT NULL DEFAULT 0,
 last_error_code TEXT NOT NULL DEFAULT '' CHECK(char_length(last_error_code)<=64),
 processed_unix_milli BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,project_id,event_id),
 FOREIGN KEY(tenant_id,project_id,event_id) REFERENCES controlplane_envelope_inbox_identities(tenant_id,project_id,event_id)
);
CREATE TABLE controlplane_envelope_proposals (
 tenant_id TEXT NOT NULL, project_id TEXT NOT NULL, event_id TEXT NOT NULL,
 event_type TEXT NOT NULL, envelope_digest BYTEA NOT NULL CHECK(octet_length(envelope_digest)=32),
 payload JSONB NOT NULL CHECK(jsonb_typeof(payload)='object'),
 state TEXT NOT NULL CHECK(state='proposed'), created_unix_milli BIGINT NOT NULL,
 PRIMARY KEY(tenant_id,project_id,event_id),
 FOREIGN KEY(tenant_id,project_id,event_id) REFERENCES controlplane_envelope_inbox_identities(tenant_id,project_id,event_id)
);
CREATE FUNCTION controlplane_queue_inbox_job() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO controlplane_envelope_inbox_jobs(tenant_id,project_id,event_id,next_attempt_unix_milli)
 VALUES(NEW.tenant_id,NEW.project_id,NEW.event_id,(EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT);
 RETURN NEW;
END;
$$;
CREATE TRIGGER controlplane_queue_inbox_job_after_accept AFTER INSERT ON controlplane_envelope_inbox_payloads
 FOR EACH ROW EXECUTE FUNCTION controlplane_queue_inbox_job();
-- Backfill existing retained inboxes, without changing immutable identities.
INSERT INTO controlplane_envelope_inbox_jobs(tenant_id,project_id,event_id,next_attempt_unix_milli)
 SELECT tenant_id,project_id,event_id,(EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT
 FROM controlplane_envelope_inbox_payloads;
CREATE TRIGGER controlplane_envelope_proposal_immutable BEFORE UPDATE OR DELETE ON controlplane_envelope_proposals
 FOR EACH ROW EXECUTE FUNCTION controlplane_refuse_envelope_audit_mutation();
CREATE INDEX controlplane_inbox_jobs_due ON controlplane_envelope_inbox_jobs(tenant_id,state,next_attempt_unix_milli,lease_until_unix_milli);
ALTER TABLE controlplane_envelope_inbox_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE controlplane_envelope_inbox_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE controlplane_envelope_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE controlplane_envelope_proposals FORCE ROW LEVEL SECURITY;
CREATE POLICY controlplane_inbox_jobs_tenant ON controlplane_envelope_inbox_jobs
 USING(tenant_id=NULLIF(current_setting('dacli.tenant_id',true),'')) WITH CHECK(tenant_id=NULLIF(current_setting('dacli.tenant_id',true),''));
CREATE POLICY controlplane_envelope_proposals_tenant ON controlplane_envelope_proposals
 USING(tenant_id=NULLIF(current_setting('dacli.tenant_id',true),'')) WITH CHECK(tenant_id=NULLIF(current_setting('dacli.tenant_id',true),''));
