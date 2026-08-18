package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/pool"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: httpClient}
}

func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", nil, nil)
}

func (c *Client) Create(ctx context.Context, request pool.CreateRequest) (pool.Pool, error) {
	var result pool.Pool
	err := c.do(ctx, http.MethodPost, "/v1/sandbox-pools", request, &result)
	return result, err
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
	err := c.do(ctx, http.MethodGet, "/v1/namespaces/"+namespace+"/contexts", nil, &result)
	return result.Items, err
}

func (c *Client) GetContext(ctx context.Context, namespace, name string) (contextresource.Resource, error) {
	var result contextresource.Resource
	err := c.do(ctx, http.MethodGet, "/v1/namespaces/"+namespace+"/contexts/"+name, nil, &result)
	return result, err
}

func (c *Client) DeleteContext(ctx context.Context, namespace, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/namespaces/"+namespace+"/contexts/"+name, nil, nil)
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
