# Actual LocalAI consumer smoke

Run from the LocalRecall worktree root:

```sh
LOCALAI_SMOKE_BACKEND=/absolute/path/to/llama.cpp/grpc-server \
LOCALAI_SMOKE_LIBS=/absolute/path/to/native/libraries \
sh test/raw/localai-smoke.sh > .sdd-work/logs/localai-real.log 2>&1
```

This is a required-dependency test: missing inputs fail, never skip. It requires
Go matching the pinned consumer, Python 3, Docker, curl for provisioning, and
approximately 1 GiB of additional headroom with the existing Go caches populated.
A clean Go cache needs substantially more. Do not blindly pull large images.

## Provisioning

The runner intentionally does not download native dependencies or images:

* Download `https://codeload.github.com/mudler/LocalAI/tar.gz/d4a5af8bfed9c97e8cb01e584f1f9f8a572df844`
  into `.sdd-work/localai.tar.gz`; extract with `--strip-components=1` into
  `.sdd-work/localai`. Read its `AGENTS.md` and build instructions. Generate Go
  protobuf files with `make protogen-go`, using private GOPATH/GOCACHE/GOMODCACHE
  and TMPDIR under `.sdd-work`. The runner verifies every archive file against
  the extracted tree, while allowing extra generated files and the smoke fixture.
* Download `https://huggingface.co/gaianet/All-MiniLM-L6-v2-Embedding-GGUF/resolve/main/all-MiniLM-L6-v2-ggml-model-f16.gguf`
  into `.sdd-work/model-smoke/minilm.gguf` (44 MiB). The runner requires SHA-256
  `797b70c4edf85907fe0a49eb85811256f65fa0f7bf52166b147fd16be2be4662`:
  upstream `main` is mutable, but accepted file content is pinned.
* Have cached provider image
  `sha256:f1bc435659b9da49180e868fdd0361915171a740cd947bf081b62a896a7ea8d0`
  and the PostgreSQL image required by `database.sh`. No running host service is used.
* Supply a genuine llama.cpp gRPC backend and its native library directory.
  These are used read-only; backend cwd is the private model directory.
  The successful local run used `.sdd-bridge-repro/grpc-server` and
  `.sdd-deps/root/usr/lib/x86_64-linux-gnu` from the existing
  `mudler-LocalAI/llama-decisions` workspace. Backend SHA-256 was
  `ce81165dfa8459b3b450402e54c69fd476c2b1ad17d883a8fa85eea84df5fc78`.
  Its source revision is not attested by this smoke; binary identity is logged.

## What it proves

The provider binary is copied from the cached image into `.sdd-work`, with a
verified binary digest. Its version is v4.11.0 / source label
`58830f7ac508845a6f4efa32cfca06af422d4d82`, **not** the consumer revision.
A private loopback provider and native backend are started and stopped by the
runner. An actual `/v1/embeddings` request must return 384 finite, nonzero MiniLM
values before testing. Responses and native/provider logs stay in `.sdd-work/logs`.

The generated test lives in the pinned real `core/services/agentpool` package.
It calls `NewAgentPoolService`, `buildCollectionsConfig`, the production embedded
`collections.NewInProcessBackend`, and `shareCollections`, matching the collection
portion of `startDistributed`. It then exercises the real ForUser upload, list,
entry-content, search and reset methods. No fake engine, HTTP store adapter or
standalone reimplementation substitutes for the consumer. Unrelated schedulers,
skills and NATS are deliberately not started. An alternate modfile replaces
LocalRecall with this worktree; neither source go.mod/go.sum is edited.

The successful run used PostgreSQL HNSW fallback (vectorscale was unavailable).
It verifies text embedding compatibility, not EmbeddingGemma2 or multimodal
quality. It does not complete Task6: concurrent raw zero-embedding accounting,
full legacy suites, cross-process/restart regressions and the all-runner remain
separate required gates.

## Evidence

Initial run failed before inference because the provider flag was incorrectly
spelled `--disable-webui`; provider help identified `--disable-web-ui`. After
that one-line correction, `.sdd-work/logs/localai-real-attempt3.log` reports
`TestLocalRecallRealConsumer` PASS and owned database removal. The earlier
compilation-only evidence is `localai-build-generated-attempt1.log`; it is not
counted as a runtime test.
