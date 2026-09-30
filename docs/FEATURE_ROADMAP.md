# Project Roadmap & Expansion Opportunities

Based on the current architecture of `xktmcp`, the following areas have been identified for future development and optimization:

## 1. Business Domain Expansion (业务领域扩展)
The modular design allows for easy addition of new service modules:
- **Faculty Management**: Add tools like `teacher_search` or `staff_management`.
- **Campus Resources**: Integrate services such as `room_booking` or `facility_status`.
- **Scheduling & Notifications**: Implement `schedule_query` and `notification_send` to allow the LLM to manage student calendars.

## 2. RAG & Search Enhancement (RAG 与搜索能力增强)
Enhance the intelligence of information retrieval:
- **Hybrid Search**: Integrate vector search with traditional keyword matching (BM25) in the service layer.
- **Multi-hop Reasoning Support**: Optimize tool definitions to facilitate multi-step reasoning flows for complex queries.
- **Advanced Query Rewrite Pipeline**: Implement more sophisticated rewriting logic using multiple LLM passes or hybrid rule-based systems.

## 3. Engineering & Infrastructure (工程化与系统增强)
Improve the reliability and observability of the server:
- **Observability**: Integrate Prometheus metrics and OpenTelemetry to track tool latency, API success rates, and rewrite accuracy.
- **Caching Layer**: Implement a caching system (e.g., Redis) in the client/service layer for high-frequency, non-volatile data.
- **Stream Optimization**: Refine the chunking logic for SSE to ensure smoother delivery of large RAG results.

## 4. Security & Performance (安全与性能优化)
Ensure the system can handle production scale:
- **Dynamic Rate Limiting**: Implement per-user/IP rate limiting to prevent resource exhaustion.
- **Circuit Breakers**: Add circuit breaker logic in `internal/client` to gracefully handle upstream service failures.

---
*Generated on 2026-06-09*
