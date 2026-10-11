#!/bin/sh
# Prerequisites and provider provenance: see test/raw/LOCALAI-SMOKE.md.
set -eu
ROOT=$(pwd -P)
[ -f test/raw/downstream-lock.json ] || exit 1
W="$ROOT/.sdd-work"
[ ! -L "$W" ] || exit 1
mkdir -p "$W/logs" "$W/tmp" "$W/model-smoke"
export TMPDIR="$W/tmp" GOCACHE="$W/cache" GOMODCACHE="$W/mod" GOPATH="$W/gopath" GOENV=off GOTOOLCHAIN=local
export DOCKER_CONFIG="$W/docker"
: "${LOCALAI_SMOKE_BACKEND:?set path to genuine llama.cpp grpc-server}"
: "${LOCALAI_SMOKE_LIBS:?set native library directory}"
export LD_LIBRARY_PATH="$LOCALAI_SMOKE_LIBS${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
python3 - "$ROOT" <<'PY'
import hashlib,json,pathlib,tarfile,sys
r=pathlib.Path(sys.argv[1]); w=r/'.sdd-work'
lock=json.loads((r/'test/raw/downstream-lock.json').read_text())
assert hashlib.sha256((w/'localai.tar.gz').read_bytes()).hexdigest() == '99e86e821f0094a3176926cfc1508deb88d229b8ab1d292e52a361df08fdd92e', 'unexpected archive digest'
assert hashlib.sha256((w/'model-smoke/minilm.gguf').read_bytes()).hexdigest() == '797b70c4edf85907fe0a49eb85811256f65fa0f7bf52166b147fd16be2be4662', 'unexpected model digest'
with tarfile.open(w/'localai.tar.gz') as tar:
 members=tar.getmembers(); prefix=members[0].name.split('/')[0]
 assert prefix=='LocalAI-'+lock['revision'], prefix
 n=0
 for m in members:
  rel=pathlib.PurePosixPath(m.name).relative_to(prefix)
  p=w/'localai'/rel
  if m.isfile():
   assert p.is_file() and hashlib.sha256(p.read_bytes()).digest()==hashlib.sha256(tar.extractfile(m).read()).digest(), str(rel)
   n+=1
  elif m.issym(): assert p.is_symlink() and str(p.readlink())==m.linkname,str(rel)
 print('Verified archive source files:',n,'consumer revision:',lock['revision'])
PY
# The archive must have been downloaded from the immutable codeload revision;
# canonical digest recorded independently, never use the enclosing repo's HEAD.
sha256sum "$W/localai.tar.gz" "$W/model-smoke/minilm.gguf" "$LOCALAI_SMOKE_BACKEND"
docker image inspect localai/localai:latest --format 'provider image {{.Id}} {{json .Config.Labels}}'
image=sha256:f1bc435659b9da49180e868fdd0361915171a740cd947bf081b62a896a7ea8d0
name="lr-embedding-$$"
created=
backend_pid= provider_pid=
cleanup() {
 [ -z "$created" ] || docker rm -v "$name" >/dev/null 2>&1 || true
 [ -z "$provider_pid" ] || { kill "$provider_pid" 2>/dev/null || true; wait "$provider_pid" 2>/dev/null || true; }
 [ -z "$backend_pid" ] || { kill "$backend_pid" 2>/dev/null || true; wait "$backend_pid" 2>/dev/null || true; }
}
trap cleanup EXIT HUP INT TERM
if [ ! -f "$W/model-smoke/local-ai" ]; then
 docker create --name "$name" --network none --tmpfs /models --tmpfs /backends --tmpfs /configuration --tmpfs /data "$image" >/dev/null
 created=yes
 docker cp "$name:/local-ai" "$W/model-smoke/local-ai"
 docker rm -v "$name" >/dev/null
 created=
fi
echo "3c03e310467418dec59d6c391e395a8e4cdfcee6dd450e66f74eeeefa21a53f6  $W/model-smoke/local-ai" | sha256sum -c -
ports=$(python3 - <<'PY'
import socket
s=[socket.socket(),socket.socket()]
for x in s: x.bind(('127.0.0.1',0))
print(*(x.getsockname()[1] for x in s))
PY
)
backend_port=${ports%% *}; api_port=${ports##* }
cat > "$W/model-smoke/minilm.yaml" <<YAML
name: minilm
backend: smoke-llama
embeddings: true
parameters:
  model: minilm.gguf
context_size: 512
threads: 2
YAML
sed 's/name: minilm/name: granite-embedding-107m-multilingual/' "$W/model-smoke/minilm.yaml" > "$W/model-smoke/legacy.yaml"
(cd "$W/model-smoke"; exec "$LOCALAI_SMOKE_BACKEND" --addr "127.0.0.1:$backend_port") > "$W/logs/localai-native-smoke.log" 2>&1 &
backend_pid=$!
(cd "$W/model-smoke"; exec ./local-ai run --address "127.0.0.1:$api_port" --models-path "$W/model-smoke" --backends-path "$W/model-smoke/backends" --backends-system-path "$W/model-smoke/system" --data-path "$W/model-smoke/data" --localai-config-dir "$W/model-smoke/config" --external-grpc-backends "smoke-llama:127.0.0.1:$backend_port" --disable-web-ui --disable-agents) > "$W/logs/localai-provider-smoke.log" 2>&1 &
provider_pid=$!
export LOCALRECALL_SMOKE_API="http://127.0.0.1:$api_port/v1"
python3 - "$LOCALRECALL_SMOKE_API" "$W/logs/localai-embeddings.json" "$provider_pid" <<'PY'
import urllib.request,time,json,sys,math,os
for i in range(90):
 os.kill(int(sys.argv[3]),0)
 try:
  req=urllib.request.Request(sys.argv[1]+'/embeddings',data=json.dumps({'model':'minilm','input':'The telescope studies distant galaxies.'}).encode(),headers={'Content-Type':'application/json'})
  with urllib.request.urlopen(req,timeout=10) as resp: data=json.load(resp)
  v=data['data'][0]['embedding']; assert len(v)==384 and all(math.isfinite(x) for x in v) and any(x!=0 for x in v)
  open(sys.argv[2],'w').write(json.dumps(data)); print('Genuine MiniLM /embeddings ready, dimensions',len(v)); break
 except Exception as e:
  if i==89: raise
  time.sleep(1)
PY
# An optional command runs only after a genuine model response, and remains
# inside this script's owned provider lifetime. No externally supplied endpoint.
if [ "$#" -gt 0 ]; then
 export LOCALAI_ENDPOINT="$LOCALRECALL_SMOKE_API"
 "$@"
 exit $?
fi
cp test/raw/templates/localai_consumer_test.go.txt "$W/localai/core/services/agentpool/localrecall_smoke_test.go"
# Preserve upstream mod/sum; resolve only in private alternate files.
cp "$W/localai/go.mod" "$W/localai-smoke.mod"
cp "$W/localai/go.sum" "$W/localai-smoke.sum"
(cd "$W/localai"; go mod edit -modfile="$W/localai-smoke.mod" -replace="github.com/mudler/localrecall=$ROOT")
echo "consumer: go test -mod=mod -modfile=$W/localai-smoke.mod -count=1 -v ./core/services/agentpool -run ^TestLocalRecallRealConsumer$ (no build tags)"
sh test/raw/database.sh sh -c 'cd "$1/localai"; go test -mod=mod -modfile="$1/localai-smoke.mod" -count=1 -v -timeout=5m ./core/services/agentpool -run "^TestLocalRecallRealConsumer$"' sh "$W"
