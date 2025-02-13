package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"

	"github.com/mudler/localrag/rag/types"
)

// Client is a client for the RAG API
type Client struct {
	BaseURL string
}

// NewClient creates a new RAG API client
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
	}
}

// CreateCollection creates a new collection
func (c *Client) CreateCollection(name string) error {
	url := fmt.Sprintf("%s/api/collections", c.BaseURL)

	type request struct {
		Name string `json:"name"`
	}

	payload, err := json.Marshal(request{Name: name})
	if err != nil {
		return err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return errors.New("failed to create collection")
	}

	return nil
}

// ListCollections lists all collections
func (c *Client) ListCollections() ([]string, error) {
	url := fmt.Sprintf("%s/api/collections", c.BaseURL)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("failed to list collections")
	}

	var collections []string
	err = json.NewDecoder(resp.Body).Decode(&collections)
	if err != nil {
		return nil, err
	}

	return collections, nil
}

// Search searches a collection
func (c *Client) Search(collection, query string, maxResults int) ([]types.Result, error) {
	url := fmt.Sprintf("%s/api/collections/%s/search", c.BaseURL, collection)

	type request struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}

	payload, err := json.Marshal(request{Query: query, MaxResults: maxResults})
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("failed to search collection")
	}

	var results []types.Result
	err = json.NewDecoder(resp.Body).Decode(&results)
	if err != nil {
		return nil, err
	}

	return results, nil
}

// Store uploads a file to a collection
func (c *Client) Store(collection, filePath string) error {
	url := fmt.Sprintf("%s/api/collections/%s/upload", c.BaseURL, collection)

	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", file.Name())
	if err != nil {
		return err
	}

	_, err = io.Copy(part, file)
	if err != nil {
		return err
	}

	err = writer.Close()
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("failed to upload file")
	}

	return nil
}
