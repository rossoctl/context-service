package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/pool"
	"github.com/rossoctl/context-service/internal/storageclass"
)

type Client struct {
	baseURL string
	token   string
	subject string
	http    *http.Client
}

func (c *Client) SetSubject(subject string) { c.subject = strings.TrimSpace(subject) }

func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: httpClient}
}

func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", nil, nil)
}

func (c *Client) ListStorageClasses(ctx context.Context) ([]storageclass.Resource, error) {
	var result storageclass.List
	err := c.do(ctx, http.MethodGet, "/v1/storage-classes", nil, &result)
	return result.Items, err
}

func (c *Client) Create(ctx context.Context, request pool.CreateRequest) (pool.Pool, error) {
	var result pool.Pool
	err := c.do(ctx, http.MethodPost, "/v1/sandbox-pools", request, &result)
	return result, err
}

func (c *Client) List(ctx context.Context) ([]pool.Pool, error) {
	var result pool.List
	err := c.do(ctx, http.MethodGet, "/v1/sandbox-pools", nil, &result)
	return result.Items, err
}

func (c *Client) Get(ctx context.Context, name string) (pool.Pool, error) {
	var result pool.Pool
	err := c.do(ctx, http.MethodGet, "/v1/sandbox-pools/"+name, nil, &result)
	return result, err
}

func (c *Client) Delete(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/sandbox-pools/"+name, nil, nil)
}

func (c *Client) CreateContext(ctx context.Context, request contextresource.CreateRequest) (contextresource.Resource, error) {
	var result contextresource.Resource
	err := c.do(ctx, http.MethodPost, "/v1/contexts", request, &result)
	return result, err
}

func (c *Client) ListContexts(ctx context.Context, namespace string) ([]contextresource.Resource, error) {
	var result contextresource.List
	err := c.do(ctx, http.MethodGet, "/v1/namespaces/"+url.PathEscape(namespace)+"/contexts", nil, &result)
	return result.Items, err
}

func (c *Client) GetContext(ctx context.Context, namespace, name string) (contextresource.Resource, error) {
	var result contextresource.Resource
	err := c.do(ctx, http.MethodGet, "/v1/namespaces/"+url.PathEscape(namespace)+"/contexts/"+url.PathEscape(name), nil, &result)
	return result, err
}

func (c *Client) CreateContextSnapshot(ctx context.Context, namespace, name string, request contextresource.SnapshotRequest) (contextresource.Snapshot, error) {
	var result contextresource.Snapshot
	err := c.do(ctx, http.MethodPost, contextPath(namespace, name)+"/snapshots", request, &result)
	return result, err
}

func (c *Client) ListContextSnapshots(ctx context.Context, namespace, name string) ([]contextresource.Snapshot, error) {
	var result contextresource.SnapshotList
	err := c.do(ctx, http.MethodGet, contextPath(namespace, name)+"/snapshots", nil, &result)
	return result.Items, err
}

func (c *Client) GetContextSnapshot(ctx context.Context, namespace, name, snapshot string) (contextresource.Snapshot, error) {
	var result contextresource.Snapshot
	err := c.do(ctx, http.MethodGet, contextPath(namespace, name)+"/snapshots/"+url.PathEscape(snapshot), nil, &result)
	return result, err
}

func (c *Client) CloneContextSnapshot(ctx context.Context, namespace, name string, request contextresource.CloneRequest) (contextresource.Resource, error) {
	var result contextresource.Resource
	err := c.do(ctx, http.MethodPost, contextPath(namespace, name)+"/clones", request, &result)
	return result, err
}

func (c *Client) RestoreContextSnapshot(ctx context.Context, namespace, name string, request contextresource.RestoreRequest) (contextresource.Resource, error) {
	var result contextresource.Resource
	err := c.do(ctx, http.MethodPost, contextPath(namespace, name)+"/restore", request, &result)
	return result, err
}

func (c *Client) SetContextRetention(ctx context.Context, namespace, name string, request contextresource.RetentionRequest) (contextresource.Resource, error) {
	var result contextresource.Resource
	err := c.do(ctx, http.MethodPut, contextPath(namespace, name)+"/retention", request, &result)
	return result, err
}

