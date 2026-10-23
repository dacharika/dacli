//go:build ignore

// Command device-identity-qualification-provider is a disposable loopback-only
// provider for exercising an extracted dacli artifact against native Keychain.
// It serves synthetic identities only and exposes its secret state solely on
// an authenticated loopback control route so the harness can scan outputs.
package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mlnomadpy/dacli/internal/cloudsync"
	"github.com/mlnomadpy/dacli/internal/ulid"
)

const signingKeyID = "qualification-server-signing-key"

type identity struct {
	tenant      string
	account     string
	device      string
	deviceCode  string
	access      string
	refresh     string
	deviceKeyID string
	deviceKey   ed25519.PublicKey
	allSecrets  []string
	oldAccess   []string
	revoked     bool
	refreshes   int
	revokes     int
	oldDenied   int
	lastExpiry  time.Time
}

type provider struct {
	mu                       sync.Mutex
	accessTTL                time.Duration
	controlKey               []byte
	publicKey                string
	privateKey               ed25519.PrivateKey
	identities               map[string]*identity
	deviceCodes              map[string]*identity
	bindings                 map[string]cloudsync.ProjectBindingResponse
	offline                  bool
	dropNextResponse         bool
	tamperNextAcknowledgment bool
	bindingDrift             bool
	syncStats                syncStats
	accepted                 map[string]cloudsync.EnvelopeAcknowledgment
	sequences                map[string]uint64
	inboundProposals         map[string]cloudsync.Envelope
	eventIDs                 map[string]cloudsync.EnvelopeAcknowledgment
}

type syncStats struct {
	Requests         int                   `json:"requests"`
	Accepted         int                   `json:"accepted"`
	Duplicates       int                   `json:"duplicates"`
	Rejected         int                   `json:"rejected"`
	EmptyExchanges   int                   `json:"empty_exchanges"`
	TamperedAcks     int                   `json:"tampered_acks"`
	InboundDelivered int                   `json:"inbound_delivered"`
	ReceivedIDs      []string              `json:"received_ids"`
	ReceivedByRoute  map[string][]string   `json:"received_by_route"`
	LastRoutes       []cloudsync.SyncRoute `json:"last_routes"`
	Registrations    []map[string]any      `json:"registrations"`
}

func main() {
	accessTTL := flag.Duration("access-ttl", 2*time.Second, "synthetic access-token lifetime (whole seconds, 1s through 5m)")
	flag.Parse()
	if flag.NArg() != 0 || !validAccessTTL(*accessTTL) {
		fmt.Fprintln(os.Stderr, "qualification provider access TTL must be whole seconds from 1s through 5m")
		os.Exit(2)
	}
	control, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || len(control) < 33 || len(control) > 257 {
		fmt.Fprintln(os.Stderr, "qualification provider requires a private control key on stdin")
		os.Exit(2)
	}
	controlKey := []byte(strings.TrimSuffix(control, "\n"))
	public, private, err := ed25519KeyPair()
	if err != nil {
		fmt.Fprintln(os.Stderr, "qualification provider could not initialize signing key")
		os.Exit(1)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "qualification provider could not bind loopback")
		os.Exit(1)
	}
	p := &provider{
		controlKey: controlKey,
		accessTTL:  *accessTTL,
		publicKey:  public,
		privateKey: private,
		identities: map[string]*identity{}, deviceCodes: map[string]*identity{},
		bindings:         map[string]cloudsync.ProjectBindingResponse{},
		accepted:         map[string]cloudsync.EnvelopeAcknowledgment{},
		sequences:        map[string]uint64{},
		inboundProposals: map[string]cloudsync.Envelope{},
		eventIDs:         map[string]cloudsync.EnvelopeAcknowledgment{},
		syncStats:        syncStats{ReceivedByRoute: map[string][]string{}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/device", p.begin)
	mux.HandleFunc("POST /v1/auth/token", p.token)
	mux.HandleFunc("GET /v1/auth/whoami", p.whoami)
	mux.HandleFunc("POST /v1/auth/revoke", p.revoke)
	mux.HandleFunc("GET /v1/sync/binding", p.binding)
	mux.HandleFunc("POST /v1/sync/exchange", p.exchange)
	mux.HandleFunc("GET /__qualification/state", p.control(p.state))
	mux.HandleFunc("POST /__qualification/revoke-tenant", p.control(p.revokeTenant))
	mux.HandleFunc("POST /__qualification/probe-old-access", p.control(p.probeOldAccess))
	mux.HandleFunc("POST /__qualification/configure-bindings", p.control(p.configureBindings))
	mux.HandleFunc("POST /__qualification/set-offline", p.control(p.setOffline))
	mux.HandleFunc("POST /__qualification/drop-next-exchange-response", p.control(p.dropNextExchangeResponse))
	mux.HandleFunc("POST /__qualification/tamper-next-ack", p.control(p.tamperNextAck))
	mux.HandleFunc("POST /__qualification/set-binding-drift", p.control(p.setBindingDrift))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "qualification provider stopped unexpectedly")
			os.Exit(1)
		}
	}()
	fmt.Printf("http://%s\n", listener.Addr())
	select {}
}

