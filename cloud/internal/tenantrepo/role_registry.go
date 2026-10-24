package tenantrepo

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
)

const (
	maxRoleCatalogListEntries = 501
	maxRoleExportEntries      = 4097
	maxRolePublisherKeys      = 513
	maxRoleStatusBytes        = 64 << 10
)

// CreateRoleCatalog creates an organization-wide or team-owned hosted catalog.
func (r *Repository) CreateRoleCatalog(ctx context.Context, identity tenant.VerifiedIdentity, mutation tenant.Mutation, catalog tenant.HostedCatalog) error {
	if err := tenant.ValidateHostedCatalog(catalog); err != nil {
		return err
	}
	if err := validateIdentityMutation(identity, mutation, catalog.Tenant); err != nil {
		return err
	}
	return r.withTenant(ctx, identity.Scope, false, func(tx *sql.Tx) error {
		membership, _, err := r.lockRoleActor(ctx, tx, identity, mutation, permissionForCatalog(catalog))
		if err != nil {
			return err
		}
		if err := authorizeCatalogTeam(ctx, tx, catalog, membership); err != nil {
			return err
		}
		now, err := refreshRoleRegistryTime(ctx, tx, membership)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO controlplane_role_catalogs
(tenant_id,catalog_id,catalog_version,team_id,name,created_at)
VALUES ($1,$2,$3,NULLIF($4,''),$5,$6)`, catalog.Tenant, catalog.ID, catalog.Version, catalog.Team, catalog.Name, now)
		if err != nil {
			return fmt.Errorf("insert role catalog: %w", classifyWrite(err))
		}
		return appendRoleAudit(ctx, tx, identity.Scope.Organization, mutation, now, "catalog.create", catalog.ID, digest(catalog), "catalog created")
	})
}

// ListRoleCatalogs exposes only organization catalogs and the caller's current team catalogs.
func (r *Repository) ListRoleCatalogs(ctx context.Context, identity tenant.VerifiedIdentity) ([]tenant.HostedCatalog, error) {
	if err := tenant.ValidateVerifiedIdentity(identity); err != nil {
		return nil, err
	}
	var catalogs []tenant.HostedCatalog
	err := r.withTenant(ctx, identity.Scope, false, func(tx *sql.Tx) error {
		membership, _, err := loadRoleActor(ctx, tx, identity, nil, tenant.PermissionOrganizationRead, true)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT tenant_id,catalog_id,COALESCE(team_id,''),name,catalog_version
FROM controlplane_role_catalogs WHERE tenant_id=$1 AND (team_id IS NULL OR team_id=$2)
	ORDER BY catalog_id LIMIT $3`, identity.Scope.Organization, membership.Team, maxRoleCatalogListEntries)
		if err != nil {
			return fmt.Errorf("list role catalogs: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			if len(catalogs) == maxRoleCatalogListEntries-1 {
				return errors.New("hosted role catalog list exceeds its 500 item bound")
			}
			var value tenant.HostedCatalog
			if err := rows.Scan(&value.Tenant, &value.ID, &value.Team, &value.Name, &value.Version); err != nil {
				return fmt.Errorf("scan role catalog: %w", err)
			}
			catalogs = append(catalogs, value)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		_, err = refreshRoleRegistryTime(ctx, tx, membership)
		return err
	})
	return catalogs, err
}

