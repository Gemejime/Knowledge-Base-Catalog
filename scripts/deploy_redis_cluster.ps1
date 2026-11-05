# ==============================================================================
# Bitnami Redis Cluster Deployment & Health Check Script (PowerShell)
# Microservice: rag-kb-catalog-service
# ==============================================================================

param(
    [string]$Namespace = "rag-system",
    [string]$ReleaseName = "redis-cluster",
    [string]$ValuesFile = "deployments/helm/redis-cluster/values.yaml"
)

$ErrorActionPreference = "Stop"

Write-Host "=== Deploying Self-Hosted Bitnami Redis Cluster ===" -ForegroundColor Cyan
Write-Host "Target Namespace: $Namespace"
Write-Host "Release Name:     $ReleaseName"

# 1. Verify helm
if (-not (Get-Command helm -ErrorAction SilentlyContinue)) {
    Write-Error "Helm is not installed or not in PATH. Please install Helm 3."
    exit 1
}

# 2. Add Bitnami repo
Write-Host "Updating Bitnami Helm repository..."
helm repo add bitnami https://charts.bitnami.com/bitnami --force-update
helm repo update bitnami

# 3. Create namespace
kubectl create namespace $Namespace --dry-run=client -o yaml | kubectl apply -f -

# 4. Deploy or Upgrade
Write-Host "Installing/Upgrading Redis Cluster via Helm..."
helm upgrade --install $ReleaseName bitnami/redis-cluster `
    --namespace $Namespace `
    -f $ValuesFile `
    --wait `
    --timeout 10m

# 5. Check rollout status
Write-Host "Waiting for Redis Cluster pods to reach Ready state..."
kubectl rollout status statefulset/$ReleaseName -n $Namespace --timeout=300s

Write-Host "=== Redis Cluster Successfully Deployed! ===" -ForegroundColor Green
Write-Host "Internal Cluster DNS: ${ReleaseName}-headless.${Namespace}.svc.cluster.local:6379"

