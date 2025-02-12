package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// createCollection handles creating a new collection
func createCollection(c echo.Context) error {
	type request struct {
		Name string `json:"name"`
	}

	r := new(request)
	if err := c.Bind(r); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	if _, exists := collections[r.Name]; exists {
		return c.JSON(http.StatusConflict, map[string]string{"error": "Collection already exists"})
	}

	collections[r.Name] = &Collection{Name: r.Name, Files: []string{}}
	return c.JSON(http.StatusCreated, collections[r.Name])
}

// uploadFile handles uploading files to a collection
func uploadFile(c echo.Context) error {
	name := c.Param("name")
	collection, exists := collections[name]
	if !exists {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "Collection not found"})
	}

	file, err := c.FormFile("file")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Failed to read file"})
	}

	f, err := file.Open()
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Failed to open file"})
	}
	defer f.Close()

	// Save the file to disk

	collection.Files = append(collection.Files, file.Filename)
	return c.JSON(http.StatusOK, collection)
}

// listCollections returns all collections
func listCollections(c echo.Context) error {
	return c.JSON(http.StatusOK, collections)
}

// Static files (in `static/index.html`) should contain the web UI layout for chatting and knowledge base management.
