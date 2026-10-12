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
	// Decode from a bounded stream rather than allocating the theoretical cap.
	limit := rawResponseLimit(method, path)
	readerLimit := &io.LimitedReader{R: resp.Body, N: limit + 1}
	decoder := json.NewDecoder(readerLimit)
	decodeErr := decoder.Decode(&envelope)
	// Drain the remaining bounded stream, including decoder read-ahead. This
	// detects overflow even when a complete JSON value precedes excess bytes.
	_, drainErr := io.Copy(rawJSONWhitespace{}, io.MultiReader(decoder.Buffered(), readerLimit))
	if readerLimit.N == 0 {
		return resp.StatusCode, fmt.Errorf("raw API response exceeds %d bytes", limit)
	}
	if drainErr != nil {
		return resp.StatusCode, drainErr
	}
	if decodeErr != nil {
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

// Bounds cover compact server JSON. Strings can expand to six bytes per input
// byte; metadata already has a serialized-byte limit. 32 bytes per vector
// component covers every finite float32 spelling plus its comma. Per-item slack
// covers keys, punctuation, scores/ranks, timestamps and other scalar fields.
const (
	rawItemOverhead     int64 = 2048
	rawEnvelopeOverhead int64 = 4096 + 6*raw.MaxCursorBytes
	rawRecordBytes      int64 = 6*(raw.MaxTextBytes+raw.MaxIDBytes) + raw.MaxMetadataBytes + 32*raw.MaxDimensions + rawItemOverhead
	// Disabled lexical indexing retains text_config without catalog resolution;
	// its existing validation limit is MaxBodyBytes, not PostgreSQL name length.
	rawCollectionBytes int64 = 6*(raw.MaxBodyBytes+raw.MaxNameBytes+raw.MaxSpaceIDBytes) + rawItemOverhead
)

func rawResponseLimit(method, path string) int64 {
	switch {
	case path == "/collections" && method == http.MethodGet:
		return rawEnvelopeOverhead + raw.MaxListLimit*rawCollectionBytes
	case path == "/collections" || (strings.HasPrefix(path, "/collections/") && strings.Count(path, "/") == 2):
		return rawEnvelopeOverhead + rawCollectionBytes
	default:
		return rawEnvelopeOverhead + int64(max(raw.MaxBatch, raw.MaxListLimit, raw.MaxTopK))*rawRecordBytes
	}
}

// Write checks trailing JSON whitespace with constant additional memory.
type rawJSONWhitespace struct{}

func (rawJSONWhitespace) Write(p []byte) (int, error) {
	for _, c := range p {
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			return 0, fmt.Errorf("invalid trailing raw API response data")
		}
	}
	return len(p), nil
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
