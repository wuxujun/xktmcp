# xktmcp 接口调用与集成指南

本文件详细阐述了 `xktmcp` 项目的接口设计、传输模式、认证机制，以及如何作为客户端集成调用此 MCP (Model Context Protocol) 服务。

---

## 目录

1. [项目架构与传输模式](#1-项目架构与传输模式)
2. [安全与身份认证](#2-安全与身份认证)
3. [MCP 工具调用规范 (JSON-RPC)](#3-mcp-工具调用规范-json-rpc)
    - [student_search (学员模糊搜索)](#student_search-学员模糊搜索)
    - [student_get (获取学员详细档案)](#student_get-获取学员详细档案)
    - [student_order (查询学员订单)](#student_order-查询学员订单)
    - [student_exam (查询学员成绩)](#student_exam-查询学员成绩)
    - [rag_search (企业知识库检索)](#rag_search-企业知识库检索)
    - [staff_search (组织机构与员工查询)](#staff_search-组织机构与员工查询)
4. [上游对接 REST API 规范](#4-上游对接-rest-api-规范)
5. [系统管理与监控接口](#5-系统管理与监控接口)

---

## 1. 项目架构与传输模式

`xktmcp` 服务是基于 Model Context Protocol (MCP) 标准构建的，用于将后台的学员管理、知识库、组织架构数据，以结构化“工具”的形式暴露给大语言模型或工作流编排引擎（如 n8n）。

服务支持以下三种传输通道（Transport）：

### A. Stdio (标准输入输出)
* **适用场景**：AI 客户端（如 Cursor、Claude Desktop、AGY CLI）在本地直接启动并以子进程管道通信。
* **启动方式**：
  ```bash
  ./mcp-server -transport=stdio
  ```

### B. SSE (Server-Sent Events)
* **适用场景**：基于 Web/远程的长连接事件流。
* **连接路径**：
  - **建立连接 (Event Stream)**: `GET http://localhost:8080/sse`
  - **发送消息 (Client Message)**: `POST http://localhost:8080/messages/?id=<client_id>`
* **启动方式**：
  ```bash
  ./mcp-server -transport=sse -port=8080
  ```

### C. Streamable HTTP
* **适用场景**：HTTP 单次请求/流式响应（主要适用于 n8n 等工作流编排组件）。
* **连接路径**：`POST http://localhost:8080/mcp`
* **启动方式**：
  ```bash
  ./mcp-server -transport=http -port=8080
  ```

---

## 2. 安全与身份认证

当使用 **SSE** 或 **Streamable HTTP** 网络传输时，服务将强制启用 `fail-closed` 认证。

### A. 请求头携带凭证
所有请求必须包含以下 Header：
```http
Authorization: Bearer <your_access_token>
```

### B. 多租户隔离 (ACL)
配置多租户时，服务在解析 Bearer Token 后查表（内存中已哈希存储）：
- 每个租户拥有独立限流速度（令牌桶控制，如 `rate_rps`）。
- 拥有工具白名单授权控制（如仅允许调用 `rag_search`）。

### C. IP 白名单直接旁路
如果在环境变量中配置了 `AUTH_IP_ALLOWLIST`（CIDR 网段列表）：
- 匹配信任网段的客户端请求将直接免去 Bearer Token 认证。
- 安全起见，只有在显式设置 `AUTH_TRUST_FORWARDED_HEADER=true` 时，服务才会信任 `X-Forwarded-For` 头。

---

## 3. MCP 工具调用规范 (JSON-RPC)

客户端请求 MCP 服务必须遵循 JSON-RPC 2.0 协议规范。

### 通用入参信封字段 (`CommonArgs`)
每个工具的 `params.arguments` 中都可以包含以下内部透传的信封字段（非必填，但在日志审计和 Trace 链路中起作用）：
- `sessionId` (string): 会话 ID
- `action` (string): 动作类型
- `chatInput` (string): 聊天原始输入
- `toolCallId` (string): 编排侧的唯一工具调用 Trace ID (推荐)
- `userId` (string): 发起查询的终端用户 ID (审计核心，必须安全校验)

---

### `student_search` (学员模糊搜索)
用于按姓名、手机号等模糊信息检索学员，并获取其唯一标识（`id` / `smp_id`）。

* **Request**:
```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "student_search",
    "arguments": {
      "query": "张三",
      "page": 1,
      "page_size": 20,
      "toolCallId": "n8n-trace-10029",
      "userId": "user_operator_abc"
    }
  },
  "id": 1
}
```

* **Response**:
```json
{
  "jsonrpc": "2.0",
  "result": {
    "content": [
      {
        "type": "text",
        "text": "[\n  {\n    \"id\": 1024,\n    \"stuid\": 9857,\n    \"smp_id\": \"SMP_STU_001\",\n    \"stu_name\": \"张三\",\n    \"gender\": \"男\",\n    \"grade\": \"高一\",\n    \"school_name\": \"第一中学\",\n    \"stu_status\": \"在读\",\n    \"userid\": \"u_zhangsan\"\n  }\n]"
      }
    ]
  },
  "id": 1
}
```

---

### `student_get` (获取学员详细档案)
根据学员唯一 ID 精确获取详细信息。

* **Request**:
```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "student_get",
    "arguments": {
      "id": "SMP_STU_001",
      "userId": "user_operator_abc"
    }
  },
  "id": 2
}
```

* **Response**:
```json
{
  "jsonrpc": "2.0",
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"id\": 1024,\n  \"stuid\": 9857,\n  \"smp_id\": \"SMP_STU_001\",\n  \"stu_name\": \"张三\",\n  \"gender\": \"男\",\n  \"grade\": \"高一\",\n  \"school_name\": \"第一中学\",\n  \"stu_status\": \"在读\",\n  \"userid\": \"u_zhangsan\"\n}"
      }
    ]
  },
  "id": 2
}
```

---

### `student_order` (查询学员订单)
根据精确的学员唯一 ID，获取其报班和缴费订单记录。

* **Request**:
```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "student_order",
    "arguments": {
      "id": "SMP_STU_001",
      "userId": "user_operator_abc"
    }
  },
  "id": 3
}
```

* **Response**:
```json
{
  "jsonrpc": "2.0",
  "result": {
    "content": [
      {
        "type": "text",
        "text": "[\n  {\n    \"id\": 99882,\n    \"stuid\": 9857,\n    \"smp_id\": \"SMP_STU_001\",\n    \"orders\": [\n      {\n        \"order_no\": \"ORD20260716001\",\n        \"course_name\": \"高中数学必修课程\",\n        \"price\": 1800.00,\n        \"status\": \"已完成\"\n      }\n    ]\n  }\n]"
      }
    ]
  },
  "id": 3
}
```

---

### `student_exam` (查询学员成绩)
根据精确的学员唯一 ID，获取历史考试成绩单。

* **Request**:
```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "student_exam",
    "arguments": {
      "id": "SMP_STU_001",
      "userId": "user_operator_abc"
    }
  },
  "id": 4
}
```

* **Response**:
```json
{
  "jsonrpc": "2.0",
  "result": {
    "content": [
      {
        "type": "text",
        "text": "[\n  {\n    \"id\": 7766,\n    \"stuid\": 9857,\n    \"smp_id\": \"SMP_STU_001\",\n    \"exams\": [\n      {\n        \"subject\": \"数学\",\n        \"score\": 142,\n        \"full_score\": 150,\n        \"exam_name\": \"高一期末联考\",\n        \"date\": \"2026-07-01\"\n      }\n    ]\n  }\n]"
      }
    ]
  },
  "id": 4
}
```

---

### `rag_search` (企业知识库检索)
用于向企业知识库发起事实性提问，获取关联上下文与引用来源。

* **Request**:
```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "rag_search",
    "arguments": {
      "query": "考勤异常怎么处理",
      "top_k": 3,
      "min_score": 0.3,
      "rewrite": true,
      "include_sources": true,
      "include_chunks": true,
      "userId": "user_operator_abc"
    }
  },
  "id": 5
}
```

* **Response**:
```json
{
  "jsonrpc": "2.0",
  "result": {
    "content": [
      {
        "type": "text",
        "text": "## 片段1\n标题: 员工考勤管理细则\n内容: 考勤异常需在3个工作日内提交线上补卡申请...\n来源: http://yk.xkt.com/docs/kq_rules.html"
      }
    ]
  },
  "id": 5
}
```

---

### `staff_search` (组织机构与员工查询)
查询教师、教职工的校区、院系、所教课程等机构信息。

* **Request**:
```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "staff_search",
    "arguments": {
      "query": "王老师",
      "userId": "user_operator_abc"
    }
  },
  "id": 6
}
```

* **Response**:
```json
{
  "jsonrpc": "2.0",
  "result": {
    "content": [
      {
        "type": "text",
        "text": "[\n  {\n    \"userid\": \"t_wangwu\",\n    \"name\": \"王五\",\n    \"department_name\": \"理学部数学组\",\n    \"campus_name\": \"城南校区\",\n    \"gender\": \"男\",\n    \"position\": \"高级教师\",\n    \"entry_date\": \"2020-09-01\"\n  }\n]"
      }
    ]
  },
  "id": 6
}
```

---

## 4. 上游对接 REST API 规范

`xktmcp` 服务器作为代理网关，在接收到上述 MCP 工具请求后，会将其转发到配置的 `BASE_URL` 后端服务。

### A. API 鉴权
转发请求时，会自动在 Header 中附带上游专用的授权 Token：
```http
Authorization: Bearer <API_TOKEN_FROM_ENV>
Accept: application/json
```

### B. 接口路由对照表

| 业务分类 | 上游 REST 端点 | HTTP 动词 | 请求 Query 映射 |
|--------|--------------|-----------|----------------|
| 学员基本搜索 | `/api/student` | `GET` | `query={name}&page={page}&page_size={page_size}` |
| 学员订单检索 | `/api/student/order` | `GET` | `query={smp_id}` |
| 学员考试检索 | `/api/student/exam` | `GET` | `query={smp_id}` |
| 学员单卡详情 | `/api/student/{id}` | `GET` | 路径参数映射 `{id}` |
| 知识库检索 | `/api/ai/rag/search` | `GET` | `userId={userId}&query={query}` |
| 机构与员工搜索 | `/api/staff` | `GET` | `userid={userId}&query={query}` |

*注意：所有上游请求均自动注入熔断器保护与 3 次指数退避重试（间隔以 100ms * 2^N 计算）。*

---

## 5. 系统管理与监控接口

除了核心业务接口，服务在 `SSE` 或 `HTTP` 网络模式下还会暴露管理性端口：

### A. 存活探针
- **路径**：`GET http://localhost:8080/health`
- **响应**：
  ```json
  {"status":"ok"}
  ```

### B. Prometheus 指标监控
- **路径**：`GET http://localhost:8080/metrics`
- **说明**：支持输出系统基础指标（Go 协程、垃圾回收）以及以下业务专属性能统计：
  - `xkt_tool_calls_total{tool, status}`: 工具累计调用量
  - `xkt_tool_duration_seconds{tool}`: 耗时分布直方图
  - `xkt_cache_hits_total{tool}`: 缓存命中次数
  - `xkt_cache_misses_total{tool}`: 缓存穿透（未命中）次数
  - `xkt_circuit_breaker_transitions_total{name, to_state}`: 熔断器状态跃迁次数（可作为微服务健康预警的关键触发项）
