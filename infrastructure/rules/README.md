# Prometheus Rules

这里放生产可用的最小告警规则。

当前目标：
- Query API 5xx 告警
- Query API p95 延迟告警
- DLQ 增长告警
- 熔断器打开告警
- Agent Run 失败、生命周期超时、p95 耗时告警
- LLM 连续 5 次失败、错误率和 p95 延迟告警

规则测试：

```bash
docker run --rm \
  -v "$PWD/infrastructure:/work" \
  -w /work/tests \
  prom/prometheus:latest \
  promtool test rules alert-rules.test.yml
```