func (c *Client) GarbageCollectContextSnapshots(ctx context.Context, namespace, name string, dryRun bool) (contextresource.GarbageCollection, error) {
	var result contextresource.GarbageCollection
	path := contextPath(namespace, name) + "/gc"
	if dryRun {
		path += "?dryRun=true"
	}
	err := c.do(ctx, http.MethodPost, path, nil, &result)
	return result, err
}

func (c *Client) ContextLifecycleCapabilities(ctx context.Context, namespace, name string) (contextresource.LifecycleCapabilities, error) {
	var result contextresource.LifecycleCapabilities
	err := c.do(ctx, http.MethodGet, contextPath(namespace, name)+"/capabilities", nil, &result)
	return result, err
}

func (c *Client) DeleteContext(ctx context.Context, namespace, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/namespaces/"+url.PathEscape(namespace)+"/contexts/"+url.PathEscape(name), nil, nil)
}

func (c *Client) ForceDeleteContext(ctx context.Context, namespace, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/namespaces/"+url.PathEscape(namespace)+"/contexts/"+url.PathEscape(name)+"?force=true", nil, nil)
}

func (c *Client) ListContextGrants(ctx context.Context, namespace, name string) ([]contextresource.Grant, error) {
	var result contextresource.GrantList
	err := c.do(ctx, http.MethodGet, contextPath(namespace, name)+"/grants", nil, &result)
	return result.Items, err
}

func (c *Client) SetContextGrant(ctx context.Context, namespace, name string, grant contextresource.Grant) (contextresource.Resource, error) {
	var result contextresource.Resource
	err := c.do(ctx, http.MethodPut, contextPath(namespace, name)+"/grants", grant, &result)
	return result, err
}

func (c *Client) RevokeContextGrant(ctx context.Context, namespace, name string, subject contextresource.Subject) (contextresource.Resource, error) {
	var result contextresource.Resource
	err := c.do(ctx, http.MethodDelete, contextPath(namespace, name)+"/grants", subject, &result)
	return result, err
}

func (c *Client) ListContextConsumers(ctx context.Context, namespace, name string) ([]contextresource.Consumer, error) {
	var result contextresource.ConsumerList
	err := c.do(ctx, http.MethodGet, contextPath(namespace, name)+"/consumers", nil, &result)
	return result.Items, err
}

func (c *Client) SetContextConsumer(ctx context.Context, namespace, name string, consumer contextresource.Consumer, attached bool) (contextresource.Resource, error) {
	method := http.MethodPut
	if !attached {
		method = http.MethodDelete
	}
	var result contextresource.Resource
	err := c.do(ctx, method, contextPath(namespace, name)+"/consumers", consumer, &result)
	return result, err
}

func (c *Client) ListContextAudit(ctx context.Context, namespace, name string) ([]contextresource.AuditEvent, error) {
	var result contextresource.AuditList
	err := c.do(ctx, http.MethodGet, contextPath(namespace, name)+"/audit", nil, &result)
	return result.Items, err
}

func contextPath(namespace, name string) string {
	return "/v1/namespaces/" + url.PathEscape(namespace) + "/contexts/" + url.PathEscape(name)
}

func (c *Client) ListContextRevisions(ctx context.Context, namespace, name string) ([]contextresource.Revision, error) {
	var result contextresource.RevisionList
	err := c.do(ctx, http.MethodGet, "/v1/namespaces/"+url.PathEscape(namespace)+"/contexts/"+url.PathEscape(name)+"/revisions", nil, &result)
	return result.Items, err
}

func (c *Client) PublishContextRevision(ctx context.Context, namespace, name string, revision contextresource.Revision) (contextresource.Resource, error) {
	var result contextresource.Resource
	err := c.do(ctx, http.MethodPost, "/v1/namespaces/"+url.PathEscape(namespace)+"/contexts/"+url.PathEscape(name)+"/revisions", revision, &result)
	return result, err
}

func (c *Client) do(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("X-SH-Auth", c.token)
	}
	if c.subject != "" {
		req.Header.Set("X-Context-Subject", c.subject)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Message string `json:"message"`
		}
		if json.NewDecoder(response.Body).Decode(&apiError) == nil && apiError.Message != "" {
			return errors.New(apiError.Message)
		}
		return fmt.Errorf("context service returned %s", response.Status)
	}
	if output != nil {
		return json.NewDecoder(response.Body).Decode(output)
	}
	return nil
}
