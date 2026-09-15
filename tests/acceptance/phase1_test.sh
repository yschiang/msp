#!/usr/bin/env bash
#
# MSP Phase 1 acceptance gate (MSP-SPEC-001 §11 Phase 1 + design §9). Run it
# with `make phase1-accept`.
#
# Steps:
#   1. build: base + example + probe images (the SDK changed, so the base
#      image must be rebuilt), Go binaries, generated CRD drift check
#   2. two insecure registries (model-center :5010, platform-internal :5011)
#      and a kind cluster named "msp" -- reused if it already exists, created
#      and torn down otherwise (D22); kind nodes are pointed at the internal
#      registry through containerd's hosts.toml
#   3. namespaces + CRD applied from the GitOps tree (kubectl apply stands in
#      for ArgoCD)
#   4. the example image published to model-center as defect-cls:v1; msp-sync
#      started on the host against the cluster
#   5. item 1: declare -> Deployed with a ready pod
#   6. item 2: overwrite the tag at model-center; pinned digest still governs
#   7. item 4: a nonconformant image lands in Rejected naming C3
#   8. item 3: delete the CR; Deployment, Service, HPA are garbage-collected
#   9. cleanup (trap): msp-sync, labelled containers, the cluster if we made it
#
# Requires docker, go, python3 (+ grpcio-health-checking), kind >= 0.27,
# kubectl, curl.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

CLUSTER="msp"
MC_REG="msp-registry-model-center"
INT_REG="msp-registry-internal"
MC_REG_HOST="localhost:5010"
INT_REG_HOST="localhost:5011"
NS="msp-green"
CR_FILE="deploy/clusters/green/deployments/defect-cls-v1.yaml"
CR_NAME="defect-cls-v1"
LABEL="msp-phase1-accept=1"

WORK="$(mktemp -d)"
KCFG="$WORK/kubeconfig"
SYNC_PID=""
CREATED_CLUSTER=""
START_EPOCH=$(date +%s)

# ---------------------------------------------------------------- plumbing --

k() { kubectl --kubeconfig "$KCFG" "$@"; }

remove_labelled_containers() {
	local ids
	ids="$(docker ps -aq --filter "label=$LABEL" 2>/dev/null || true)"
	if [ -n "$ids" ]; then
		# shellcheck disable=SC2086
		docker rm -f $ids >/dev/null 2>&1 || true
	fi
}

cleanup() {
	set +e
	if [ -n "$SYNC_PID" ]; then
		kill "$SYNC_PID" >/dev/null 2>&1
		wait "$SYNC_PID" >/dev/null 2>&1
	fi
	if [ -n "$CREATED_CLUSTER" ]; then
		kind delete cluster --name "$CLUSTER" >/dev/null 2>&1
	fi
	remove_labelled_containers
	rm -rf "$WORK"
}
trap cleanup EXIT
trap 'echo; echo "interrupted"; exit 130' INT
trap 'echo; echo "terminated"; exit 143' TERM

step() { printf '\n=== %s ===\n' "$*"; }
fail() {
	printf '\nPHASE 1 ACCEPTANCE FAILED: %s\n' "$*" >&2
	if [ -f "$WORK/msp-sync.log" ]; then
		echo "--- msp-sync log (tail) ---" >&2
		tail -40 "$WORK/msp-sync.log" >&2
	fi
	exit 1
}

# registry_digest <host:port> <repo> <tag> -- what the registry says the tag
# points at right now. Accepts every manifest media type docker may push.
registry_digest() {
	curl -sI "http://$1/v2/$2/manifests/$3" \
		-H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json' |
		tr -d '\r' | awk 'tolower($1)=="docker-content-digest:" {print $2}'
}

md_field() { k get modeldeployment -n "$NS" "$1" -o "jsonpath=$2"; }

# wait_phase <name> <phase> <timeout_seconds> -- prints each phase change.
wait_phase() {
	local name="$1" want="$2" timeout="$3" deadline last="" cur
	deadline=$(($(date +%s) + timeout))
	while [ "$(date +%s)" -lt "$deadline" ]; do
		cur="$(md_field "$name" '{.status.phase}' 2>/dev/null || true)"
		if [ "$cur" != "$last" ]; then
			echo "  $name: phase=${cur:-<empty>}"
			last="$cur"
		fi
		[ "$cur" = "$want" ] && return 0
		if [ "$cur" = "Rejected" ] && [ "$want" != "Rejected" ]; then
			echo "  message: $(md_field "$name" '{.status.message}')"
			return 1
		fi
		sleep 2
	done
	return 1
}

