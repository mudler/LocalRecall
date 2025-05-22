package sources

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

// GetGitRepositoryContent fetches and processes content from a Git repository
func GetGitRepositoryContent(url string, privateKey string) (string, error) {
	// Create a temporary directory for cloning
	tempDir, err := os.MkdirTemp("", "git-repo-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempDir)

	// Configure clone options
	cloneOptions := &git.CloneOptions{
		URL:           url,
		Depth:         1,
		SingleBranch:  true,
		ReferenceName: plumbing.HEAD,
	}

	// If private key is provided, use SSH authentication
	if privateKey != "" {
		// Decode base64 private key
		keyBytes, err := base64.StdEncoding.DecodeString(privateKey)
		if err != nil {
			return "", err
		}

		// Create SSH auth method from the decoded key
		auth, err := ssh.NewPublicKeys("git", keyBytes, "")
		if err != nil {
			return "", err
		}
		cloneOptions.Auth = auth
	}

	// Clone the repository
	_, err = git.PlainClone(tempDir, false, cloneOptions)
	if err != nil {
		return "", err
	}

	// Walk through the repository and collect content
	var content strings.Builder
	err = filepath.Walk(tempDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip .git directory and binary files
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}

		// Only process text files
		if !info.IsDir() && isTextFile(path) {
			fileContent, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			content.WriteString("\n--- File: " + strings.TrimPrefix(path, tempDir+"/") + " ---\n")
			content.Write(fileContent)
			content.WriteString("\n")
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	return content.String(), nil
}

// isTextFile checks if a file is likely to be a text file
func isTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	// List of common text file extensions
	textExtensions := map[string]bool{
		".txt": true, ".md": true, ".go": true, ".py": true, ".js": true,
		".ts": true, ".html": true, ".css": true, ".json": true, ".yaml": true,
		".yml": true, ".xml": true, ".sh": true, ".bash": true, ".c": true,
		".cpp": true, ".h": true, ".hpp": true, ".java": true, ".rb": true,
		".php": true, ".rs": true, ".swift": true, ".kt": true, ".scala": true,
		".sql": true, ".proto": true, ".toml": true, ".ini": true, ".conf": true,
		".log": true, ".csv": true, ".tsv": true, ".rst": true, ".tex": true,
		".adoc": true, ".asciidoc": true, ".wiki": true,
	}

	return textExtensions[ext]
}
