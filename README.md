# LocalRAG

A simple, no-frills LocalRAG webui that works well with LocalAI.

No GPU, No internet, no cloud needed.

See also:

- [LocalAI](https://github.com/mudler/LocalAI)
- [LocalAgent](https://github.com/mudler/LocalAgent)

## Features

- Simple and lightweight web UI
- Works locally with LocalAI
- No dependency on external cloud services
- Provides a RAG knowledgebase layer to use on top of other Vector Databases, or just use the embedded Vector Database
- Supported file types:
  - Markdown
  - Text
  - PDF
  - more to come..

## Prerequisites
- Go 1.16 or above
- Docker (optional, for containerized deployment)

## Installation

### Clone the Repository
```sh
git clone https://github.com/mudler/LocalRAG.git
cd LocalRAG
```

### Build from Source
```sh
go build -o localrag
```

### Run the Application
```sh
./localrag
```

## Docker Deployment
Build and run the Docker container:
```sh
docker build -t localrag .
docker run -p 8080:8080 localrag
```

## REST API Documentation

### Create a Collection
```sh
curl -X POST http://localhost:8080/api/collections \
  -H "Content-Type: application/json" \
  -d '{"name":"myCollection"}'
```

### Upload a File to a Collection
```sh
curl -X POST http://localhost:8080/api/collections/myCollection/upload \
  -F "file=@/path/to/your/file.txt"
```

### List All Collections
```sh
curl -X GET http://localhost:8080/api/collections
```

### List Files in a Collection
```sh
curl -X GET http://localhost:8080/api/collections/myCollection/entries
```

### Search in a Collection
```sh
curl -X POST http://localhost:8080/api/collections/myCollection/search \
  -H "Content-Type: application/json" \
  -d '{"query":"search term", "max_results":5}'
```

### Reset a Collection
```sh
curl -X POST http://localhost:8080/api/collections/myCollection/reset
```

### Delete an Entry in a Collection
```sh
curl -X DELETE http://localhost:8080/api/collections/myCollection/entry/delete \
  -H "Content-Type: application/json" \
  -d '{"entry":"file.txt"}'
```

## License
This project is licensed under the MIT License.

## Contributing
Contributions are welcome! Please open an issue or submit a pull request.