// Keep generated private signing material inside this process only.
func ed25519KeyPair() (string, ed25519.PrivateKey, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, err
	}
	return base64.StdEncoding.EncodeToString(public), private, nil
}

func (p *provider) control(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("X-Qualification-Control"))
		if len(got) != len(p.controlKey) || subtle.ConstantTimeCompare(got, p.controlKey) != 1 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		next(w, r)
	}
}

func (p *provider) begin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TenantID  string `json:"tenant_id"`
		PublicKey string `json:"public_key"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil || !validTenant(request.TenantID) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	deviceKey, err := base64.StdEncoding.DecodeString(request.PublicKey)
	if err != nil || len(deviceKey) != ed25519.PublicKeySize {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.identities[request.TenantID] != nil {
		writeError(w, http.StatusConflict, "invalid_request")
		return
	}
	deviceCode, err := randomSecret("device-code-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "temporarily_unavailable")
		return
	}
	access, err := randomSecret("access-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "temporarily_unavailable")
		return
	}
	refresh, err := randomSecret("refresh-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "temporarily_unavailable")
		return
	}
	digest := sha256.Sum256(deviceKey)
	i := &identity{tenant: request.TenantID, account: "account-" + request.TenantID, device: "device-" + request.TenantID, deviceCode: deviceCode, access: access, refresh: refresh, deviceKeyID: "qualification-device-" + hex.EncodeToString(digest[:]), deviceKey: append(ed25519.PublicKey(nil), deviceKey...), allSecrets: []string{deviceCode, access, refresh}}
	p.identities[i.tenant] = i
	p.deviceCodes[deviceCode] = i
	writeJSON(w, map[string]any{
		"device_code": deviceCode, "user_code": "QUALIFY-" + strings.ToUpper(request.TenantID),
		"verification_uri": "http://127.0.0.1/qualification", "expires_in": 120,
		"interval": 1, "server_time": time.Now().UTC(),
	})
}

func (p *provider) token(w http.ResponseWriter, r *http.Request) {
	var request struct {
		GrantType    string `json:"grant_type"`
		DeviceCode   string `json:"device_code"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var i *identity
	switch request.GrantType {
	case "urn:ietf:params:oauth:grant-type:device_code":
		i = p.deviceCodes[request.DeviceCode]
		if i == nil || i.revoked || i.deviceCode != request.DeviceCode {
			writeError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		i.deviceCode = ""
	case "refresh_token":
		for _, candidate := range p.identities {
			if candidate.refresh == request.RefreshToken {
				i = candidate
				break
			}
		}
		if i == nil || i.revoked {
			writeError(w, http.StatusUnauthorized, "invalid_grant")
			return
		}
		newAccess, err := randomSecret("access-")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "temporarily_unavailable")
			return
		}
		newRefresh, err := randomSecret("refresh-")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "temporarily_unavailable")
			return
		}
		i.oldAccess = append(i.oldAccess, i.access)
		i.access, i.refresh = newAccess, newRefresh
		i.allSecrets = append(i.allSecrets, newAccess, newRefresh)
		i.refreshes++
	case "qualification_disable_auth":
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	default:
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	now := time.Now().UTC()
	// Short expiry makes the artifact's refresh branch deterministic without
	// waiting for production-duration credentials.
	i.lastExpiry = now.Add(p.accessTTL)
	writeJSON(w, p.tokenDocument(i, now, int64(p.accessTTL/time.Second)))
}

