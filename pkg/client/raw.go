// Package client provides additive context-aware access to raw vectors.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/mudler/localrecall/rag/raw"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type RawClient struct {
	baseURL, apiKey string
	httpClient      *http.Client
}

func NewRawClient(baseURL, apiKey string, httpClient *http.Client) *RawClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &RawClient{baseURL, apiKey, httpClient}
}
func (c *RawClient) do(ctx context.Context, method, path string, q url.Values, body, out any) (int, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return 0, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return 0, fmt.Errorf("invalid raw API base URL")
	}
	escapedPath := strings.TrimRight(u.EscapedPath(), "/") + "/api/raw/v1" + path
	u.Path, err = url.PathUnescape(escapedPath)
	if err != nil {
		return 0, err
	}
	u.RawPath = escapedPath
	u.RawQuery = q.Encode()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var envelope struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   *raw.Error      `json:"error"`
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil {
		return resp.StatusCode, err
	}
	if len(b) > 32<<20 {
		return resp.StatusCode, fmt.Errorf("raw API response exceeds 32 MiB")
	}
	if err = json.Unmarshal(b, &envelope); err != nil {
		return resp.StatusCode, fmt.Errorf("invalid raw API response (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.Success {
		if envelope.Error != nil {
			e := raw.NewError(envelope.Error.Code)
			e.Status = resp.StatusCode
			return resp.StatusCode, e
		}
		return resp.StatusCode, fmt.Errorf("raw API request failed (HTTP %d)", resp.StatusCode)
	}
	if out != nil {
		err = json.Unmarshal(envelope.Data, out)
	}
	return resp.StatusCode, err
}
func rawCollectionPath(cid string) (string, error) {
	id, err := uuid.Parse(cid)
	if err != nil || id.String() != cid {
		return "", raw.NewError(raw.CodeValidation)
	}
	return "/collections/" + cid, nil
}
func (c *RawClient) Capabilities(ctx context.Context) (out raw.Capabilities, err error) {
	_, err = c.do(ctx, "GET", "/capabilities", nil, nil, &out)
	return
}
func (c *RawClient) CreateCollection(ctx context.Context, req raw.CreateCollectionRequest) (out raw.Collection, created bool, err error) {
	status, err := c.do(ctx, "POST", "/collections", nil, req, &out)
	return out, status == 201, err
}
func (c *RawClient) ListCollections(ctx context.Context, req raw.ListCollectionsRequest) (out raw.CollectionPage, err error) {
	q := url.Values{}
	if req.Limit != nil {
		q.Set("limit", strconv.Itoa(*req.Limit))
	}
	if req.Cursor != nil {
		q.Set("cursor", *req.Cursor)
	}
	_, err = c.do(ctx, "GET", "/collections", q, nil, &out)
	return
}
func (c *RawClient) GetCollection(ctx context.Context, cid string) (out raw.Collection, err error) {
	path, err := rawCollectionPath(cid)
	if err != nil {
		return
	}
	_, err = c.do(ctx, "GET", path, nil, nil, &out)
	return
}
func (c *RawClient) Upsert(ctx context.Context, cid string, req raw.UpsertRequest) (out raw.UpsertResponse, err error) {
	path, err := rawCollectionPath(cid)
	if err != nil {
		return
	}
	_, err = c.do(ctx, "POST", path+"/records/upsert", nil, req, &out)
	return
}
func (c *RawClient) GetRecords(ctx context.Context, cid string, req raw.GetRecordsRequest) (out raw.GetRecordsResponse, err error) {
	path, err := rawCollectionPath(cid)
	if err != nil {
		return
	}
	_, err = c.do(ctx, "POST", path+"/records/get", nil, req, &out)
	return
}
func (c *RawClient) ListRecords(ctx context.Context, cid string, req raw.ListRecordsRequest) (out raw.RecordPage, err error) {
	path, err := rawCollectionPath(cid)
	if err != nil {
		return
	}
	_, err = c.do(ctx, "POST", path+"/records/list", nil, req, &out)
	return
}
func (c *RawClient) DeleteRecords(ctx context.Context, cid string, req raw.DeleteRecordsRequest) (out raw.DeleteRecordsResponse, err error) {
	path, err := rawCollectionPath(cid)
	if err != nil {
		return
	}
	_, err = c.do(ctx, "POST", path+"/records/delete", nil, req, &out)
	return
}
func (c *RawClient) Search(ctx context.Context, cid string, req raw.SearchRequest) (out raw.SearchResponse, err error) {
	path, err := rawCollectionPath(cid)
	if err != nil {
		return
	}
	_, err = c.do(ctx, "POST", path+"/search", nil, req, &out)
	return
}
func (c *RawClient) Reset(ctx context.Context, cid string) error {
	path, err := rawCollectionPath(cid)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, "POST", path+"/reset", nil, raw.ResetRequest{}, nil)
	return err
}
func (c *RawClient) DeleteCollection(ctx context.Context, cid string) error {
	path, err := rawCollectionPath(cid)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, "DELETE", path, nil, nil, nil)
	return err
}
