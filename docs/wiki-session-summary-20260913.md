# Wiki Search 检索测试与性能评测会话总结

**日期**：2026-09-13  
**模块**：`github.com/wuxujun/xktmcp`  
**会话目标**：验证当前运行在 8081 端口的 Wiki 检索与页面读取功能，支持对总结文档的检索读取；对反向链接候选桶和词典配置（`zh` vs `zh_s`）在 419 篇真实语料上进行细分测量与业务对比，并完成代码审查与分批提交。

---

## 一、本次会话完成的工作与核心成果

### 1. 8081 在线服务与 Wiki 检索测试套件实现
- **文件**：`cmd/server/wiki_search_live_test.go`
- **实现内容**：
  - **健康与就绪探针**：测试直连 `http://127.0.0.1:8081/health` 与 `/ready`，断言返回 HTTP 200 及 `status: ok` / `ready`。
  - **认证防御**：测试未携带凭据访问 `/mcp` 端点返回 HTTP 401 Unauthorized。
  - **真实租户工具调用**：从 `.env` 提取本地令牌，通过 MCP Streamable HTTP 传输层携带 `userId=0002`，调用 `wiki_search` 工具执行真实关键词（如“比赛”）查询，校验命中候选的标题、分值及 PageID；调用 `wiki_get_page` 提取首条命中文档的完整 Markdown 正文；验证不存在的标识符（如 `zzxxyywwqq`）返回 0 条结果。
  - **基于会话文档的隔离语料测试**：自动定位并读取 `docs/wiki-search-session-summary.md`，构建临时 Wiki 语料，测试以“反向链接”、“候选桶”、“GSE”、“优化会话总结”等词项进行精准召回，并通过 `wiki_get_page` 完整还原 Markdown 原始内容。

### 2. 真实 419 篇语料性能细分测量（候选桶机制验证）
- **测试用例**：`internal/wiki/search_corpus_profile_test.go` 中的 `TestLocalWikiCorpusProfile`
- **语料规模**：419 个 Markdown 文档，正文 1,070,089 字节（约 1.07 MB），词项槽位 155,517。
- **环境配置**：租户 `0002`（`tokenizer: "gse"`, `gse_dictionary: "zh_s"`）。
- **阶段测量结果对比**：
  | 测量阶段 / 指标 | 优化前（全量遍历） | 优化后（候选桶机制） | 改善幅度 |
  | :--- | :--- | :--- | :--- |
  | **反向链接构建 (`buildBacklinks`)** | **945 – 964 ms** | **14.35 – 15.51 ms** | **耗时下降 ~98.5%（加速 ~66 倍）** |
  | **倒排词项索引 (`term_index`)** | ~7 ms | **6.62 – 7.07 ms** | 保持高效毫秒级 |
  | **整体索引构建** | ~1100 ms | **417 – 431 ms** | **整体耗时下降约 60%** |
  | **词典初始化耗时** | 约 780 ms | **645 – 766 ms** | 堆增量 ~128.7 MiB |
  | **热搜索延迟 (p50)** | 0.61 – 0.74 ms | **0.32 – 0.84 ms** | 亚毫秒级稳定响应 |
- **核心结论**：候选桶索引彻底消除了反向链接阶段作为刷新首请求的主要性能瓶颈，无需额外实现更复杂的 PageID 后缀专用缓存。

### 3. GSE 词典业务召回对比（`zh` vs `zh_s`）
- **测试用例**：`TestLocalWikiDictionaryComparison`
- **测试范围**：对 419 篇真实文档执行 27 组分类业务关键词查询，对比简繁兼备词典（`zh`）与纯简体词典（`zh_s`）：
  - **专有名词与赛事**（`PBL`、`约翰洛克`、`Thinktown`、`FBLA`、`NEC`、`沃顿商赛` 等 8 组）：Top-1 与 Top-5 一致率 **100.0%**，总召回 35 条完全一致。
  - **英文与数字混合**（`1920年代`、`8–10年级`、`800–1000字` 等 5 组）：Top-1 与 Top-5 一致率 **100.0%**，总召回 24 条完全一致。
  - **简体通用业务短语**（`参赛资格`、`评审标准`、`辅导方案` 等 5 组）：Top-1 与 Top-5 一致率 **100.0%**，总召回 25 条完全一致。
  - **繁体中文**（`課程`、`項目制`、`寫作`、`商業競賽` 等 9 组）：Top-1 与 Top-5 一致率 **88.9%**。差异分析：查询 `項目制` 时，`zh` 命中《项目制与学术顾问课程设计》（score=21.0），而 `zh_s` 结果为 0（漏召回）。
  - **总体一致率**：**96.3%**（26/27 组完全一致）。
- **架构决策**：
  - `zh_s` 相比 `zh` 可减少 **34.5%** 的堆内存（128.7 MiB vs 196.5 MiB）。
  - 因繁体查询存在漏召回风险，**根配置与全局默认必须维持 `zh`**。
  - 租户 `0002` 明确仅涉及简体中文，可安全保持 `zh_s` 独立配置以节省资源。

### 4. 代码审查与分步规范提交
所有代码已通过 `gofmt` 与 `git diff --check`，并已划分为两个清晰的 Git Commit：
1. **Commit `20d633b`**：`feat: 优化 Wiki 本地检索分词、反向链接候选桶及性能测试`
   - 包含分词器扩展（`zh`/`zh_s`）、倒排索引构建、反向链接候选桶算法及全部基准测试（23 个文件，+1867 / -54）。
2. **Commit `fcc1859`**：`test: 增加 Wiki 在线检索与总结文档用例并优化日志脱敏`
   - 包含缓存日志 PII 掩码、8081 在线测试、总结文档语料测试与会话总结记录（4 个文件，+546 / -1）。
- 当前工作区状态：`working tree clean`。

---

## 二、重点验证命令与结果

```bash
# 1. 运行 8081 在线测试与会话总结检索测试
go test ./cmd/server -run TestLiveWikiSearchPort8081 -v
go test ./cmd/server -run TestWikiSearchAndReadSessionSummaryCorpus -v
# 结果：PASS (0.05s)

# 2. 运行 419 篇真实语料性能分析与阶段细分耗时
WIKI_PROFILE_CONFIG=/Users/xujunwu/Documents/IDEAProject/xktmcp/config/wiki.json go test ./internal/wiki -run TestLocalWikiCorpusProfile -v
# 结果：PASS (1.39s, backlinks_stage=15.5ms)

# 3. 运行真实语料 zh 与 zh_s 业务词库对比
WIKI_PROFILE_CONFIG=/Users/xujunwu/Documents/IDEAProject/xktmcp/config/wiki.json go test ./internal/wiki -run TestLocalWikiDictionaryComparison -v
# 结果：PASS (2.24s, 总体一致率 96.3%)

# 4. 全量串行回归验证与差异检查
go test -p 1 ./...
git diff --check
# 结果：全包通过，代码规范干净
```

---

## 三、后续建议与事项

1. **多租户配置推广**：其他租户若经确认无繁体查询需求，可按需在 `config/wiki.json` 指定 `"gse_dictionary": "zh_s"` 获得约 35% 的内存削减。
2. **远端同步**：本地提交完成后，可按需执行 `git push origin main` 推送至代码托管平台。
