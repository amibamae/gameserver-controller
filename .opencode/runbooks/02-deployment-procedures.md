# Deployment Procedures for Observability Stack

**Document Version:** 0.1.0  
**Last Updated:** 2026-11-21  
**Target Environment:** Staging → Production  

---

## Pre-Deployment Checklist

### Metrics Integration

1. **Expose Prometheus endpoint:**
   ```bash
   # Add metrics handler to server startup
   go run ./cmd/server.go \
     --metrics-path=/metrics \
     --metrics-interval=5s
   
   # Verify endpoint responds
   curl http://localhost:8080/metrics | grep "gameserver_controller"
   ```

2. **Configure Prometheus scraping:**
   ```yaml
   # prometheus.yml scrape_configs section
   - job_name: 'gameserver-controller'
     static_configs:
       - targets: ['controller-1:8080', 'controller-2:8080']
     metrics_path: '/metrics'
     scrape_interval: 5s
   ```

3. **Deploy alerting rules:**
   ```bash
   # Copy rules to Prometheus
   cp prometheus/alerting_rules.go /etc/prometheus/
   
   # Generate deployment manifest
   go run ./prometheus -generate-manifest > prometheus/deployment.yaml
   
   # Deploy to Prometheus alertmanager
   kubectl apply -f prometheus/deployment.yaml
   ```

### Health Endpoint Verification

```bash
# Verify health endpoint responds with HTTP 200
curl -X GET http://localhost:8080/health | jq .

# Expected output:
# {
#   "status": "healthy",
#   "timestamp": "2026-11-21T...",
#   "store_accessible": true
# }

# Verify readiness endpoint
curl -X GET http://localhost:8080/ready | jq .

# Expected output:
# {
#   "ready": true,
#   "timestamp": "2026-11-21T..."
# }
```

---

## Integration Testing

### Run Crash Recovery Tests (Staging Only)

```bash
cd gameserver-controller

# Skip integration tests in production (marked with testing.Short())
go test -v ./internal/store/crash_recovery_test.go \
  -run "TestCrashRecovery" \
  -timeout=5m \
  -count=1

# Verify metrics export
curl http://localhost:8080/metrics | grep gameserver_controller

# Check alerting rules compile
go run ./prometheus -validate
```

### Benchmark Observability Overhead

```bash
# Measure latency overhead of metrics collection
go test -bench=. -benchmem ./internal/metrics/...

# Acceptable thresholds:
# - /health: <10ms p99
# - /metrics: <50ms p99  
# - Write duration impact: <5% increase in 50th percentile
```

---

## Environment Variables

### Prometheus Configuration

```bash
export PROMETHEUS_URL="http://prometheus.prometheus.svc.cluster.local:9090"
export PROMETHEUS_PATH="/api/v1/query"
export ALERTMANAGER_URL="http://alertmanager.alerting.svc.cluster.local:9093"

# Add to deployment manifest
envFrom:
  - configMapKeyRef:
      name: controller-metrics-env
```

### Alert Routing Configuration

```bash
# Define alert routing in alertmanager config.yaml
route:
  receiver: 'gameserver-critical-alerts'
  group_by: ['severity', 'team']
  
receivers:
  - name: 'gameserver-critical-alerts'
    pagerduty_service_key: '${PAGERDUTY_API_KEY}'
    
  - name: 'gameserver-warnings'
    slack_webhook_url: '${SLACK_WEBHOOK_URL}'
```

---

## Monitoring Dashboard Setup (Grafana)

### Metrics Query Examples

```promql
# Overall health status
up == 1 and 
gameserver_controller_adapter_health == 1

# Error rate over time
rate(gameserver_controller_store_operations_total{status="error"}[5m])

# Recovery frequency alert
rate(gameserver_controller_store_recoveries_total{cause="crash"}[1h]) > 0.1

# Latency p99 histogram
histogram_quantile(0.99, 
  rate(gameserver_controller_store_write_duration_seconds_bucket[5m]))
```

### Grafana Dashboard JSON (Example)

Create `grafana/dashboard.json` with panels for:
- Store operations throughput
- Recovery events over time  
- Error rate trends
- Latency percentiles

---

## Rollback Procedures

If observability implementation causes issues:

1. **Disable metrics collection:**
   ```bash
   # Stop exposing /metrics endpoint
   # Or set empty metrics path in server config
   
   go run ./cmd/server.go --metrics-path="" \
     --disable-metrics
   ```

2. **Remove alerting rules from Prometheus:**
   ```bash
   kubectl delete -f prometheus/alerting-rules.yaml
   kubectl delete -f prometheus/deployment.yaml
   ```

3. **Restore previous server binary:**
   ```bash
   git checkout main -- cmd/server.go internal/store/ \
     # Exclude new metrics package temporarily
   
   git clean -fd  # Remove untracked files
   ```

---

## Production Deployment Script

Create `scripts/deploy-observability.sh`:

```bash
#!/bin/bash
set -e

cd /app/gameserver-controller

echo "=== Deploying Observability Stack ==="

# 1. Build with metrics enabled
go build -ldflags="-X main.Version=$(git describe --tags)" \
  -o bin/server .

# 2. Verify health endpoint in container
docker run --rm \
  -p 8080:8080 \
  app/gameserver-controller:$BUILD_TAG \
  /bin/server --metrics-path=/health

# 3. Test Prometheus scraping
curl http://localhost:8080/metrics | grep gameserver_controller > /dev/null || exit 1

echo "=== Observability deployment verified ==="
```

---

## Monitoring Alerts Configuration

### Critical Alert Thresholds

| Metric | Warning | Critical | Team | Response Time |
|--------|---------|----------|------|---------------|
| Recovery rate (>0.1/min) | N/A | 0.1/min | Reliability | Immediate |
| Error rate | 0.05% | 0.1% | Reliability | 15 min |
| Latency p99 | 300ms | 500ms | Platform | 10 min |
| Adapter health == 0 | N/A | N/A | Platform | Immediate |

### Info Alerts (No Immediate Action)

- Recovery completed automatically
- Backup verification successful within SLA window

---

## Next Steps After Deployment

1. **Week 1:** Monitor recovery events and error rates in staging
2. **Week 2:** Verify alert routing to PagerDuty/Slack
3. **Week 3:** Run crash simulation tests with realistic load
4. **Month 1:** Evaluate metrics against SLO goals (error budget consumption)

---

**Document Status:** Initial version  
**Review Date:** End of quarter or after incident  