// EnrollRolePublisher adds a publisher key only through an explicit catalog-scoped action.
func (r *Repository) EnrollRolePublisher(ctx context.Context, identity tenant.VerifiedIdentity, mutation tenant.Mutation, catalogID string, key tenant.HostedPublisherKey) error {
	if err := tenant.ValidateVerifiedIdentity(identity); err != nil {
		return err
	}
	if err := validateMutation(mutation); err != nil {
		return err
	}
	if mutation.Actor != identity.Account || mutation.Device != identity.Device || key.Tenant != identity.Scope.Organization || key.CatalogID != catalogID {
		return errors.New("publisher enrollment identity does not match the verified tenant mutation")
	}
	return r.withTenant(ctx, identity.Scope, false, func(tx *sql.Tx) error {
		catalog, err := loadRoleCatalog(ctx, tx, identity.Scope.Organization, catalogID, true)
		if err != nil {
			return err
		}
		membership, _, err := r.lockRoleActor(ctx, tx, identity, mutation, permissionForCatalog(catalog))
		if err != nil {
			return err
		}
		if err := authorizeCatalogTeam(ctx, tx, catalog, membership); err != nil {
			return err
		}
		now, err := refreshRoleRegistryTime(ctx, tx, membership)
		if err != nil {
			return err
		}
		key.EnrolledAt = now
		if err := tenant.ValidateHostedPublisherKey(catalog, key); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO controlplane_role_publishers
(tenant_id,catalog_id,key_id,public_key,enrolled_at) VALUES ($1,$2,$3,$4,$5)`, key.Tenant, key.CatalogID, key.KeyID, []byte(key.PublicKey), now)
		if err != nil {
			return fmt.Errorf("enroll role publisher: %w", classifyWrite(err))
		}
		return appendRoleAudit(ctx, tx, identity.Scope.Organization, mutation, now, "publisher.enroll", catalogID+":"+key.KeyID, sha256.Sum256(key.PublicKey), "publisher key explicitly enrolled")
	})
}

// ImportRoleRelease stores an exact signed envelope as a draft; imports never enroll trust.
func (r *Repository) ImportRoleRelease(ctx context.Context, identity tenant.VerifiedIdentity, mutation tenant.Mutation, catalogID string, release tenant.HostedRelease) (tenant.HostedLifecycleEntry, error) {
	var created tenant.HostedLifecycleEntry
	if err := tenant.ValidateVerifiedIdentity(identity); err != nil {
		return created, err
	}
	if err := validateMutation(mutation); err != nil {
		return created, err
	}
	if mutation.Actor != identity.Account || mutation.Device != identity.Device || release.Key.Tenant != identity.Scope.Organization || release.Key.CatalogID != catalogID {
		return created, errors.New("release import identity does not match the verified tenant mutation")
	}
	err := r.withTenant(ctx, identity.Scope, false, func(tx *sql.Tx) error {
		catalog, err := loadRoleCatalog(ctx, tx, identity.Scope.Organization, catalogID, true)
		if err != nil {
			return err
		}
		membership, _, err := r.lockRoleActor(ctx, tx, identity, mutation, permissionForCatalog(catalog))
		if err != nil {
			return err
		}
		if err := authorizeCatalogTeam(ctx, tx, catalog, membership); err != nil {
			return err
		}
		if len(release.Envelope) == 0 || len(release.Envelope) > tenant.MaxHostedRoleEnvelopeBytes {
			return errors.New("signed role release envelope exceeds the hosted size limit")
		}
		if err := lockRole(ctx, tx, identity.Scope.Organization, catalogID, release.Key.RoleID); err != nil {
			return err
		}
		publishers, err := loadRolePublishers(ctx, tx, identity.Scope.Organization, catalogID)
		if err != nil {
			return err
		}
		verified, err := tenant.ValidateHostedRelease(catalog, release, publishers)
		if err != nil {
			return err
		}
		var existingVersion int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM controlplane_role_releases
WHERE tenant_id=$1 AND catalog_id=$2 AND role_id=$3 AND version=$4 FOR UPDATE`, release.Key.Tenant, catalogID, release.Key.RoleID, release.Key.Version).Scan(&existingVersion)
		if err == nil {
			return ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check immutable role release: %w", err)
		}
		now, err := refreshRoleRegistryTime(ctx, tx, membership)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO controlplane_role_releases
(tenant_id,catalog_id,role_id,version,content_digest,release_bytes,publisher_key_id,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, release.Key.Tenant, catalogID, release.Key.RoleID, release.Key.Version, release.Key.ContentDigest, release.Envelope, verified.PublisherKeyID, now)
		if err != nil {
			return fmt.Errorf("insert immutable role release: %w", classifyWrite(err))
		}
		sequence, err := nextLifecycleSequence(ctx, tx, identity.Scope.Organization, catalogID, release.Key.RoleID)
		if err != nil {
			return err
		}
		created = tenant.HostedLifecycleEntry{Key: release.Key, Sequence: sequence, State: tenant.HostedDraft, Actor: identity.Account, Reason: "release imported as draft", RecordedAt: now}
		if err := insertRoleLifecycle(ctx, tx, created); err != nil {
			return err
		}
		return appendRoleAudit(ctx, tx, identity.Scope.Organization, mutation, now, "release.import", release.Key.RoleID+":"+release.Key.Version, digest(release.Key), "signed release imported as draft")
	})
	return created, err
}

