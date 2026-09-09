# Documentation Archive

历史学习日志、旧 backlog、面试材料和早期演示设计已移出工作树，避免检索时把过时架构读进上下文。

需要查阅时从 Git 历史读取删除前提交：

```bash
git log -- docs/archive/
git log -1 -- docs/archive/LEARNINGS.codex-2026-09-05.md
git show <commit>:docs/archive/LEARNINGS.codex-2026-09-05.md
```

当前执行状态只以根目录 `LEARNINGS.codex.md`、`docs/backlog.md` 和对应设计/验收文档为准。
