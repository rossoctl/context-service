package contextresource

import (
	"context"
	"errors"
	"time"
)

var (
	ErrAlreadyExists = errors.New("context resource already exists")
	ErrNotFound      = errors.New("context resource not found")
	ErrInvalid       = errors.New("invalid context resource")
	ErrForbidden     = errors.New("context access denied")
	ErrInUse         = errors.New("context resource is in use")
	ErrUnsupported   = errors.New("context lifecycle operation is not supported by this storage backend")
)

type CreateRequest struct {
	Name      string  `json:"name"`
	Namespace string  `json:"namespace"`
	Type      string  `json:"type"`
	Storage   Storage `json:"storage"`
	Owner     Subject `json:"-"`
}

type Storage struct {
	Backend      string `json:"backend"`
	Size         string `json:"size"`
	AccessMode   string `json:"accessMode"`
	StorageClass string `json:"storageClass,omitempty"`
}

type Attachment struct {
	Kind      string `json:"kind"`
	ClaimName string `json:"claimName"`
}

type Resource struct {
	Name            string       `json:"name"`
	Namespace       string       `json:"namespace"`
	Type            string       `json:"type"`
	Status          string       `json:"status"`
	Storage         Storage      `json:"storage"`
	Attachment      Attachment   `json:"attachment"`
	CurrentRevision string       `json:"currentRevision,omitempty"`
	Revisions       []Revision   `json:"revisions,omitempty"`
	EffectiveAccess []Permission `json:"effectiveAccess,omitempty"`
	Consumers       []Consumer   `json:"consumers,omitempty"`
}

type Permission string

const (
	PermissionRead       Permission = "read"
	PermissionWrite      Permission = "write"
	PermissionAttach     Permission = "attach"
	PermissionDerive     Permission = "derive"
	PermissionAdminister Permission = "administer"
)

type Subject struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type Grant struct {
	Subject     Subject      `json:"subject"`
	Permissions []Permission `json:"permissions"`
	CreatedAt   time.Time    `json:"createdAt"`
}

type GrantList struct {
	Items []Grant `json:"items"`
}

type Consumer struct {
	Subject    Subject   `json:"subject"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	AccessMode string    `json:"accessMode"`
	Active     bool      `json:"active"`
	Desired    bool      `json:"desired"`
	Since      time.Time `json:"since,omitempty"`
}

type ConsumerList struct {
	Items []Consumer `json:"items"`
}

type AuditEvent struct {
	Time    time.Time `json:"time"`
	Action  string    `json:"action"`
	Subject Subject   `json:"subject"`
	Target  *Subject  `json:"target,omitempty"`
}

type AuditList struct {
	Items []AuditEvent `json:"items"`
}

type SourceReference struct {
	Context  string `json:"context"`
	Type     string `json:"type"`
	Revision string `json:"revision"`
}

type Revision struct {
	ID         string            `json:"id"`
	CreatedAt  time.Time         `json:"createdAt"`
	Operation  string            `json:"operation"`
	Producer   string            `json:"producer,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
	Sources    []SourceReference `json:"sources,omitempty"`
	Files      int               `json:"files"`
	Bytes      int64             `json:"bytes"`
}

type RevisionList struct {
	Items []Revision `json:"items"`
}

type SnapshotRequest struct {
	Name          string            `json:"name"`
	SnapshotClass string            `json:"snapshotClass,omitempty"`
	Protected     bool              `json:"protected,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type Snapshot struct {
	Name            string            `json:"name"`
	ContextName     string            `json:"contextName"`
	Namespace       string            `json:"namespace"`
	Revision        string            `json:"revision,omitempty"`
	Status          string            `json:"status"`
	SnapshotName    string            `json:"snapshotName"`
	SnapshotClass   string            `json:"snapshotClass,omitempty"`
	SourceClaimName string            `json:"sourceClaimName"`
	CreatedAt       time.Time         `json:"createdAt,omitempty"`
	Protected       bool              `json:"protected,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Error           string            `json:"error,omitempty"`
}

type SnapshotList struct {
	Items []Snapshot `json:"items"`
}