// AppendRoleLifecycle advances one role-wide hosted sequence and the signed role status chain.
func (r *Repository) AppendRoleLifecycle(ctx context.Context, identity tenant.VerifiedIdentity, mutation tenant.Mutation, key tenant.ExactReleaseKey, expectedSequence uint64, state tenant.HostedLifecycleState, status *rolebundle.LifecycleStatus, reason string) (tenant.HostedLifecycleEntry, error) {
	var created tenant.HostedLifecycleEntry
	if err := tenant.ValidateVerifiedIdentity(identity); err != nil {
		return created, err
	}
	if err := validateMutation(mutation); err != nil {
		return created, err
	}
	if mutation.Actor != identity.Account || mutation.Device != identity.Device || key.Tenant != identity.Scope.Organization {
		return created, errors.New("lifecycle identity does not match the verified tenant mutation")
	}
	err := r.withTenant(ctx, identity.Scope, false, func(tx *sql.Tx) error {
		catalog, err := loadRoleCatalog(ctx, tx, identity.Scope.Organization, key.CatalogID, true)
		if err != nil {
			return err
		}
		membership, _, err := r.lockRoleActor(ctx, tx, identity, mutation, permissionForCatalog(catalog))
		if err != nil {
			return err
		}
		if err := authorizeCatalogTeam(ctx, tx, catalog, membership); err != nil {
			return err
		}
		if err := lockRole(ctx, tx, key.Tenant, key.CatalogID, key.RoleID); err != nil {
			return err
		}
		sequence, err := currentLifecycleSequence(ctx, tx, key.Tenant, key.CatalogID, key.RoleID)
		if err != nil {
			return err
		}
		if sequence != expectedSequence {
			return ErrConflict
		}
		catalogRelease, err := loadHostedRelease(ctx, tx, key)
		if err != nil {
			return err
		}
		publishers, err := loadRolePublishers(ctx, tx, key.Tenant, key.CatalogID)
		if err != nil {
			return err
		}
		previous, err := latestLifecycleForRelease(ctx, tx, key)
		if err != nil {
			return err
		}
		previousStatus, err := latestRoleStatus(ctx, tx, key.Tenant, key.CatalogID, key.RoleID, publishers)
		if err != nil {
			return err
		}
		if state != tenant.HostedDraft {
			if _, err := tenant.ValidateHostedRelease(catalog, catalogRelease, publishers); err != nil {
				return err
			}
		}
		now, err := refreshRoleRegistryTime(ctx, tx, membership)
		if err != nil {
			return err
		}
		created = tenant.HostedLifecycleEntry{Key: key, Sequence: sequence + 1, State: state, Status: status, Actor: identity.Account, Reason: reason, RecordedAt: now}
		if err := tenant.ValidateHostedLifecycleEntry(created, previous, previousStatus, publishers, now); err != nil {
			return err
		}
		if err := insertRoleLifecycle(ctx, tx, created); err != nil {
			return err
		}
		action := "lifecycle." + string(state)
		return appendRoleAudit(ctx, tx, key.Tenant, mutation, now, action, key.CatalogID+":"+key.RoleID, digest(created), reason)
	})
	return created, err
}

