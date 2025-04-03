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

	"github.com/mudler/localrecall/rag/types"
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

// ListCollections lists all collections
func (c *Client) ListEntries(collection string) ([]string, error) {
	url := fmt.Sprintf("%s/api/collections/%s/entries", c.BaseURL, collection)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("failed to list collections")
	}

	var entries []string
	err = json.NewDecoder(resp.Body).Decode(&entries)
	if err != nil {
		return nil, err
	}

	return entries, nil
}

// DeleteEntry deletes an Entry in a collection and return the entries left
func (c *Client) DeleteEntry(collection, entry string) ([]string, error) {
	url := fmt.Sprintf("%s/api/collections/%s/entry/delete", c.BaseURL, collection)

	type request struct {
		Entry string `json:"entry"`
	}
	client := &http.Client{}
	payload, err := json.Marshal(request{Entry: entry})
	if err != nil {
		return nil, err
	}

	// Create request
	req, err := http.NewRequest("DELETE", url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	// Fetch Request
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyResult := new(bytes.Buffer)
		bodyResult.ReadFrom(resp.Body)
		return nil, errors.New("failed to delete collection: " + bodyResult.String())
	}

	var results []string
	err = json.NewDecoder(resp.Body).Decode(&results)
	if err != nil {
		return nil, err
	}

	return results, nil
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

func (c *Client) Reset(collection string) error {
	url := fmt.Sprintf("%s/api/collections/%s/reset", c.BaseURL, collection)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return err
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b := new(bytes.Buffer)
		b.ReadFrom(resp.Body)

		return errors.New("failed to reset collection: " + b.String())
	}

	return nil
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
		b := new(bytes.Buffer)
		b.ReadFrom(resp.Body)

		type response struct {
			Error string `json:"error"`
		}

		var r response
		err = json.Unmarshal(b.Bytes(), &r)
		if err == nil {
			return errors.New("failed to upload file: " + r.Error)
		}

		return errors.New("failed to upload file")
	}

	return nil
}