func (p *provider) whoami(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, i := range p.identities {
		if r.Header.Get("Authorization") == "Bearer "+i.access {
			if i.revoked || time.Now().After(i.lastExpiry) {
				writeError(w, http.StatusUnauthorized, "invalid_grant")
				return
			}
			now := time.Now().UTC()
			writeJSON(w, map[string]any{
				"tenant_id": i.tenant, "account_id": i.account, "device_id": i.device,
				"scope": "qualification", "expires_at": i.lastExpiry, "server_time": now,
				"signing_key_id": i.deviceKeyID, "server_signing_keys": []map[string]string{{"key_id": signingKeyID, "public_key": p.publicKey}},
			})
			return
		}
		for _, old := range i.oldAccess {
			if r.Header.Get("Authorization") == "Bearer "+old {
				i.oldDenied++
				writeError(w, http.StatusUnauthorized, "invalid_grant")
				return
			}
		}
	}
	writeError(w, http.StatusUnauthorized, "invalid_grant")
}

func (p *provider) revoke(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, i := range p.identities {
		if request.Token == i.refresh && r.Header.Get("Authorization") == "Bearer "+i.access {
			i.revoked = true
			i.revokes++
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeError(w, http.StatusUnauthorized, "invalid_grant")
}

func (p *provider) revokeTenant(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Tenant string `json:"tenant"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.identities[request.Tenant]
	if i == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	i.revoked = true
	writeJSON(w, map[string]bool{"revoked": true})
}

func (p *provider) probeOldAccess(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Tenant string `json:"tenant"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	i := p.identities[request.Tenant]
	if i == nil || len(i.oldAccess) == 0 {
		p.mu.Unlock()
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	oldAccess := append([]string(nil), i.oldAccess...)
	p.mu.Unlock()
	// Exercise the actual whoami handler with each superseded bearer held only
	// in process memory; neither token crosses the control API.
	for _, old := range oldAccess {
		probe := httptest.NewRequest(http.MethodGet, "/v1/auth/whoami", nil)
		probe.Header.Set("Authorization", "Bearer "+old)
		response := httptest.NewRecorder()
		p.whoami(response, probe)
		if response.Code != http.StatusUnauthorized {
			writeJSON(w, map[string]bool{"rejected": false})
			return
		}
	}
	writeJSON(w, map[string]bool{"rejected": true})
}

func routeKey(route cloudsync.SyncRoute) string {
	return route.WorkspaceID + "\x00" + route.TenantID + "\x00" + route.ProjectID + "\x00" + route.EnvironmentID + "\x00" + route.RepositoryID + "\x00" + route.DeviceID
}

func (p *provider) bearerIdentity(r *http.Request) (*identity, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || len(header) <= len("Bearer ") {
		return nil, false
	}
	for _, candidate := range p.identities {
		if header == "Bearer "+candidate.access {
			return candidate, !candidate.revoked
		}
	}
	return nil, false
}

func (p *provider) configureBindings(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Bindings []cloudsync.ProjectBindingResponse `json:"bindings"`
	}
	if decodeClosedBody(r, &request, 65536) != nil || len(request.Bindings) != 2 {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	next := map[string]cloudsync.ProjectBindingResponse{}
	for _, binding := range request.Bindings {
		if binding.SchemaVersion != 1 || binding.BindingVersion != 1 || binding.Route.TenantID == "" || binding.Route.ProjectID == "" || binding.Route.DeviceID == "" || binding.Route.WorkspaceID == "" || binding.Route.EnvironmentID == "" || binding.Route.RepositoryID == "" || binding.RepositoryURL != "https://example.invalid/dacli-qualification" || binding.DefaultBranch != "main" || binding.MinimumSchemaVersion != 1 || binding.MaximumSchemaVersion != 1 || len(binding.TrustedKeys) != 1 || binding.TrustedKeys[signingKeyID] != p.publicKey {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		id := p.identities[binding.Route.TenantID]
		if id == nil || id.device != binding.Route.DeviceID || next[routeKey(binding.Route)].Route.ProjectID != "" {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		next[routeKey(binding.Route)] = binding
	}
	p.bindings = next
	writeJSON(w, map[string]bool{"configured": true})
}

func (p *provider) setOffline(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Offline bool `json:"offline"`
	}
	if decodeClosedBody(r, &request, 8192) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	p.offline = request.Offline
	p.mu.Unlock()
	writeJSON(w, map[string]bool{"offline": request.Offline})
}

func (p *provider) dropNextExchangeResponse(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Drop bool `json:"drop"`
	}
	if decodeClosedBody(r, &request, 8192) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	p.dropNextResponse = request.Drop
	p.mu.Unlock()
	writeJSON(w, map[string]bool{"drop_next_response": request.Drop})
}

func (p *provider) tamperNextAck(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Tamper bool `json:"tamper"`
	}
	if decodeClosedBody(r, &request, 8192) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	p.tamperNextAcknowledgment = request.Tamper
	p.mu.Unlock()
	writeJSON(w, map[string]bool{"tamper_next_ack": request.Tamper})
}

func (p *provider) setBindingDrift(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Drift bool `json:"drift"`
	}
	if decodeClosedBody(r, &request, 8192) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	p.bindingDrift = request.Drift
	p.mu.Unlock()
	writeJSON(w, map[string]bool{"binding_drift": request.Drift})
}

func (p *provider) binding(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	if len(values) != 6 {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	route := cloudsync.SyncRoute{WorkspaceID: values.Get("workspace_id"), TenantID: values.Get("tenant_id"), ProjectID: values.Get("project_id"), EnvironmentID: values.Get("environment_id"), RepositoryID: values.Get("repository_id"), DeviceID: values.Get("device_id")}
	if values.Get("workspace_id") == "" || values.Get("tenant_id") == "" || values.Get("project_id") == "" || values.Get("environment_id") == "" || values.Get("repository_id") == "" || values.Get("device_id") == "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	id, authorized := p.bearerIdentity(r)
	if !authorized || id == nil || id.device != route.DeviceID || id.tenant != route.TenantID {
		writeError(w, http.StatusUnauthorized, "invalid_grant")
		return
	}
	if time.Now().After(id.lastExpiry) {
		writeError(w, http.StatusUnauthorized, "invalid_grant")
		return
	}
	binding, ok := p.bindings[routeKey(route)]
	if !ok {
		writeError(w, http.StatusForbidden, "binding_refused")
		return
	}
	if p.bindingDrift {
		binding.BindingVersion++
	}
	writeJSON(w, binding)
}

func (p *provider) exchange(w http.ResponseWriter, r *http.Request) {
	var request cloudsync.ExchangeRequest
	if decodeClosedBody(r, &request, 1<<20) != nil || request.SchemaVersion != 1 || len(request.Envelopes) > 100 || len(request.DeliveryCursor) > 8192 {
		p.rejectExchange(w)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.syncStats.Requests++
	id, authorized := p.bearerIdentity(r)
	if !authorized || id == nil || id.tenant != request.Route.TenantID || id.device != request.Route.DeviceID {
		p.syncStats.Rejected++
		writeError(w, http.StatusUnauthorized, "invalid_grant")
		return
	}
	key := routeKey(request.Route)
	binding, ok := p.bindings[key]
	if !ok || binding.BindingVersion != request.BindingVersion {
		p.syncStats.Rejected++
		writeError(w, http.StatusForbidden, "binding_refused")
		return
	}
	deliveryCursor := fmt.Sprintf("qualification:%s:1", request.Route.ProjectID)
	if request.DeliveryCursor != "" && request.DeliveryCursor != deliveryCursor {
		p.syncStats.Rejected++
		writeError(w, http.StatusForbidden, "delivery_cursor_refused")
		return
	}
	if p.offline {
		writeError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	if time.Now().After(id.lastExpiry) {
		p.syncStats.Rejected++
		writeError(w, http.StatusUnauthorized, "invalid_grant")
		return
	}
	deviceKeys := map[string]ed25519.PublicKey{id.deviceKeyID: id.deviceKey}
	acks := make([]cloudsync.EnvelopeAcknowledgment, 0, len(request.Envelopes))
	for _, envelope := range request.Envelopes {
		if envelope.SchemaVersion != 1 || envelope.TenantID != request.Route.TenantID || envelope.ProjectID != request.Route.ProjectID || envelope.ProducerSequence == 0 || !validULID(envelope.EventID) || envelope.IdempotencyKey == "" || envelope.Integrity.KeyID != id.deviceKeyID || !cloudsync.Verify(envelope, deviceKeys) || cloudsync.ValidatePayload(envelope.EventType, envelope.Payload) != nil {
			p.syncStats.Rejected++
			writeError(w, http.StatusBadRequest, "invalid_envelope")
			return
		}
		ledgerKey := key + "\x00" + envelope.IdempotencyKey
		if prior, duplicate := p.accepted[ledgerKey]; duplicate {
			if prior.EventID != envelope.EventID || prior.Sequence != envelope.ProducerSequence {
				p.syncStats.Rejected++
				writeError(w, http.StatusConflict, "idempotency_conflict")
				return
			}
			prior.Outcome = cloudsync.OutcomeDuplicate
			acks = append(acks, prior)
			p.syncStats.Duplicates++
		} else {
			if _, exists := p.eventIDs[key+"\x00"+envelope.EventID]; exists || envelope.ProducerSequence != p.sequences[key]+1 {
				p.syncStats.Rejected++
				writeError(w, http.StatusConflict, "sequence_conflict")
				return
			}
			ack := cloudsync.EnvelopeAcknowledgment{EventID: envelope.EventID, IdempotencyKey: envelope.IdempotencyKey, Sequence: envelope.ProducerSequence, Outcome: cloudsync.OutcomeAccepted}
			p.accepted[ledgerKey] = ack
			p.eventIDs[key+"\x00"+envelope.EventID] = ack
			p.sequences[key] = envelope.ProducerSequence
			p.syncStats.Accepted++
			p.syncStats.ReceivedIDs = append(p.syncStats.ReceivedIDs, envelope.EventID)
			p.syncStats.ReceivedByRoute[key] = append(p.syncStats.ReceivedByRoute[key], envelope.EventID)
			if envelope.EventType == "project_registration" {
				var registration struct {
					RegistrationID string `json:"registration_id"`
					State          string `json:"state"`
					Version        uint64 `json:"version"`
				}
				if json.Unmarshal(envelope.Payload, &registration) != nil {
					p.syncStats.Rejected++
					writeError(w, http.StatusBadRequest, "invalid_registration")
					return
				}
				p.syncStats.Registrations = append(p.syncStats.Registrations, map[string]any{"project_id": request.Route.ProjectID, "registration_id": registration.RegistrationID, "state": registration.State, "version": registration.Version})
			}
			acks = append(acks, ack)
		}
	}
	p.syncStats.LastRoutes = append(p.syncStats.LastRoutes, request.Route)
	if p.dropNextResponse {
		p.dropNextResponse = false
		writeError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	if p.tamperNextAcknowledgment && len(acks) > 0 {
		acks[0].EventID = ulid.New()
		p.tamperNextAcknowledgment = false
		p.syncStats.TamperedAcks++
	}
	inbound := []cloudsync.Envelope{}
	if request.DeliveryCursor != deliveryCursor {
		proposal, exists := p.inboundProposals[key]
		if !exists {
			var err error
			proposal, err = p.proposal(request.Route)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "temporarily_unavailable")
				return
			}
			p.inboundProposals[key] = proposal
		}
		inbound = append(inbound, proposal)
		p.syncStats.InboundDelivered++
	}
	if len(request.Envelopes) == 0 {
		p.syncStats.EmptyExchanges++
	}
	writeJSON(w, cloudsync.ExchangeResponse{SchemaVersion: 1, Acknowledged: acks, Inbound: inbound, DeliveryCursor: deliveryCursor})
}

func (p *provider) proposal(route cloudsync.SyncRoute) (cloudsync.Envelope, error) {
	payload, err := json.Marshal(map[string]any{"proposal_id": ulid.New(), "title": "qualification server proposal", "acceptance": []string{"inspect the proposal only"}, "base_version": 0, "proposed_by": "qualification-server"})
	if err != nil {
		return cloudsync.Envelope{}, err
	}
	envelope := cloudsync.Envelope{SchemaVersion: 1, EventType: "task_proposal", TenantID: route.TenantID, ProjectID: route.ProjectID, EventID: ulid.New(), ProducerSequence: 1, OccurredAt: time.Now().UTC(), IdempotencyKey: "qualification-inbound-" + route.ProjectID, ProducerVersion: "qualification-provider", Payload: payload, Integrity: cloudsync.Integrity{Algorithm: "Ed25519", KeyID: signingKeyID}}
	if err := cloudsync.Sign(&envelope, p.privateKey); err != nil {
		return cloudsync.Envelope{}, err
	}
	return envelope, nil
}

func validULID(value string) bool {
	if len(value) != 26 || value[0] > '7' {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", char) {
			return false
		}
	}
	return true
}

func decodeClosedBody(r *http.Request, target any, limit int64) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return fmt.Errorf("request body exceeds bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing request data")
	}
	return nil
}

