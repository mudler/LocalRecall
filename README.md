<p align="center">
  <img src="https://github.com/user-attachments/assets/7f8322fe-f6e9-4e54-98b3-afae287b8082" alt="LocalRecall Logo" width="220"/>
</p>

<h3 align="center"><em>Your AI. Your Hardware. Your Rules.</em></h3>

<div align="center">
  
[![Go Report Card](https://goreportcard.com/badge/github.com/mudler/LocalRecall)](https://goreportcard.com/report/github.com/mudler/LocalRecall)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![GitHub stars](https://img.shields.io/github/stars/mudler/LocalRecall)](https://github.com/mudler/LocalRecall/stargazers)
[![GitHub issues](https://img.shields.io/github/issues/mudler/LocalRecall)](https://github.com/mudler/LocalRecall/issues)

</div>

A lightweight, no-frills RESTful API designed for managing knowledge bases and files stored in vector databases—**no GPU, internet, or cloud services required**! LocalRecall provides a simple and generic abstraction layer to handle knowledge retrieval, ideal for AI agents and chatbots to manage both long-term and short-term memory seamlessly.

Currently, LocalRecall is batteries included and supports a local vector store powered by **Chromem**, with plans to add additional vector stores such as **Milvus** and **Qdrant**. It can easily integrate with LocalAI, LocalAGI, and other agent frameworks, offering an intuitive web UI for convenient file management, including support for raw text inputs.

🔗 **LocalRecall is part of the Local AI stack family:**

- [LocalAI](https://github.com/mudler/LocalAI)
- [LocalAGI](https://github.com/mudler/LocalAGI)

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

or with Docker compose

```sh
docker compose up -d
```

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

---

## 📝 License

Released under the [MIT License](LICENSE).

---

## 🤝 Contributing

We welcome contributions! Please feel free to:

- ✅ Open an issue for suggestions or bugs.
- ✅ Submit a pull request with enhancements.

