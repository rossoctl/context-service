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
)

type CreateRequest struct {
	Name      string  `json:"name"`
	Namespace string  `json:"namespace"`
	Type      string  `json:"type"`
	Storage   Storage `json:"storage"`
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
	Name            string     `json:"name"`
	Namespace       string     `json:"namespace"`
	Type            string     `json:"type"`
	Status          string     `json:"status"`
	Storage         Storage    `json:"storage"`
	Attachment      Attachment `json:"attachment"`
	CurrentRevision string     `json:"currentRevision,omitempty"`
	Revisions       []Revision `json:"revisions,omitempty"`
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

type List struct {
	Items []Resource `json:"items"`
}

type Manager interface {
	CreateContext(context.Context, CreateRequest) (Resource, error)
	ListContexts(context.Context, string) ([]Resource, error)
	GetContext(context.Context, string, string) (Resource, error)
	DeleteContext(context.Context, string, string) error
	PublishContextRevision(context.Context, string, string, Revision) (Resource, error)
}