type CloneRequest struct {
	Name     string  `json:"name"`
	Snapshot string  `json:"snapshot"`
	Owner    Subject `json:"-"`
}

type RestoreRequest struct {
	Snapshot string `json:"snapshot"`
}

type RetentionRequest struct {
	KeepLast int    `json:"keepLast,omitempty"`
	MaxAge   string `json:"maxAge,omitempty"`
}

type GarbageCollection struct {
	DryRun  bool       `json:"dryRun"`
	Deleted []Snapshot `json:"deleted,omitempty"`
	Kept    []Snapshot `json:"kept,omitempty"`
}

type LifecycleCapabilities struct {
	Snapshots bool   `json:"snapshots"`
	Clones    bool   `json:"clones"`
	Restore   bool   `json:"restore"`
	Driver    string `json:"driver,omitempty"`
	Class     string `json:"snapshotClass,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type QueryRecord struct {
	ID       string          `json:"id"`
	Context  string          `json:"context"`
	Type     string          `json:"type"`
	Revision string          `json:"revision"`
	Title    string          `json:"title,omitempty"`
	Text     string          `json:"text"`
	Keywords []string        `json:"keywords,omitempty"`
	Source   SourceReference `json:"source"`
	File     string          `json:"file,omitempty"`
}

type QueryIndex struct {
	Revision string        `json:"revision"`
	Records  []QueryRecord `json:"records"`
}

type QueryRequest struct {
	Contexts       []string `json:"contexts,omitempty"`
	Types          []string `json:"types,omitempty"`
	SourceContexts []string `json:"sourceContexts,omitempty"`
	Revisions      []string `json:"revisions,omitempty"`
	IDs            []string `json:"ids,omitempty"`
	Query          string   `json:"query,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Cursor         string   `json:"cursor,omitempty"`
}

type QueryResult struct {
	Score  float64     `json:"score,omitempty"`
	Record QueryRecord `json:"record"`
}

type QueryResponse struct {
	Items       []QueryResult `json:"items"`
	NextCursor  string        `json:"nextCursor,omitempty"`
	Unavailable []string      `json:"unavailable,omitempty"`
}

type List struct {
	Items []Resource `json:"items"`
}

type Manager interface {
	CreateContext(context.Context, CreateRequest) (Resource, error)
	ListContexts(context.Context, string) ([]Resource, error)
	GetContext(context.Context, string, string) (Resource, error)
	DeleteContext(context.Context, string, string) error
	PublishContextRevision(context.Context, string, string, Revision) (Resource, error)
	ListAccessibleContexts(context.Context, string, Subject) ([]Resource, error)
	AccessContext(context.Context, string, string, Subject, Permission) (Resource, error)
	ListContextGrants(context.Context, string, string) ([]Grant, error)
	SetContextGrant(context.Context, string, string, Grant, Subject) (Resource, error)
	RevokeContextGrant(context.Context, string, string, Subject, Subject) (Resource, error)
	ListContextConsumers(context.Context, string, string) ([]Consumer, error)
	SetContextConsumer(context.Context, string, string, Consumer, bool, Subject) (Resource, error)
	ListContextAudit(context.Context, string, string) ([]AuditEvent, error)
	ForceDeleteContext(context.Context, string, string) error
	CreateContextSnapshot(context.Context, string, string, SnapshotRequest) (Snapshot, error)
	ListContextSnapshots(context.Context, string, string) ([]Snapshot, error)
	GetContextSnapshot(context.Context, string, string, string) (Snapshot, error)
	CloneContextSnapshot(context.Context, string, string, CloneRequest) (Resource, error)
	RestoreContextSnapshot(context.Context, string, string, RestoreRequest) (Resource, error)
	SetContextRetention(context.Context, string, string, RetentionRequest) (Resource, error)
	GarbageCollectContextSnapshots(context.Context, string, string, bool) (GarbageCollection, error)
	ContextLifecycleCapabilities(context.Context, string, string) (LifecycleCapabilities, error)
	PublishContextQueryIndex(context.Context, string, string, QueryIndex) error
	QueryContexts(context.Context, string, Subject, QueryRequest) (QueryResponse, error)
}
