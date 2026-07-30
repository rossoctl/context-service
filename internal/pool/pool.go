package pool

import (
	"context"
	"errors"
)

var (
	ErrAlreadyExists = errors.New("sandbox pool already exists")
	ErrNotFound      = errors.New("sandbox pool not found")
	ErrInvalid       = errors.New("invalid sandbox pool")
)

type CreateRequest struct {
	Name        string    `json:"name"`
	Replicas    int       `json:"replicas"`
	WarmPoolRef string    `json:"warmPoolRef,omitempty"`
	Workspace   Workspace `json:"workspace"`
}

type Workspace struct {
	Size         string `json:"size"`
	AccessMode   string `json:"accessMode"`
	StorageClass string `json:"storageClass,omitempty"`
	ClaimName    string `json:"claimName,omitempty"`
	ReadOnly     *bool  `json:"readOnly,omitempty"`
}

type Pool struct {
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	Replicas        int       `json:"replicas"`
	ReadyReplicas   int       `json:"readyReplicas"`
	SandboxSelector string    `json:"sandboxSelector"`
	WarmPoolRef     string    `json:"warmPoolRef,omitempty"`
	Workspace       Workspace `json:"workspace"`
}

type Manager interface {
	Create(context.Context, CreateRequest) (Pool, error)
	Get(context.Context, string) (Pool, error)
	Delete(context.Context, string) error
}