func (p *provider) rejectExchange(w http.ResponseWriter) {
	p.mu.Lock()
	p.syncStats.Rejected++
	p.mu.Unlock()
	writeError(w, http.StatusBadRequest, "invalid_request")
}

func (p *provider) state(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]any{}
	for tenant, i := range p.identities {
		out[tenant] = map[string]any{
			"account": i.account, "device": i.device, "server_public_key": p.publicKey, "device_signing_key_id": i.deviceKeyID, "refreshes": i.refreshes,
			"revokes": i.revokes, "revoked": i.revoked, "old_access_rejected": i.oldDenied,
			"secrets_for_in_memory_scan": append([]string(nil), i.allSecrets...),
		}
	}
	p.syncStats.ReceivedIDs = append([]string(nil), p.syncStats.ReceivedIDs...)
	p.syncStats.LastRoutes = append([]cloudsync.SyncRoute(nil), p.syncStats.LastRoutes...)
	receivedByRoute := make(map[string][]string, len(p.syncStats.ReceivedByRoute))
	for key, ids := range p.syncStats.ReceivedByRoute {
		receivedByRoute[key] = append([]string(nil), ids...)
	}
	p.syncStats.ReceivedByRoute = receivedByRoute
	out["sync"] = p.syncStats
	writeJSON(w, out)
}

func (p *provider) tokenDocument(i *identity, now time.Time, expires int64) map[string]any {
	return map[string]any{
		"token_type": "Bearer", "access_token": i.access, "refresh_token": i.refresh,
		"expires_in": expires, "server_time": now, "tenant_id": i.tenant,
		"account_id": i.account, "device_id": i.device, "scope": "qualification",
		"signing_key_id":      i.deviceKeyID,
		"server_signing_keys": []map[string]string{{"key_id": signingKeyID, "public_key": p.publicKey}},
	}
}

func validAccessTTL(value time.Duration) bool {
	return value >= time.Second && value <= 5*time.Minute && value%time.Second == 0
}

func validTenant(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
			return false
		}
	}
	return true
}

func randomSecret(prefix string) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSONStatus(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, value any) { writeJSONStatus(w, http.StatusOK, value) }

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, "qualification provider response write failed")
	}
}
