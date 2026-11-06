#!/usr/bin/env bash
# ==============================================================================
# Bitnami Redis Cluster Deployment & Health Check Script
# Microservice: rag-kb-catalog-service
# ==============================================================================

set -euo pipefail

NAMESPACE="${NAMESPACE:-rag-system}"
RELEASE_NAME="${RELEASE_NAME:-redis-cluster}"
CHART_REPO="https://charts.bitnami.com/bitnami"
VALUES_FILE="deployments/helm/redis-cluster/values.yaml"

echo "=== Deploying Self-Hosted Bitnami Redis Cluster ==="
echo "Target Namespace: $NAMESPACE"
echo "Release Name:     $RELEASE_NAME"

# 1. Ensure Helm is installed
if ! command -v helm &>/dev/null; then
  echo "Error: helm is not installed. Please install Helm 3." >&2
  exit 1
fi

# 2. Add and update Bitnami chart repo
echo "Updating Bitnami Helm repository..."
helm repo add bitnami "$CHART_REPO" --force-update >/dev/null 2>&1 || true
helm repo update bitnami

# 3. Create destination namespace
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# 4. Deploy or Upgrade Redis Cluster
echo "Installing/Upgrading Redis Cluster via Helm..."
helm upgrade --install "$RELEASE_NAME" bitnami/redis-cluster \
  --namespace "$NAMESPACE" \
  -f "$VALUES_FILE" \
  --wait \
  --timeout 10m

# 5. Check cluster rollout
echo "Waiting for Redis Cluster pods to reach Ready state..."
kubectl rollout status statefulset/"$RELEASE_NAME" -n "$NAMESPACE" --timeout=300s

# 6. Verify Redis Cluster Topology
echo "Verifying Redis Cluster topology..."
CLUSTER_INFO=$(kubectl exec -n "$NAMESPACE" "${RELEASE_NAME}-0" -- redis-cli -c -a "$(kubectl get secret rag-kb-catalog-service-secret -n "$NAMESPACE" -o jsonpath='{.data.REDIS_PASSWORD}' | base64 -d)" cluster info)
echo "$CLUSTER_INFO" | grep -E 'cluster_state|cluster_slots_assigned|cluster_known_nodes'

if ! echo "$CLUSTER_INFO" | grep -q "^cluster_state:ok"; then
  echo "Error: Redis cluster_state is not 'ok'" >&2
  exit 1
fi

if ! echo "$CLUSTER_INFO" | grep -q "^cluster_slots_assigned:16384"; then
  echo "Error: Redis cluster_slots_assigned is not '16384'" >&2
  exit 1
fi

echo "=== Redis Cluster Successfully Deployed & Verified! ==="
echo "Internal Cluster DNS: ${RELEASE_NAME}-headless.${NAMESPACE}.svc.cluster.local:6379"