# ------------------------------------------------------------------ step 1 --

remove_labelled_containers

step "step 1/9: build images, Go binaries; CRD drift check; unit tests"
make image-base image-example image-probe build crd-check
cd msp && go test ./... && cd "$ROOT"
docker build -t msp-neg-c3-bad-status:dev \
	-f contract/examples/negative/Dockerfile.c3-bad-status contract/examples/negative >"$WORK/c3.build" 2>&1 ||
	{ cat "$WORK/c3.build"; fail "c3-bad-status fixture failed to build"; }

# ------------------------------------------------------------------ step 2 --

step "step 2/9: registries on :5010 and :5011, kind cluster '$CLUSTER'"
docker run -d --label "$LABEL" --name "$MC_REG" -p "127.0.0.1:5010:5000" registry:2 >/dev/null
docker run -d --label "$LABEL" --name "$INT_REG" -p "127.0.0.1:5011:5000" registry:2 >/dev/null
for i in $(seq 1 30); do
	curl -sf "http://$MC_REG_HOST/v2/" >/dev/null && curl -sf "http://$INT_REG_HOST/v2/" >/dev/null && break
	[ "$i" -lt 30 ] || fail "registries never answered /v2/"
	sleep 1
done

if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
	echo "  reusing existing kind cluster '$CLUSTER' (left standing at the end)"
else
	echo "  creating kind cluster '$CLUSTER' (deleted at the end)"
	kind create cluster --name "$CLUSTER" --wait 120s >"$WORK/kind.log" 2>&1 ||
		{ cat "$WORK/kind.log"; fail "kind create cluster failed"; }
	CREATED_CLUSTER=1
fi
kind get kubeconfig --name "$CLUSTER" >"$KCFG"

# Pods pull from localhost:5011; inside a kind node that name means the
# registry container on the "kind" docker network (kind's local-registry
# recipe). Idempotent, so a reused cluster is re-pointed every run.
docker network connect kind "$INT_REG" 2>/dev/null || true
for node in $(kind get nodes --name "$CLUSTER"); do
	docker exec "$node" mkdir -p "/etc/containerd/certs.d/$INT_REG_HOST"
	printf '[host."http://%s:5000"]\n' "$INT_REG" |
		docker exec -i "$node" cp /dev/stdin "/etc/containerd/certs.d/$INT_REG_HOST/hosts.toml"
done

# ------------------------------------------------------------------ step 3 --

step "step 3/9: apply namespaces and CRD from deploy/ (kubectl stands in for ArgoCD)"
k apply -f deploy/platform/namespaces.yaml
k apply -f deploy/platform/crd/
k wait --for=condition=Established crd/modeldeployments.msp.platform --timeout=60s
k delete modeldeployment -n "$NS" --all --ignore-not-found >/dev/null # leftovers from an interrupted run

# ------------------------------------------------------------------ step 4 --

step "step 4/9: publish defect-cls:v1 to model-center; start msp-sync"
docker tag msp-example-defect-cls:dev "$MC_REG_HOST/defect-cls:v1"
docker push "$MC_REG_HOST/defect-cls:v1" >"$WORK/push.log" 2>&1 || { cat "$WORK/push.log"; fail "push to model-center failed"; }
GOOD_DIGEST="$(registry_digest "$MC_REG_HOST" defect-cls v1)"
[ -n "$GOOD_DIGEST" ] || fail "could not read the digest of defect-cls:v1 from model-center"
echo "  model-center defect-cls:v1 = $GOOD_DIGEST"

bin/msp-sync --kubeconfig "$KCFG" \
	--model-center-registry "$MC_REG_HOST" --internal-registry "$INT_REG_HOST" \
	>"$WORK/msp-sync.log" 2>&1 &
SYNC_PID=$!
sleep 2
kill -0 "$SYNC_PID" 2>/dev/null || fail "msp-sync exited at startup"