// ExportRole returns exact release bytes and full lifecycle history for one catalog role.
func (r *Repository) ExportRole(ctx context.Context, identity tenant.VerifiedIdentity, key tenant.ExactReleaseKey) (tenant.HostedRoleExport, error) {
	var result tenant.HostedRoleExport
	if err := tenant.ValidateVerifiedIdentity(identity); err != nil {
		return result, err
	}
	if key.Tenant != identity.Scope.Organization {
		return result, errors.New("role export tenant does not match verified identity")
	}
	err := r.withTenantReadSnapshot(ctx, identity.Scope, func(tx *sql.Tx) error {
		membership, _, err := loadRoleActor(ctx, tx, identity, nil, tenant.PermissionOrganizationRead, true)
		if err != nil {
			return err
		}
		catalog, err := loadRoleCatalog(ctx, tx, key.Tenant, key.CatalogID, true)
		if err != nil {
			return err
		}
		if err := authorizeCatalogRead(catalog, membership); err != nil {
			return err
		}
		if catalog.Team != "" {
			if err := ensureActiveTeam(ctx, tx, catalog.Tenant, catalog.Team, true); err != nil {
				return err
			}
		}
		selected, err := loadHostedRelease(ctx, tx, key)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT version,content_digest,release_bytes FROM controlplane_role_releases
WHERE tenant_id=$1 AND catalog_id=$2 AND role_id=$3 ORDER BY version LIMIT $4`, key.Tenant, key.CatalogID, key.RoleID, maxRoleExportEntries)
		if err != nil {
			return fmt.Errorf("read exported role releases: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var value tenant.HostedRelease
			value.Key.Tenant, value.Key.CatalogID, value.Key.RoleID = key.Tenant, key.CatalogID, key.RoleID
			if err := rows.Scan(&value.Key.Version, &value.Key.ContentDigest, &value.Envelope); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan exported role release: %w", err)
			}
			result.Releases = append(result.Releases, value)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate exported role releases: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close exported role releases: %w", err)
		}
		if len(result.Releases) > maxRoleExportEntries-1 {
			return errors.New("hosted role release history exceeds its 4096 item bound")
		}
		publishers, err := loadRolePublishers(ctx, tx, key.Tenant, key.CatalogID)
		if err != nil {
			return err
		}
		for _, release := range result.Releases {
			if _, err := tenant.ValidateHostedRelease(catalog, release, publishers); err != nil {
				return err
			}
		}
		result.Lifecycle, err = readRoleLifecycleHistory(ctx, tx, key.Tenant, key.CatalogID, key.RoleID)
		if err != nil {
			return err
		}
		if len(result.Lifecycle) > maxRoleExportEntries-1 {
			return errors.New("hosted role lifecycle history exceeds its 4096 item bound")
		}
		if err := validateExportLifecycleHistory(result.Lifecycle, publishers); err != nil {
			return err
		}
		if _, err := refreshRoleRegistryTime(ctx, tx, membership); err != nil {
			return err
		}
		result.Selected = selected.Key
		return nil
	})
	return result, err
}

func permissionForCatalog(catalog tenant.HostedCatalog) tenant.Permission {
	if catalog.Team == "" {
		return tenant.PermissionOrganizationManage
	}
	return tenant.PermissionTeamManage
}

func validateIdentityMutation(identity tenant.VerifiedIdentity, mutation tenant.Mutation, tenantID tenant.OrganizationID) error {
	if err := tenant.ValidateVerifiedIdentity(identity); err != nil {
		return err
	}
	if err := validateMutation(mutation); err != nil {
		return err
	}
	if tenantID != identity.Scope.Organization || mutation.Actor != identity.Account || mutation.Device != identity.Device {
		return errors.New("role registry mutation does not match verified tenant actor and device")
	}
	return nil
}

func (r *Repository) lockRoleActor(ctx context.Context, tx *sql.Tx, identity tenant.VerifiedIdentity, mutation tenant.Mutation, permission tenant.Permission) (tenant.Membership, time.Time, error) {
	if mutation.Actor != identity.Account || mutation.Device != identity.Device {
		return tenant.Membership{}, time.Time{}, errors.New("role registry mutation actor or device differs from verified identity")
	}
	membership, now, err := loadRoleActor(ctx, tx, identity, &mutation, permission, true)
	return membership, now, err
}

func loadRoleActor(ctx context.Context, tx *sql.Tx, identity tenant.VerifiedIdentity, mutation *tenant.Mutation, permission tenant.Permission, lock bool) (tenant.Membership, time.Time, error) {
	var membership tenant.Membership
	var now time.Time
	if err := ensureActiveRoleIdentity(ctx, tx, identity.Scope.Organization, identity.Account, lock); err != nil {
		return membership, now, err
	}
	query := `SELECT tenant_id,account_id,COALESCE(team_id,''),roles,state,version,expires_unix
FROM controlplane_memberships WHERE tenant_id=$1 AND account_id=$2`
	if lock {
		query += ` FOR SHARE`
	}
	var roleBytes []byte
	err := tx.QueryRowContext(ctx, query, identity.Scope.Organization, identity.Account).Scan(&membership.Tenant, &membership.Account, &membership.Team, &roleBytes, &membership.State, &membership.Version, &membership.ExpiresUnix)
	if errors.Is(err, sql.ErrNoRows) {
		return membership, now, ErrNotFound
	}
	if err != nil {
		return membership, now, fmt.Errorf("reload current role registry membership: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return membership, now, fmt.Errorf("read server role registry time: %w", err)
	}
	if membership.Version != identity.MembershipVersion || membership.State != tenant.LifecycleActive || (membership.ExpiresUnix != 0 && membership.ExpiresUnix <= now.Unix()) {
		return membership, now, tenant.ErrDenied
	}
	var names []string
	if err := json.Unmarshal(roleBytes, &names); err != nil {
		return membership, now, fmt.Errorf("decode current role registry membership: %w", err)
	}
	roles := make([]tenant.Role, 0, len(names))
	for _, name := range names {
		role, err := tenant.ParseRole(name)
		if err != nil {
			return membership, now, fmt.Errorf("decode current role registry permissions: %w", err)
		}
		roles = append(roles, role)
	}
	membership.Roles, err = tenant.Roles(roles...)
	if err != nil || tenant.ValidateMembership(identity.Scope, membership) != nil {
		return membership, now, tenant.ErrDenied
	}
	if membership.Team != "" {
		if err := ensureActiveTeam(ctx, tx, identity.Scope.Organization, membership.Team, lock); err != nil {
			return membership, now, err
		}
	}
	if !roleHasPermission(membership, permission) {
		return membership, now, tenant.ErrDenied
	}
	if mutation != nil && (mutation.Actor != identity.Account || mutation.Device != identity.Device) {
		return membership, now, tenant.ErrDenied
	}
	if identity.Device != "" {
		if err := lockRoleDevice(ctx, tx, identity.Scope.Organization, identity.Account, identity.Device, lock); err != nil {
			return membership, now, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return membership, now, fmt.Errorf("recheck server role registry time: %w", err)
		}
		if membership.ExpiresUnix != 0 && membership.ExpiresUnix <= now.Unix() {
			return membership, now, tenant.ErrDenied
		}
	}
	return membership, now, nil
}

func refreshRoleRegistryTime(ctx context.Context, tx *sql.Tx, membership tenant.Membership) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return now, fmt.Errorf("refresh server role registry time: %w", err)
	}
	if membership.ExpiresUnix != 0 && membership.ExpiresUnix <= now.Unix() {
		return now, tenant.ErrDenied
	}
	return now, nil
}

// withTenantReadSnapshot keeps bounded multi-query exports on one database snapshot.
func (r *Repository) withTenantReadSnapshot(ctx context.Context, scope tenant.Scope, operation func(*sql.Tx) error) error {
	if r == nil || r.db == nil {
		return errors.New("tenant repository is not open")
	}
	if _, err := tenant.NewScope(scope.Organization); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("begin role export snapshot: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('dacli.tenant_id', $1, true)`, scope.Organization); err != nil {
		return fmt.Errorf("bind role export tenant transaction: %w", err)
	}
	if err := operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit role export snapshot: %w", err)
	}
	committed = true
	return nil
}

