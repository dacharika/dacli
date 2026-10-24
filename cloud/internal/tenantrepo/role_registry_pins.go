package tenantrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
)

// SetRolePin appends a compare-and-swap exact release selection for a tenant-owned scope.
func (r *Repository) SetRolePin(ctx context.Context, identity tenant.VerifiedIdentity, mutation tenant.Mutation, target tenant.PinTarget, roleID, catalogID, version, contentDigest string, expectedGeneration uint64, reason string) (tenant.RolePin, error) {
	var pin tenant.RolePin
	if err := tenant.ValidateVerifiedIdentity(identity); err != nil {
		return pin, err
	}
	if err := validateMutation(mutation); err != nil {
		return pin, err
	}
	if mutation.Actor != identity.Account || mutation.Device != identity.Device || target.Tenant != identity.Scope.Organization {
		return pin, errors.New("role pin target or actor does not match verified identity")
	}
	if _, err := tenant.NewProjectID(roleID); err != nil {
		return pin, errors.New("role pin requires a valid role id")
	}
	if reason == "" || len(reason) > 1024 || reason != strings.TrimSpace(reason) {
		return pin, errors.New("role pin reason must be non-empty, trimmed, and at most 1024 bytes")
	}
	returnPin := pin
	err := r.withTenant(ctx, identity.Scope, false, func(tx *sql.Tx) error {
		permission := permissionForPin(target.Kind)
		membership, _, err := r.lockRoleActor(ctx, tx, identity, mutation, permission)
		if err != nil {
			return err
		}
		if err := validatePinTarget(ctx, tx, identity.Scope.Organization, membership, target); err != nil {
			return err
		}
		if err := lockRole(ctx, tx, identity.Scope.Organization, catalogID, roleID); err != nil {
			return err
		}
		lockKey := fmt.Sprintf("hosted-pin:%s:%s:%s:%s", target.Tenant, target.Kind, target.ScopeID, roleID)
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,678))`, lockKey); err != nil {
			return fmt.Errorf("lock role pin generation: %w", err)
		}
		var currentGeneration, currentSequence uint64
		var currentCatalog, currentVersion, currentDigest string
		err = tx.QueryRowContext(ctx, `SELECT generation,sequence,catalog_id,version,content_digest
FROM controlplane_role_pins WHERE tenant_id=$1 AND scope_kind=$2 AND scope_id=$3 AND role_id=$4
ORDER BY generation DESC LIMIT 1 FOR UPDATE`, identity.Scope.Organization, target.Kind, target.ScopeID, roleID).
			Scan(&currentGeneration, &currentSequence, &currentCatalog, &currentVersion, &currentDigest)
		if errors.Is(err, sql.ErrNoRows) {
			currentGeneration, currentSequence = 0, 0
		} else if err != nil {
			return fmt.Errorf("read current role pin generation: %w", err)
		}
		if expectedGeneration != currentGeneration {
			return ErrConflict
		}
		catalog, err := loadRoleCatalog(ctx, tx, identity.Scope.Organization, catalogID, true)
		if err != nil {
			return err
		}
		if err := authorizeCatalogRead(catalog, membership); err != nil {
			return err
		}
		if catalog.Team != "" && (membership.Team != catalog.Team && !roleHasPermission(membership, tenant.PermissionOrganizationManage)) {
			return tenant.ErrDenied
		}
		releaseKey := tenant.ExactReleaseKey{Tenant: identity.Scope.Organization, CatalogID: catalogID, RoleID: roleID, Version: version, ContentDigest: contentDigest}
		release, err := loadHostedRelease(ctx, tx, releaseKey)
		if err != nil {
			return err
		}
		publishers, err := loadRolePublishers(ctx, tx, releaseKey.Tenant, releaseKey.CatalogID)
		if err != nil {
			return err
		}
		if _, err := tenant.ValidateHostedRelease(catalog, release, publishers); err != nil {
			return err
		}
		history, err := readRoleLifecycleHistory(ctx, tx, releaseKey.Tenant, releaseKey.CatalogID, releaseKey.RoleID)
		if err != nil {
			return err
		}
		if len(history) > maxRoleExportEntries-1 {
			return errors.New("hosted role lifecycle history exceeds its 4096 item bound")
		}
		if err := validateExportLifecycleHistory(history, publishers); err != nil {
			return err
		}
		var lifecycle *tenant.HostedLifecycleEntry
		for index := range history {
			if history[index].Key == releaseKey {
				entry := history[index]
				lifecycle = &entry
			}
		}
		now, err := refreshRoleRegistryTime(ctx, tx, membership)
		if err != nil {
			return err
		}
		if lifecycle == nil || (lifecycle.State != tenant.HostedReleased && lifecycle.State != tenant.HostedDeprecated) || lifecycle.Status == nil || lifecycle.Status.State != "active" ||
			lifecycle.Status.RoleID != releaseKey.RoleID || lifecycle.Status.Version != releaseKey.Version || lifecycle.Status.ContentDigest != releaseKey.ContentDigest || lifecycle.Status.Verify(publishers, now) != nil {
			return fmt.Errorf("%w: role pin requires a fresh active signed lifecycle statement for the exact release", tenant.ErrInvalidRoleRegistry)
		}
		previousRelease := (*tenant.ExactReleaseKey)(nil)
		if currentGeneration > 0 {
			key := tenant.ExactReleaseKey{Tenant: identity.Scope.Organization, CatalogID: currentCatalog, RoleID: roleID, Version: currentVersion, ContentDigest: currentDigest}
			previousRelease = &key
		}
		pin = tenant.RolePin{Tenant: identity.Scope.Organization, Target: target, RoleID: roleID, CatalogID: catalogID, Version: version, ContentDigest: contentDigest,
			Sequence: currentSequence + 1, Generation: currentGeneration + 1, Previous: previousRelease, Actor: identity.Account, Reason: reason, PinnedAt: now}
		if err := tenant.ValidateRolePin(identity.Scope, catalog, pin, release); err != nil {
			return err
		}
		now, err = refreshRoleRegistryTime(ctx, tx, membership)
		if err != nil {
			return err
		}
		if lifecycle == nil || lifecycle.Status == nil || lifecycle.Status.Verify(publishers, now) != nil {
			return fmt.Errorf("%w: role pin lifecycle statement expired before the pin write", tenant.ErrInvalidRoleRegistry)
		}
		pin.PinnedAt = now
		var prevCatalog, prevVersion, prevDigest any
		if previousRelease != nil {
			prevCatalog, prevVersion, prevDigest = previousRelease.CatalogID, previousRelease.Version, previousRelease.ContentDigest
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO controlplane_role_pins
(tenant_id,scope_kind,scope_id,organization_id,team_id,project_id,role_id,sequence,generation,catalog_id,version,content_digest,previous_catalog_id,previous_version,previous_digest,actor_id,reason,pinned_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, pin.Tenant, pin.Target.Kind, pin.Target.ScopeID,
			nullString(string(pin.Target.OrganizationID)), nullString(string(pin.Target.TeamID)), nullString(string(pin.Target.ProjectID)), pin.RoleID, pin.Sequence, pin.Generation,
			pin.CatalogID, pin.Version, pin.ContentDigest, prevCatalog, prevVersion, prevDigest, pin.Actor, pin.Reason, pin.PinnedAt)
		if err != nil {
			return fmt.Errorf("append exact role pin: %w", classifyWrite(err))
		}
		if err := appendRoleAudit(ctx, tx, pin.Tenant, mutation, now, "pin.set", pin.Target.ScopeID+":"+roleID, digest(pin), reason); err != nil {
			return err
		}
		returnPin = pin
		return nil
	})
	return returnPin, err
}

// ResolveRole follows explicit project, current-team, then organization pins.
func (r *Repository) ResolveRole(ctx context.Context, identity tenant.VerifiedIdentity, projectID tenant.ProjectID, roleID string) (tenant.ResolvedRole, error) {
	var result tenant.ResolvedRole
	if err := tenant.ValidateVerifiedIdentity(identity); err != nil {
		return result, err
	}
	if _, err := tenant.NewProjectID(roleID); err != nil {
		return result, errors.New("role resolution requires a valid role id")
	}
	if projectID != "" {
		if _, err := tenant.NewProjectID(string(projectID)); err != nil {
			return result, errors.New("role resolution project id is invalid")
		}
	}
	err := r.withTenantReadSnapshot(ctx, identity.Scope, func(tx *sql.Tx) error {
		membership, _, err := loadRoleActor(ctx, tx, identity, nil, tenant.PermissionOrganizationRead, true)
		if err != nil {
			return err
		}
		if projectID != "" {
			if !roleHasPermission(membership, tenant.PermissionProjectRead) {
				return tenant.ErrDenied
			}
			var state tenant.Lifecycle
			err := tx.QueryRowContext(ctx, `SELECT state FROM controlplane_projects WHERE tenant_id=$1 AND project_id=$2 FOR SHARE`, identity.Scope.Organization, projectID).Scan(&state)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("read role resolution project: %w", err)
			}
			if state != tenant.LifecycleActive {
				return tenant.ErrDenied
			}
		}
		scope := tenant.PinScope{Tenant: identity.Scope.Organization, OrganizationID: identity.Scope.Organization, TeamID: membership.Team, ProjectID: projectID}
		pins, err := readRolePinHeads(ctx, tx, identity.Scope.Organization, scope, roleID)
		if err != nil {
			return err
		}
		if len(pins) == 0 {
			return fmt.Errorf("%w: no explicit pin for role", tenant.ErrInvalidRoleRegistry)
		}
		// The first row is the highest-priority explicit target. Only that catalog's
		// trust namespace participates: publisher key IDs are catalog-local.
		winner := pins[0]
		catalog, err := loadRoleCatalog(ctx, tx, winner.Tenant, winner.CatalogID, true)
		if err != nil {
			return err
		}
		if err := authorizeCatalogRead(catalog, membership); err != nil {
			return err
		}
		if catalog.Team != "" && catalog.Team != membership.Team && !roleHasPermission(membership, tenant.PermissionOrganizationManage) {
			return tenant.ErrDenied
		}
		if catalog.Team != "" {
			if err := ensureActiveTeam(ctx, tx, catalog.Tenant, catalog.Team, true); err != nil {
				return err
			}
		}
		key := tenant.ExactReleaseKey{Tenant: winner.Tenant, CatalogID: winner.CatalogID, RoleID: roleID, Version: winner.Version, ContentDigest: winner.ContentDigest}
		release, err := loadHostedRelease(ctx, tx, key)
		if err != nil {
			return err
		}
		publishers, err := loadRolePublishers(ctx, tx, winner.Tenant, winner.CatalogID)
		if err != nil {
			return err
		}
		history, err := readRoleLifecycleHistory(ctx, tx, winner.Tenant, winner.CatalogID, roleID)
		if err != nil {
			return err
		}
		if len(history) > maxRoleExportEntries-1 {
			return errors.New("hosted role lifecycle history exceeds its 4096 item bound")
		}
		if err := validateExportLifecycleHistory(history, publishers); err != nil {
			return err
		}
		releases := map[tenant.ExactReleaseKey]tenant.HostedRelease{key: release}
		lifecycle := make(map[tenant.ExactReleaseKey]tenant.HostedLifecycleEntry, 1)
		for _, entry := range history {
			if entry.Key == key {
				lifecycle[key] = entry
			}
		}
		result, err = tenant.ResolveRole(scope, roleID, []tenant.RolePin{winner}, releases, lifecycle, publishers)
		if err != nil {
			return err
		}
		selectedKey := tenant.ExactReleaseKey{Tenant: result.Pin.Tenant, CatalogID: result.Pin.CatalogID, RoleID: roleID, Version: result.Pin.Version, ContentDigest: result.Pin.ContentDigest}
		selectedEntry, ok := lifecycle[selectedKey]
		if !ok || selectedEntry.Status == nil || (selectedEntry.State != tenant.HostedReleased && selectedEntry.State != tenant.HostedDeprecated) || selectedEntry.Status.State != "active" {
			return fmt.Errorf("%w: selected exact release has no active hosted lifecycle state", tenant.ErrInvalidRoleRegistry)
		}
		if selectedEntry.Key != selectedKey || selectedEntry.Status.RoleID != selectedKey.RoleID || selectedEntry.Status.Version != selectedKey.Version || selectedEntry.Status.ContentDigest != selectedKey.ContentDigest {
			return fmt.Errorf("%w: selected exact release lifecycle statement identifies different content", tenant.ErrInvalidRoleRegistry)
		}
		now, err := refreshRoleRegistryTime(ctx, tx, membership)
		if err != nil {
			return err
		}
		if selectedEntry.Status.Verify(publishers, now) != nil {
			return fmt.Errorf("%w: selected exact release lifecycle statement is stale or invalid", tenant.ErrInvalidRoleRegistry)
		}
		return nil
	})
	return result, err
}

func permissionForPin(kind tenant.PinTargetKind) tenant.Permission {
	switch kind {
	case tenant.PinOrganization:
		return tenant.PermissionOrganizationManage
	case tenant.PinTeam:
		return tenant.PermissionTeamManage
	default:
		return tenant.PermissionProjectManage
	}
}

func validatePinTarget(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, membership tenant.Membership, target tenant.PinTarget) error {
	if target.Tenant != tenantID || target.ScopeID == "" {
		return errors.New("role pin target must be fully scoped to the verified tenant")
	}
	switch target.Kind {
	case tenant.PinOrganization:
		if target.ScopeID != string(tenantID) || target.OrganizationID != tenantID || target.TeamID != "" || target.ProjectID != "" {
			return errors.New("organization role pin target is malformed")
		}
		return nil
	case tenant.PinTeam:
		if target.ScopeID != string(target.TeamID) || target.TeamID == "" || target.OrganizationID != "" || target.ProjectID != "" {
			return errors.New("team role pin target is malformed")
		}
		if err := ensureActiveTeam(ctx, tx, tenantID, target.TeamID, true); err != nil {
			return err
		}
		if !roleHasPermission(membership, tenant.PermissionOrganizationManage) && (!roleHasPermission(membership, tenant.PermissionTeamManage) || membership.Team != target.TeamID) {
			return tenant.ErrDenied
		}
		return nil
	case tenant.PinProject:
		if target.ScopeID != string(target.ProjectID) || target.ProjectID == "" || target.OrganizationID != "" || target.TeamID != "" {
			return errors.New("project role pin target is malformed")
		}
		var state tenant.Lifecycle
		err := tx.QueryRowContext(ctx, `SELECT state FROM controlplane_projects WHERE tenant_id=$1 AND project_id=$2 FOR SHARE`, tenantID, target.ProjectID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read project for role pin: %w", err)
		}
		if state != tenant.LifecycleActive {
			return tenant.ErrDenied
		}
		return nil
	default:
		return errors.New("unknown role pin target kind")
	}
}

func readRolePinHeads(ctx context.Context, tx *sql.Tx, tenantID tenant.OrganizationID, scope tenant.PinScope, roleID string) ([]tenant.RolePin, error) {
	rows, err := tx.QueryContext(ctx, `SELECT scope_kind,scope_id,organization_id,team_id,project_id,catalog_id,version,content_digest,sequence,generation,
previous_catalog_id,previous_version,previous_digest,actor_id,reason,pinned_at
FROM controlplane_role_pins p WHERE tenant_id=$1 AND role_id=$2 AND (
 (scope_kind='organization' AND scope_id=$3) OR
 (scope_kind='team' AND $4<>'' AND scope_id=$4) OR
 (scope_kind='project' AND $5<>'' AND scope_id=$5))
AND generation=(SELECT max(p2.generation) FROM controlplane_role_pins p2 WHERE p2.tenant_id=p.tenant_id AND p2.scope_kind=p.scope_kind AND p2.scope_id=p.scope_id AND p2.role_id=p.role_id)
ORDER BY CASE scope_kind WHEN 'project' THEN 1 WHEN 'team' THEN 2 ELSE 3 END`, tenantID, roleID, scope.Tenant, scope.TeamID, scope.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("read inherited role pin heads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var pins []tenant.RolePin
	for rows.Next() {
		var pin tenant.RolePin
		var kind string
		var organization, team, project sql.NullString
		var previousCatalog, previousVersion, previousDigest sql.NullString
		pin.Tenant = tenantID
		if err := rows.Scan(&kind, &pin.Target.ScopeID, &organization, &team, &project, &pin.CatalogID, &pin.Version, &pin.ContentDigest, &pin.Sequence, &pin.Generation,
			&previousCatalog, &previousVersion, &previousDigest, &pin.Actor, &pin.Reason, &pin.PinnedAt); err != nil {
			return nil, fmt.Errorf("scan inherited role pin: %w", err)
		}
		pin.Target.Tenant, pin.Target.Kind = tenantID, tenant.PinTargetKind(kind)
		if organization.Valid {
			pin.Target.OrganizationID = tenant.OrganizationID(organization.String)
		}
		if team.Valid {
			pin.Target.TeamID = tenant.TeamID(team.String)
		}
		if project.Valid {
			pin.Target.ProjectID = tenant.ProjectID(project.String)
		}
		pin.RoleID = roleID
		if previousCatalog.Valid {
			if !previousVersion.Valid || !previousDigest.Valid {
				return nil, errors.New("stored role pin predecessor is incomplete")
			}
			pin.Previous = &tenant.ExactReleaseKey{Tenant: tenantID, CatalogID: previousCatalog.String, RoleID: roleID, Version: previousVersion.String, ContentDigest: previousDigest.String}
		} else if previousVersion.Valid || previousDigest.Valid {
			return nil, errors.New("stored role pin predecessor is inconsistent")
		}
		pins = append(pins, pin)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inherited role pin heads: %w", err)
	}
	return pins, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
