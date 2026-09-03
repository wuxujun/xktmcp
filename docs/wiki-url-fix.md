当前这种写法只是纯文本 URI，ChatGPT 或其他客户端不一定会自动识别为可点击链接。即使写成 Markdown：

  [打开页面](wiki://page/xxx)

  多数客户端也会因为 wiki:// 是自定义协议而禁止直接跳转。

  建议采用三层方案：

  1. 跨 MCP 客户端：返回结构化 ResourceLink

  不要只返回 URI 字符串，而是在 wiki_search 等工具结果中追加：

  &mcp.ResourceLink{
      URI:         resourceURI,
      Name:        item.PageID,
      Title:       item.Title,
      Description: item.Summary,
      MIMEType:    "text/markdown",
  }

  项目使用的 go-sdk v1.7.0 已支持 mcp.ResourceLink。支持该类型的客户端可以显示成带标题的资源入口；不支持的客户端仍能看到 URI。

  理想效果会变成：

  主要 Wiki 来源：

  - PBL 课程设计指南
  - 课程评价规范
  - 项目实施流程

  点击标题后，由客户端调用 resources/read 读取对应 wiki://page/...。

  不过，各客户端是否将 ResourceLink 渲染成可点击入口属于客户端行为，不能保证完全一致。OpenAI 官方也说明 Resources 是“客户端可读取的数据”，而
  ChatGPT 插件的主要调用入口仍然是 Tools。OpenAI MCP Server 文档

  2. ChatGPT 最佳体验：增加 MCP Apps 页面组件

  为搜索结果增加一个列表卡片，每条显示：

  标题
  摘要
  [打开正文]

  点击后调用 wiki_get_page，在 ChatGPT 内展开正文。这不依赖 ChatGPT 是否把 wiki:// 自动识别成超链接。

  ChatGPT 官方推荐通过 _meta.ui.resourceUri 将工具与 UI Resource 关联，组件可以调用 MCP 工具并展示结果。ChatGPT MCP Apps UI 文档

  3. 浏览器通用跳转：提供 HTTPS 地址

  如果要求在任何聊天客户端中都能点击，需要提供真正的网页地址，例如：

  https://wiki.example.com/pages/{page_key}

  然后输出：

  [打开《PBL 课程设计指南》](https://wiki.example.com/pages/...)

  但该方案需要额外处理网页登录、租户隔离和短期签名 URL；不能把 Bearer Token 放进链接参数。

  对当前项目，建议下一步先实现方案 1：

  - wiki_search 返回 ResourceLink
  - 保留现有 JSON/文本结果作为兼容回退
  - ResourceLink 使用页面标题，不直接展示 Base64 key
  - 增加 Postman、ChatGPT Desktop、VS Code 三端验证