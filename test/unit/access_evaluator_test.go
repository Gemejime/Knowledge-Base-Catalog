package unit_test

import (
	"errors"
	"testing"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
)

func TestAccessEvaluatorRBACAndTenantIsolation(t *testing.T) {
	allActions := []policy.Action{
		policy.ActionCreateKB,
		policy.ActionUpdateKB,
		policy.ActionGetKB,
		policy.ActionListKBs,
		policy.ActionDeleteKB,
		policy.ActionTriggerReindexing,
		policy.ActionPromotePointer,
		policy.ActionAbortReindexing,
	}

	t.Run("Owner and Editor are permitted all administrative and read actions within their tenant", func(t *testing.T) {
		for _, role := range []policy.Role{policy.RoleOwner, policy.RoleEditor} {
			caller := policy.CallerIdentity{
				TenantID: "tenant-1",
				UserID:   "user-123",
				Roles:    []policy.Role{role},
			}
			for _, act := range allActions {
				if err := policy.EvaluateAccess(caller, "tenant-1", act); err != nil {
					t.Fatalf("expected role %s to be allowed action %s, got %v", role, act, err)
				}
			}
		}
	})

	t.Run("Viewer and Auditor are permitted read actions but rejected on mutation and cutover actions", func(t *testing.T) {
		for _, role := range []policy.Role{policy.RoleViewer, policy.RoleAuditor} {
			caller := policy.CallerIdentity{
				TenantID: "tenant-1",
				UserID:   "user-readonly",
				Roles:    []policy.Role{role},
			}
			for _, readAct := range []policy.Action{policy.ActionGetKB, policy.ActionListKBs} {
				if err := policy.EvaluateAccess(caller, "tenant-1", readAct); err != nil {
					t.Fatalf("expected role %s to be allowed read action %s, got %v", role, readAct, err)
				}
			}
			for _, writeAct := range []policy.Action{
				policy.ActionCreateKB,
				policy.ActionUpdateKB,
				policy.ActionDeleteKB,
				policy.ActionTriggerReindexing,
				policy.ActionPromotePointer,
				policy.ActionAbortReindexing,
			} {
				err := policy.EvaluateAccess(caller, "tenant-1", writeAct)
				if !errors.Is(err, entity.ErrInsufficientPermissions) {
					t.Fatalf("expected ErrInsufficientPermissions for role %s on %s, got %v", role, writeAct, err)
				}
			}
		}
	})

	t.Run("Cross-tenant access is strictly rejected with ErrTenantMismatch", func(t *testing.T) {
		caller := policy.CallerIdentity{
			TenantID: "tenant-1",
			UserID:   "user-owner",
			Roles:    []policy.Role{policy.RoleOwner},
		}
		err := policy.EvaluateAccess(caller, "tenant-2", policy.ActionGetKB)
		if !errors.Is(err, entity.ErrTenantMismatch) {
			t.Fatalf("expected ErrTenantMismatch on cross-tenant access, got %v", err)
		}
	})

	t.Run("Missing tenant_id or user_id is rejected", func(t *testing.T) {
		noTenant := policy.CallerIdentity{TenantID: "", UserID: "user-1", Roles: []policy.Role{policy.RoleOwner}}
		if err := policy.EvaluateAccess(noTenant, "", policy.ActionGetKB); !errors.Is(err, entity.ErrInsufficientPermissions) {
			t.Fatalf("expected ErrInsufficientPermissions for empty tenant_id, got %v", err)
		}
		noUser := policy.CallerIdentity{TenantID: "tenant-1", UserID: "", Roles: []policy.Role{policy.RoleOwner}}
		if err := policy.EvaluateAccess(noUser, "tenant-1", policy.ActionGetKB); !errors.Is(err, entity.ErrInsufficientPermissions) {
			t.Fatalf("expected ErrInsufficientPermissions for empty user_id, got %v", err)
		}
	})
}