func ensureActiveRoleIdentity(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, account tenant.AccountID, lock bool) error {
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	var organizationState tenant.Lifecycle
	err := tx.QueryRowContext(ctx, `SELECT state FROM controlplane_organizations WHERE tenant_id=$1 AND organization_id=$1`+suffix, tenantID).Scan(&organizationState)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("reload current role registry organization: %w", err)
	}
	var accountState tenant.Lifecycle
	err = tx.QueryRowContext(ctx, `SELECT state FROM controlplane_accounts WHERE account_id=$1`+suffix, account).Scan(&accountState)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("reload current role registry account: %w", err)
	}
	if organizationState != tenant.LifecycleActive || accountState != tenant.LifecycleActive {
		return tenant.ErrDenied
	}
	return nil
}

func roleHasPermission(membership tenant.Membership, permission tenant.Permission) bool {
	for _, role := range membership.Roles.Roles() {
		if tenant.Allows(role, permission) {
			return true
		}
	}
	return false
}

func authorizeCatalogTeam(ctx context.Context, tx *sql.Tx, catalog tenant.HostedCatalog, membership tenant.Membership) error {
	if catalog.Team == "" {
		if !roleHasPermission(membership, tenant.PermissionOrganizationManage) {
			return tenant.ErrDenied
		}
		return nil
	}
	if err := ensureActiveTeam(ctx, tx, catalog.Tenant, catalog.Team, true); err != nil {
		return err
	}
	if roleHasPermission(membership, tenant.PermissionOrganizationManage) {
		return nil
	}
	if !roleHasPermission(membership, tenant.PermissionTeamManage) || membership.Team != catalog.Team {
		return tenant.ErrDenied
	}
	return nil
}

func authorizeCatalogRead(catalog tenant.HostedCatalog, membership tenant.Membership) error {
	if catalog.Team == "" || roleHasPermission(membership, tenant.PermissionOrganizationManage) || membership.Team == catalog.Team {
		return nil
	}
	return tenant.ErrDenied
}

func ensureActiveTeam(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, teamID tenant.TeamID, lock bool) error {
	query := `SELECT state FROM controlplane_teams WHERE tenant_id=$1 AND team_id=$2`
	if lock {
		query += ` FOR SHARE`
	}
	var state tenant.Lifecycle
	err := tx.QueryRowContext(ctx, query, tenantID, teamID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read role catalog team: %w", err)
	}
	if state != tenant.LifecycleActive {
		return tenant.ErrDenied
	}
	return nil
}

func lockRoleDevice(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, account tenant.AccountID, device tenant.DeviceID, lock bool) error {
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	var state tenant.Lifecycle
	var owner tenant.AccountID
	err := tx.QueryRowContext(ctx, `SELECT account_id,state FROM controlplane_devices
WHERE tenant_id=$1 AND device_id=$2`+suffix, tenantID, device).Scan(&owner, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return tenant.ErrDenied
	}
	if err != nil {
		return fmt.Errorf("reload verified role registry device: %w", err)
	}
	if owner != account || state != tenant.LifecycleActive {
		return tenant.ErrDenied
	}
	return nil
}

