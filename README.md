<p align="center">
  <img src="./static/logo.png" alt="LocalRecall Logo" width="220"/>
</p>

<h3 align="center"><em>Your AI. Your Hardware. Your Rules.</em></h3>

<div align="center">
  
[![Go Report Card](https://goreportcard.com/badge/github.com/mudler/LocalRecall)](https://goreportcard.com/report/github.com/mudler/LocalRecall)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![GitHub stars](https://img.shields.io/github/stars/mudler/LocalRecall)](https://github.com/mudler/LocalRecall/stargazers)
[![GitHub issues](https://img.shields.io/github/issues/mudler/LocalRecall)](https://github.com/mudler/LocalRecall/issues)

</div>

A lightweight, no-frills RESTful API designed for managing knowledge bases and files stored in vector databases—**no GPU, internet, or cloud services required**! LocalRecall provides a simple and generic abstraction layer to handle knowledge retrieval, ideal for AI agents and chatbots to manage both long-term and short-term memory seamlessly.

Currently, LocalRecall is batteries included and supports multiple vector database engines:
- **Chromem**: Local file-based vector store (default)
- **PostgreSQL**: Production-ready PostgreSQL with TimescaleDB, pgvector, and pgvectorscale for hybrid search (BM25 + vector similarity)

It can easily integrate with LocalAI, LocalAGI, and other agent frameworks, offering an intuitive web UI for convenient file management, including support for raw text inputs.

## 📚🆕 Local Stack Family

🆕 LocalAI is now part of a comprehensive suite of AI tools designed to work together:

<table>
  <tr>
    <td width="50%" valign="top">
      <a href="https://github.com/mudler/LocalAI">
        <img src="https://raw.githubusercontent.com/mudler/LocalAI/refs/heads/master/core/http/static/logo_horizontal.png" width="300" alt="LocalAI Logo">
      </a>
    </td>
    <td width="50%" valign="top">
      <h3><a href="https://github.com/mudler/LocalAI">LocalAI</a></h3>
      <p>LocalAI is the free, Open Source OpenAI alternative. LocalAI act as a drop-in replacement REST API that's compatible with OpenAI API specifications for local AI inferencing. Does not require GPU.</p>
    </td>
  </tr>
  <tr>
    <td width="50%" valign="top">
      <a href="https://github.com/mudler/LocalAGI">
        <img src="https://raw.githubusercontent.com/mudler/LocalAGI/refs/heads/main/webui/react-ui/public/logo_2.png" width="300" alt="LocalAGI Logo">
      </a>
    </td>
    <td width="50%" valign="top">
      <h3><a href="https://github.com/mudler/LocalAGI">LocalAGI</a></h3>
      <p>A powerful Local AI agent management platform that serves as a drop-in replacement for OpenAI's Responses API, enhanced with advanced agentic capabilities.</p>
    </td>
  </tr>
</table>

---

## 🌟 Features

- ⚡ **RESTful API**: Simple and intuitive REST interface for knowledge management.
- 📡 **Fully Local**: Operates offline without external cloud dependencies.
- 📚 **RAG Knowledgebase**: Retrieve-Augmented Generation (RAG) compatible with multiple vector databases.
- 🗃️ **Memory Management**: Ideal for AI-driven applications requiring memory abstraction.
- 📂 **File Support**:
  - ✅ Markdown
  - ✅ Plain Text
  - ✅ PDF
  - ⏳ More formats coming soon!

---

## ⚙️ Prerequisites

- **Go** 1.16 or higher
- **Docker** (optional, for containerized deployment)

---

## 🚧 Quickstart

### 📥 Clone Repository

```sh
git clone https://github.com/mudler/LocalRecall.git
cd LocalRecall
```

### 🛠️ Build from Source

```sh
go build -o localrecall
```

### ▶️ Run Application

```sh
./localrecall
```

Your web UI will be available at `http://localhost:8080`.

---

## 🐳 Docker Deployment

### Using Chromem (Default)

Build and run using Docker:

```sh
docker build -t localrecall .
docker run -ti -v $PWD/state:/state \
               -e COLLECTION_DB_PATH=/state/db \
               -e EMBEDDING_MODEL=granite-embedding-107m-multilingual \
               -e FILE_ASSETS=/state/assets \
               -e OPENAI_API_KEY=sk-1234567890 \
               -e OPENAI_BASE_URL=http://localai:8080 \
               -p 8080:8080 localrecall

# Or use the images already built by the CI:
docker run -ti -v $PWD/state:/state \
               -e COLLECTION_DB_PATH=/state/db \
               -e EMBEDDING_MODEL=granite-embedding-107m-multilingual \
               -e FILE_ASSETS=/state/assets \
               -e OPENAI_API_KEY=sk-1234567890 \
               -e OPENAI_BASE_URL=http://localai:8080 \
               -p 8080:8080 quay.io/mudler/localrecall
```

### Using PostgreSQL (Recommended for Production)

For production deployments, PostgreSQL provides better performance, scalability, and hybrid search capabilities (combining BM25 keyword search with vector similarity search).

#### Quick Start with Docker Compose

The easiest way to get started with PostgreSQL is using Docker Compose:

```sh
docker compose up -d
```

This will start:
- **LocalAI**: For embeddings (port 8081)
- **PostgreSQL**: With TimescaleDB, pgvector, and pgvectorscale extensions (port 5432)
- **LocalRecall**: RAG server configured to use PostgreSQL (port 8080)

#### Manual Setup

1. **Start PostgreSQL** (using the pre-built image):

```sh
docker run -d \
  --name localrecall-postgres \
  -e POSTGRES_DB=localrecall \
  -e POSTGRES_USER=localrecall \
  -e POSTGRES_PASSWORD=localrecall \
  -p 5432:5432 \
  -v postgres_data:/var/lib/postgresql/data \
  quay.io/mudler/localrecall:latest-postgresql
```

2. **Start LocalRecall** with PostgreSQL:

```sh
docker run -ti \
  -e DATABASE_URL=postgresql://localrecall:localrecall@localhost:5432/localrecall?sslmode=disable \
  -e VECTOR_ENGINE=postgres \
  -e EMBEDDING_MODEL=granite-embedding-107m-multilingual \
  -e FILE_ASSETS=/assets \
  -e OPENAI_API_KEY=sk-1234567890 \
  -e OPENAI_BASE_URL=http://localai:8080 \
  -e HYBRID_SEARCH_BM25_WEIGHT=0.5 \
  -e HYBRID_SEARCH_VECTOR_WEIGHT=0.5 \
  -p 8080:8080 \
  quay.io/mudler/localrecall
```

#### PostgreSQL Features

- **Hybrid Search**: Combines BM25 (keyword) and vector (semantic) search with configurable weights
- **Advanced Indexing**: 
  - GIN indexes for full-text search
  - BM25 indexes for keyword search
  - DiskANN/HNSW indexes for vector similarity search
- **Extensions Included**:
  - `pg_textsearch`: BM25 keyword search
  - `vectorscale`: Advanced vector search with DiskANN
  - `pgvector`: Vector similarity search (fallback)
  - `timescaledb`: Time-series capabilities

---

## 🌍 Environment Variables

LocalRecall uses environment variables to configure its behavior. These variables allow you to customize paths, models, and integration settings without modifying the code.

| Variable                    | Description                                                                                                     |
| --------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `COLLECTION_DB_PATH`        | Path to the vector database directory where collections are stored (for Chromem engine).                        |
| `DATABASE_URL`              | PostgreSQL connection string (required for PostgreSQL engine). Format: `postgresql://user:pass@host:port/db?sslmode=disable` |
| `EMBEDDING_MODEL`           | Name of the embedding model used for vectorization (e.g., `granite-embedding-107m-multilingual`).               |
| `FILE_ASSETS`               | Directory path to store and retrieve uploaded file assets.                                                      |
| `OPENAI_API_KEY`            | API key for embedding services (such as LocalAI or OpenAI-compatible APIs).                                     |
| `OPENAI_BASE_URL`           | Base URL for the embedding model API (commonly `http://localai:8080`).                                          |
| `LISTENING_ADDRESS`         | Address the server listens on (default: `:8080`). Useful for deployments on custom ports or network interfaces. |
| `VECTOR_ENGINE`             | Vector database engine to use (`chromem` by default, `postgres` for PostgreSQL).                              |
| `MAX_CHUNKING_SIZE`         | Maximum size (in characters) for breaking down documents into chunks. Affects performance and accuracy.       |
| `HYBRID_SEARCH_BM25_WEIGHT` | Weight for BM25 keyword search in hybrid search (default: 0.5, PostgreSQL only).                                 |
| `HYBRID_SEARCH_VECTOR_WEIGHT` | Weight for vector similarity search in hybrid search (default: 0.5, PostgreSQL only).                           |
| `API_KEYS`                  | Comma-separated list of API keys for securing access to the REST API (optional).                                |
| `GIT_PRIVATE_KEY`           | Base64-encoded SSH private key for accessing private Git repositories (optional).                                |

These variables can be passed directly when running the binary or inside your Docker container for easy configuration.

You can use an `.env` file to set the variables. The Docker compose file is configured to use an `.env` file in the root of the project if available.

---

## 📖 REST API

**Base URL**: `http://localhost:8080/api`

### 🔧 Manage Collections

- **Create Collection**:

```sh
curl -X POST $BASE_URL/collections \
  -H "Content-Type: application/json" \
  -d '{"name":"myCollection"}'
```

- **Upload File**:

```sh
curl -X POST $BASE_URL/collections/myCollection/upload \
  -F "file=@/path/to/file.txt"
```

- **List Collections**:

```sh
curl -X GET $BASE_URL/collections
```

- **List Files in Collection**:

```sh
curl -X GET $BASE_URL/collections/myCollection/entries
```

- **Get Entry Content**:

```sh
curl -X GET $BASE_URL/collections/myCollection/entries/file.txt
```

Returns `collection`, `entry`, `chunks` (array of `id`, `content`, `metadata`), and `count`.

- **Search Collection**:

```sh
curl -X POST $BASE_URL/collections/myCollection/search \
  -H "Content-Type: application/json" \
  -d '{"query":"search term", "max_results":5}'
```

- **Reset Collection**:

```sh
curl -X POST $BASE_URL/collections/myCollection/reset
```

- **Delete Entry**:

```sh
curl -X DELETE $BASE_URL/collections/myCollection/entry/delete \
  -H "Content-Type: application/json" \
  -d '{"entry":"file.txt"}'
```

- **Add External Source**:

```sh
curl -X POST $BASE_URL/collections/myCollection/sources \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.com", "update_interval":30}'
```

The `update_interval` is specified in minutes. If not provided, it defaults to 60 minutes.

External sources support various URL types:
- Web pages (https://example.com)
- Git repositories (https://github.com/user/repo.git or git@github.com:user/repo.git)
- Sitemaps (https://example.com/sitemap.xml)

For private Git repositories, set the `GIT_PRIVATE_KEY` environment variable with a base64-encoded SSH private key:
```sh
# Encode your private key
export GIT_PRIVATE_KEY=$(cat /path/to/private_key | base64 -w 0)
```

- **Remove External Source**:

```sh
curl -X DELETE $BASE_URL/collections/myCollection/sources \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.com"}'
```

External sources are automatically monitored and updated in the background. The content is periodically fetched and added to the collection, making it searchable through the regular search endpoint.

---

## 🔌 Model Context Protocol (MCP) Integration

LocalRecall can be controlled via MCP (Model Context Protocol) through the [LocalRecall MCP Server](https://github.com/mudler/mcps/tree/master/localrecall) available in the [mcps repository](https://github.com/mudler/mcps).

The MCP server provides tools for:
- 🔍 **Search**: Search content in collections
- ➕ **Create Collection**: Create new collections
- 🔄 **Reset Collection**: Clear collections
- 📄 **Add Document**: Upload documents to collections
- 📋 **List Collections**: List all available collections
- 📁 **List Files**: List files in a collection
- 🗑️ **Delete Entry**: Remove entries from collections

### Quick Start with MCP

The MCP server can be configured to enable specific tools for security and flexibility:

```bash
docker run -e LOCALRECALL_URL=http://localhost:8080 \
           -e LOCALRECALL_API_KEY=your-api-key \
           -e LOCALRECALL_ENABLED_TOOLS="search,list_collections,add_document" \
           ghcr.io/mudler/mcps/localrecall:latest
```

For more information and configuration options, see the [LocalRecall MCP Server documentation](https://github.com/mudler/mcps#-localrecall-server).

---

## 📝 License

Released under the [MIT License](LICENSE).

---

## 🤝 Contributing

We welcome contributions! Please feel free to:

- ✅ Open an issue for suggestions or bugs.
- ✅ Submit a pull request with enhancements.
