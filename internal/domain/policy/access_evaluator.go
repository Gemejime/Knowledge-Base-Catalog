package policy

import (
	"fmt"
	"strings"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
)

// Role represents a coarse entity-level RBAC role propagated in caller JWT claims.
type Role string

const (
	RoleOwner   Role = "Owner"
	RoleEditor  Role = "Editor"
	RoleViewer  Role = "Viewer"
	RoleAuditor Role = "Auditor"
)

// Action represents an administrative or discovery operation on the catalog control plane.
type Action string

const (
	ActionCreateKB          Action = "CreateKnowledgeBase"
	ActionUpdateKB          Action = "UpdateKnowledgeBase"
	ActionGetKB             Action = "GetKnowledgeBase"
	ActionListKBs           Action = "ListKnowledgeBases"
	ActionDeleteKB          Action = "DeleteKnowledgeBase"
	ActionTriggerReindexing Action = "TriggerReindexing"
	ActionPromotePointer    Action = "PromoteIndexPointer"
	ActionAbortReindexing   Action = "AbortReindexing"
)

// CallerIdentity holds the verified caller context extracted from Edge Gateway JWT claims.
type CallerIdentity struct {
	TenantID  string
	UserID    string
	Roles     []Role
	RequestID string
}

// EvaluateAccess validates tenant isolation, user identity presence, and coarse RBAC permissions:
//   - Read (GetKnowledgeBase, ListKnowledgeBases): Owner, Editor, Viewer, Auditor
//   - Mutate / Cutover / Delete (Create, Update, TriggerReindexing, PromoteIndexPointer, AbortReindexing, Delete): Owner, Editor
func EvaluateAccess(caller CallerIdentity, targetTenantID string, action Action) error {
	callerTenant := strings.TrimSpace(caller.TenantID)
	if callerTenant == "" {
		return fmt.Errorf("%w: missing verified tenant_id in caller context", entity.ErrInsufficientPermissions)
	}
	if strings.TrimSpace(caller.UserID) == "" {
		return fmt.Errorf("%w: missing verified user_id (sub) in caller context", entity.ErrInsufficientPermissions)
	}
	targetTenant := strings.TrimSpace(targetTenantID)
	if targetTenant != "" && targetTenant != callerTenant {
		return fmt.Errorf("%w: caller tenant %q cannot access tenant %q", entity.ErrTenantMismatch, callerTenant, targetTenant)
	}

	for _, rawRole := range caller.Roles {
		role := normalizeRole(string(rawRole))
		if isRoleAllowed(role, action) {
			return nil
		}
	}

	return fmt.Errorf("%w: caller roles %v are not permitted to perform %s", entity.ErrInsufficientPermissions, caller.Roles, action)
}

func normalizeRole(r string) Role {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "owner", "admin":
		return RoleOwner
	case "editor", "writer":
		return RoleEditor
	case "viewer", "reader":
		return RoleViewer
	case "auditor":
		return RoleAuditor
	default:
		return Role(r)
	}
}

func isRoleAllowed(role Role, action Action) bool {
	switch action {
	case ActionGetKB, ActionListKBs:
		return role == RoleOwner || role == RoleEditor || role == RoleViewer || role == RoleAuditor
	case ActionCreateKB, ActionUpdateKB, ActionTriggerReindexing, ActionPromotePointer, ActionAbortReindexing, ActionDeleteKB:
		return role == RoleOwner || role == RoleEditor
	default:
		return false
	}
}