func loadRoleCatalog(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, catalogID string, lock bool) (tenant.HostedCatalog, error) {
	var catalog tenant.HostedCatalog
	query := `SELECT tenant_id,catalog_id,COALESCE(team_id,''),name,catalog_version FROM controlplane_role_catalogs WHERE tenant_id=$1 AND catalog_id=$2`
	if lock {
		query += ` FOR SHARE`
	}
	err := tx.QueryRowContext(ctx, query, tenantID, catalogID).Scan(&catalog.Tenant, &catalog.ID, &catalog.Team, &catalog.Name, &catalog.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog, ErrNotFound
	}
	if err != nil {
		return catalog, fmt.Errorf("read hosted role catalog: %w", err)
	}
	if err := tenant.ValidateHostedCatalog(catalog); err != nil {
		return catalog, err
	}
	return catalog, nil
}

func loadRolePublishers(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, catalogID string) (map[string]ed25519.PublicKey, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key_id,public_key FROM controlplane_role_publishers WHERE tenant_id=$1 AND catalog_id=$2 ORDER BY key_id LIMIT $3`, tenantID, catalogID, maxRolePublisherKeys)
	if err != nil {
		return nil, fmt.Errorf("read enrolled role publishers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	keys := make(map[string]ed25519.PublicKey)
	for rows.Next() {
		if len(keys) == maxRolePublisherKeys-1 {
			return nil, errors.New("hosted role publisher set exceeds its 512 key bound")
		}
		var id string
		var key []byte
		if err := rows.Scan(&id, &key); err != nil {
			return nil, fmt.Errorf("scan enrolled role publisher: %w", err)
		}
		if len(key) != ed25519.PublicKeySize {
			return nil, errors.New("stored role publisher key has an invalid length")
		}
		keys[id] = ed25519.PublicKey(append([]byte(nil), key...))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enrolled role publishers: %w", err)
	}
	return keys, nil
}

func lockRole(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, catalogID, roleID string) error {
	key := "hosted-role:" + string(tenantID) + ":" + catalogID + ":" + roleID
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,677))`, key); err != nil {
		return fmt.Errorf("lock hosted role history: %w", err)
	}
	return nil
}

func loadHostedRelease(ctx context.Context, tx *sql.Tx, key tenant.ExactReleaseKey) (tenant.HostedRelease, error) {
	var release tenant.HostedRelease
	release.Key = key
	err := tx.QueryRowContext(ctx, `SELECT release_bytes FROM controlplane_role_releases
WHERE tenant_id=$1 AND catalog_id=$2 AND role_id=$3 AND version=$4 AND content_digest=$5`, key.Tenant, key.CatalogID, key.RoleID, key.Version, key.ContentDigest).Scan(&release.Envelope)
	if errors.Is(err, sql.ErrNoRows) {
		return release, ErrNotFound
	}
	if err != nil {
		return release, fmt.Errorf("read exact hosted role release: %w", err)
	}
	if len(release.Envelope) == 0 || len(release.Envelope) > tenant.MaxHostedRoleEnvelopeBytes {
		return release, errors.New("stored role release envelope exceeds the hosted size limit")
	}
	return release, nil
}

