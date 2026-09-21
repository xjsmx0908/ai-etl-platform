# Prometheus Rules

这里放生产可用的最小告警规则。

| 文件 | 覆盖范围 |
| --- | --- |
| `etl-alerts.yml` | 应用层：Query API、DLQ、熔断、Agent Run、LLM、索引 generation、文档删除、SCIM、发布中心 |
| `host-alerts.yml` | 宿主机层：根分区可用空间 |

当前目标：
- Query API 5xx 告警
- Query API p95 延迟告警
- DLQ 增长告警
- 熔断器打开告警
- Agent Run 失败、生命周期超时、p95 耗时告警
- LLM 连续 5 次失败、错误率和 p95 延迟告警
- Index generation 构建卡住、失败、双后端分歧、修复耗尽和清理失败告警
- **宿主机根分区可用空间低于 15%（warning）/ 10%（critical）**

宿主机磁盘告警依赖 `node-exporter`（定义在 `docker-compose.yml`，抓取配置在
`infrastructure/prometheus.yml`）。**没有这个服务就没有 `node_filesystem_avail_bytes`，
规则不会报错、只会永远不响** —— 这是加规则时最容易漏掉的一步。

阈值是按「剩余百分比」定的，与 Elasticsearch 的水位（`docker-compose.yml` 的
`ES_DISK_WATERMARK_*`，按**已用百分比**）构成一条升序的防线：
告警 15% → 10% free，ES 停分配 95% → 迁移 96% → 只读 97% used。
改任何一边都要检查这条升序还成立。

规则测试：

```bash
docker run --rm \
  -v "$PWD/infrastructure:/work" \
  -w /work/tests \
  --entrypoint promtool \
  prom/prometheus:latest \
  test rules alert-rules.test.yml
```

`alert-rules.test.yml` 的 `rule_files` 是显式列举的 —— 新增规则文件必须同步登记，
否则测试跑不到它，而结果仍然是绿的。

写 `input_series` 时**不要在一个 `values` 字符串里混写两个 `value xN`**
（例如 `'16e9x6 100e9x6'`）：它不会按字面展开，会静默产生另一条序列，
测试于是看起来在测某件事、实际没有。要分段就写显式样本列表。
