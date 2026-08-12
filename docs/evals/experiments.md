# Retrieval 对照实验

> 目的：在语义集（`docs/evals/semantic-golden-set.json`）上量化不同检索配置的真实收益。
> 数据集 Recall@5 基线 63.89%，pass_rate 69.05%（见 `real-baseline-findings.md` §5）。
> 判定标准：F-03 确立的显著性下限——pass_rate 差异需超过 4.26% 才算真实差异。

## 实验矩阵

| 实验 | 变量 | 结论 |
| --- | --- | --- |
| E1 | ES on（hybrid）vs ES off（纯 Qdrant） | **hybrid 保留**（ES 净增益 +7 例） |
| E2 | rerank on vs off | **英文 rerank 有害，应关闭** |
| E3 | candidateK 50 vs 200 | 低优先级（目标都在 50 候选内） |

---

## E1: ES 对中文语义检索的影响

### 配置
- 对照 A（hybrid，现状）：`RETRIEVAL_ENABLE_ES=true`（默认）
- 对照 B（纯 Qdrant）：`RETRIEVAL_ENABLE_ES=false`
- 其余配置相同：nomic-embed-text 768d，rerank auto，topK 5，candidateK 50

### 结果

| 指标 | hybrid (ES on) | 纯 Qdrant (ES off) | 差异 |
| --- | --- | --- | --- |
| pass_rate | 69.05% | 52.38% | **-16.67pp** |
| Recall@1 | 19.44% | — | |
| Recall@5 | 63.89% | — | |
| 通过 case 数 | 29/42 | 22/42 | -7 |

### Case 级差异
- **ES 帮助 9 例**：sem-003, 006, 007, 008, 009, 022, 025, 030, 032
- **ES 有害 2 例**：sem-017, 018
- **两者都失败 11 例**：sem-005, 010, 013, 016, 019, 023, 028, 033, 038, 039, 040

### 结论
1. **ES BM25 对中文不是噪音，而是真实增益**（+7 例，远超显著性下限）。「ES 对中文单字切分所以无效」的直觉假设被证伪。
2. **11 例两者都失败是系统真实短板**——不依赖 ES，根因是 nomic-embed-text（137M）对中文口语 query 的语义理解有限，以及语料同质（企业知识库文档主题相近）。
3. hybrid 保留。

---

## E2: Rerank 对中文语义检索的影响

### 配置
- 对照 A（rerank on，现状）：`RETRIEVAL_ENABLE_RERANK=true`, policy=auto，`ms-marco-MiniLM-L6-v2`
- 对照 B（rerank off）：`RETRIEVAL_ENABLE_RERANK=false`
- 其余配置相同（ES on，nomic-embed-text 768d，topK 5）

### 结果

| 指标 | rerank on | rerank off | 差异 |
| --- | --- | --- | --- |
| pass_rate | 69.05% | 73.81% | **+4.76pp** |
| 通过 case 数 | 29/42 | 31/42 | +2 |

### Case 级差异
- **rerank 有益：0 例**（没有任何 case 因为 rerank 从失败变通过）
- **rerank 有害：2 例**（sem-017, 018）
- **两者都失败 11 例**（与 E1 相同的 11 例）

### 结论
1. **`ms-marco-MiniLM-L6-v2`（英文 Cross-Encoder）对中文语义检索零帮助，还略微有害。**
   它由英文问答数据集训练，不适用中文。差值 +4.76pp 超过显著性下限，是真实差异而非噪声。
2. **配置建议：在当前英文 reranker 下，`RETRIEVAL_ENABLE_RERANK` 应默认关闭。**
   或替换为中文 reranker（如 bge-reranker-v2-m3）后重测。
3. 注意：该结论只影响语义/hybrid 查询路径。锚点集走 exact_keyword 路径，`auto` policy
   下不触发 rerank，因此锚点集行为不受影响——但锚点集本身已不作为质量指标。

---

## 共同结论与改进方向

### 配置决策总结（E1 + E2）

| 配置 | 现状 | 建议 |
| --- | --- | --- |
| ES 混合检索 | on | 保留（净增益 +7 例） |
| 英文 reranker | on | **关闭**（或换中文 reranker） |
| candidateK 50 | — | 保持（目标都在候选内） |

### 11 例共同失败的根因

query 特征：抽象、口语化、指代模糊（「这样」「那个」「怎么」）。正式文档用具体词
（「切块」「三份副本」「版本历史」）。nomic-embed-text 对跨表达（口语→书面）的语义
映射理解不足。

### 潜在改进方向（供 T-19 面试叙事）

1. **换更大的中文 embedding**（bge-m3 / bge-large-zh）——需重新灌库，collection 维度变更
2. **query 改写**：检索前先用 LLM 把口语 query 改写为检索友好的书面表达
3. **中文 reranker**：用 bge-reranker-v2-m3 替代英文 Cross-Encoder
4. **RRF 权重调优**：当前 semantic 路由 Qdrant 0.8 / ES 0.2