func currentLifecycleSequence(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, catalogID, roleID string) (uint64, error) {
	var sequence uint64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM controlplane_role_lifecycle
WHERE tenant_id=$1 AND catalog_id=$2 AND role_id=$3`, tenantID, catalogID, roleID).Scan(&sequence)
	if err != nil {
		return 0, fmt.Errorf("read hosted role lifecycle head: %w", err)
	}
	return sequence, nil
}

func nextLifecycleSequence(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, catalogID, roleID string) (uint64, error) {
	sequence, err := currentLifecycleSequence(ctx, tx, tenantID, catalogID, roleID)
	return sequence + 1, err
}

func latestLifecycleForRelease(ctx context.Context, tx *sql.Tx, key tenant.ExactReleaseKey) (*tenant.HostedLifecycleEntry, error) {
	var entry tenant.HostedLifecycleEntry
	var rawStatus, rawDigest []byte
	err := tx.QueryRowContext(ctx, `SELECT sequence,hosted_state,signed_status_bytes,signed_status_digest,actor_id,reason,recorded_at
FROM controlplane_role_lifecycle WHERE tenant_id=$1 AND catalog_id=$2 AND role_id=$3 AND version=$4 AND content_digest=$5
ORDER BY sequence DESC LIMIT 1`, key.Tenant, key.CatalogID, key.RoleID, key.Version, key.ContentDigest).Scan(&entry.Sequence, &entry.State, &rawStatus, &rawDigest, &entry.Actor, &entry.Reason, &entry.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read exact role lifecycle head: %w", err)
	}
	entry.Key = key
	if len(rawStatus) == 0 && len(rawDigest) != 0 {
		return nil, errors.New("stored role lifecycle digest has no signed status bytes")
	}
	if len(rawStatus) != 0 {
		if len(rawStatus) > maxRoleStatusBytes {
			return nil, errors.New("stored role lifecycle status exceeds its 64 KiB bound")
		}
		var status rolebundle.LifecycleStatus
		if err := json.Unmarshal(rawStatus, &status); err != nil {
			return nil, fmt.Errorf("decode exact role lifecycle status: %w", err)
		}
		if err := verifyStoredLifecycleDigest(status, rawDigest); err != nil {
			return nil, err
		}
		entry.Status = &status
	}
	return &entry, nil
}

func latestRoleStatus(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, catalogID, roleID string, publishers map[string]ed25519.PublicKey) (*rolebundle.LifecycleStatus, error) {
	var raw, storedDigest []byte
	err := tx.QueryRowContext(ctx, `SELECT signed_status_bytes,signed_status_digest FROM controlplane_role_lifecycle
WHERE tenant_id=$1 AND catalog_id=$2 AND role_id=$3 AND signed_status_bytes IS NOT NULL
ORDER BY sequence DESC LIMIT 1`, tenantID, catalogID, roleID).Scan(&raw, &storedDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read role-wide signed status head: %w", err)
	}
	if len(raw) > maxRoleStatusBytes {
		return nil, errors.New("stored role lifecycle status exceeds its 64 KiB bound")
	}
	var status rolebundle.LifecycleStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, fmt.Errorf("decode role-wide signed status: %w", err)
	}
	if err := verifyStoredLifecycleDigest(status, storedDigest); err != nil {
		return nil, err
	}
	if err := status.Verify(publishers, status.IssuedAt); err != nil {
		return nil, fmt.Errorf("verify historical role-wide status at issue time: %w", err)
	}
	return &status, nil
}

func insertRoleLifecycle(ctx context.Context, tx *sql.Tx, entry tenant.HostedLifecycleEntry) error {
	var raw []byte
	var statusDigest any
	if entry.Status != nil {
		var err error
		raw, err = json.Marshal(entry.Status)
		if err != nil {
			return fmt.Errorf("encode signed role lifecycle status: %w", err)
		}
		digest, err := rolebundle.LifecycleDigest(*entry.Status)
		if err != nil {
			return fmt.Errorf("digest signed role lifecycle status: %w", err)
		}
		statusDigest = digest
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO controlplane_role_lifecycle
(tenant_id,catalog_id,role_id,version,content_digest,sequence,hosted_state,signed_status_bytes,signed_status_digest,actor_id,reason,recorded_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, entry.Key.Tenant, entry.Key.CatalogID, entry.Key.RoleID, entry.Key.Version, entry.Key.ContentDigest, entry.Sequence, entry.State, nullableBytes(raw), statusDigest, entry.Actor, entry.Reason, entry.RecordedAt)
	if err != nil {
		return fmt.Errorf("append hosted role lifecycle: %w", classifyWrite(err))
	}
	return nil
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func readRoleLifecycleHistory(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, catalogID, roleID string) ([]tenant.HostedLifecycleEntry, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version,content_digest,sequence,hosted_state,signed_status_bytes,signed_status_digest,actor_id,reason,recorded_at
FROM controlplane_role_lifecycle WHERE tenant_id=$1 AND catalog_id=$2 AND role_id=$3 ORDER BY sequence LIMIT $4`, tenantID, catalogID, roleID, maxRoleExportEntries)
	if err != nil {
		return nil, fmt.Errorf("read exported role lifecycle history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var entries []tenant.HostedLifecycleEntry
	for rows.Next() {
		var entry tenant.HostedLifecycleEntry
		entry.Key.Tenant, entry.Key.CatalogID, entry.Key.RoleID = tenantID, catalogID, roleID
		var raw, storedDigest []byte
		if err := rows.Scan(&entry.Key.Version, &entry.Key.ContentDigest, &entry.Sequence, &entry.State, &raw, &storedDigest, &entry.Actor, &entry.Reason, &entry.RecordedAt); err != nil {
			return nil, fmt.Errorf("scan exported role lifecycle entry: %w", err)
		}
		if len(raw) != 0 {
			if len(raw) > maxRoleStatusBytes {
				return nil, errors.New("exported role lifecycle status exceeds its 64 KiB bound")
			}
			var status rolebundle.LifecycleStatus
			if err := json.Unmarshal(raw, &status); err != nil {
				return nil, fmt.Errorf("decode exported role lifecycle status: %w", err)
			}
			if err := verifyStoredLifecycleDigest(status, storedDigest); err != nil {
				return nil, err
			}
			entry.Status = &status
		} else if len(storedDigest) != 0 {
			return nil, errors.New("exported role lifecycle digest has no signed status bytes")
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate exported role lifecycle history: %w", err)
	}
	return entries, nil
}

func verifyStoredLifecycleDigest(status rolebundle.LifecycleStatus, stored []byte) error {
	digest, err := rolebundle.LifecycleDigest(status)
	if err != nil || string(stored) != digest {
		return errors.New("stored role lifecycle status digest does not match its signed bytes")
	}
	return nil
}

func validateExportLifecycleHistory(entries []tenant.HostedLifecycleEntry, publishers map[string]ed25519.PublicKey) error {
	var previousStatus *rolebundle.LifecycleStatus
	previousByRelease := make(map[tenant.ExactReleaseKey]*tenant.HostedLifecycleEntry)
	for index := range entries {
		entry := entries[index]
		if entry.Sequence != uint64(index+1) {
			return errors.New("stored hosted role lifecycle sequence has a gap")
		}
		previous := previousByRelease[entry.Key]
		if previous == nil && entry.State != tenant.HostedDraft {
			return errors.New("exported exact release lifecycle does not begin with its draft import")
		}
		if entry.Status != nil {
			if err := entry.Status.Verify(publishers, entry.Status.IssuedAt); err != nil {
				return fmt.Errorf("verify exported historical lifecycle signature at issue time: %w", err)
			}
			if entry.Status.RoleID != entry.Key.RoleID || entry.Status.Version != entry.Key.Version || entry.Status.ContentDigest != entry.Key.ContentDigest {
				return errors.New("exported lifecycle status does not match its exact release")
			}
			wantGeneration := uint64(1)
			wantPrevious := ""
			if previousStatus != nil {
				wantGeneration = previousStatus.Generation + 1
				var err error
				wantPrevious, err = rolebundle.LifecycleDigest(*previousStatus)
				if err != nil {
					return fmt.Errorf("digest exported previous lifecycle status: %w", err)
				}
			}
			if entry.Status.Generation != wantGeneration || entry.Status.PreviousStatusDigest != wantPrevious {
				return errors.New("exported signed lifecycle history has a generation or predecessor gap")
			}
			previousStatus = entry.Status
		}
		if previous != nil && !validHostedLifecycleTransition(previous.State, entry.State) {
			return errors.New("exported exact release lifecycle transition is invalid")
		}
		if entry.State == tenant.HostedRevoked && (entry.Status == nil || entry.Status.State != "revoked") || entry.State != tenant.HostedRevoked && entry.State != tenant.HostedDraft && (entry.Status == nil || entry.Status.State != "active") {
			return errors.New("exported hosted state and signed status disagree")
		}
		copy := entry
		previousByRelease[entry.Key] = &copy
	}
	return nil
}

func validHostedLifecycleTransition(from, to tenant.HostedLifecycleState) bool {
	switch from {
	case tenant.HostedDraft:
		return to == tenant.HostedDraft || to == tenant.HostedReleased || to == tenant.HostedRevoked
	case tenant.HostedReleased:
		return to == tenant.HostedReleased || to == tenant.HostedDeprecated || to == tenant.HostedRevoked
	case tenant.HostedDeprecated:
		return to == tenant.HostedDeprecated || to == tenant.HostedReleased || to == tenant.HostedRevoked
	default:
		return false
	}
}

func appendRoleAudit(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, mutation tenant.Mutation, now time.Time, action, target string, actionDigest [32]byte, reason string) error {
	if len(action) == 0 || len(action) > 128 || len(target) == 0 || len(target) > 512 || len(reason) == 0 || len(reason) > 1024 {
		return errors.New("role audit action, target, or reason exceeds its bounded field")
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO controlplane_role_audit_events
(tenant_id,correlation_id,actor_id,actor_device_id,action,target_id,action_digest,reason,occurred_at)
VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9) ON CONFLICT (tenant_id,correlation_id) DO NOTHING`, tenantID, mutation.CorrelationID, mutation.Actor, mutation.Device, action, target, actionDigest[:], reason, now)
	if err != nil {
		return fmt.Errorf("append role registry audit event: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read role registry audit result: %w", err)
	}
	if inserted == 1 {
		return nil
	}
	return ErrAuditConflict
}
