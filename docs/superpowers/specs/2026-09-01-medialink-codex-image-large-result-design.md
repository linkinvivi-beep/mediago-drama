# MediaLink：Codex 大图片结果接收修复设计

日期：2026-09-01

状态：设计已确认，等待书面规格复审

## 目标

修复 `codex.imagegen` 已成功生成图片、但 MediaLink 因 app-server 消息过大而断线并显示失败的问题。继续使用本机 Codex 的 ChatGPT 登录和内置 `$imagegen`，不改用 OpenAI Images API，不增加 API Key。

## 已确认根因

Codex 已生成一张 1672×941、1,823,680 字节的有效 PNG，并返回结构化 `imageGeneration` item。MediaLink 的 JSON-RPC stdio 客户端使用 `bufio.Scanner` 默认约 64 KiB token 上限；完成事件携带大体积结果时触发 scanner token-too-long，连接关闭。Provider 已持久化 thread/turn/item ID，因此退化为 `waiting_reconnect`，但没有导入已生成文件。并发预检又被同一 FIFO 队列阻塞，最终表现为 `context deadline exceeded`。

## 修改范围

1. 只调整 Codex app-server stdio 解码边界，为 scanner 设置显式、受控的最大消息大小；上限需覆盖当前 Codex 图片事件和 MediaLink 已允许的最大图片结果，同时拒绝无界输入。
2. 保持逐行 JSON-RPC、结构化 `imageGeneration` item、`savedPath` 校验和正式素材导入流程不变。
3. 读取失败必须保留可诊断错误类型，不能把 token-too-long、进程退出和网络重连统一压成无信息的生成失败。
4. 已有 thread ID 的任务只读取原 thread，不重新发起生图；修复不得引入重复计费或重复 turn。
5. 能力预检不得在另一个生图任务持有生成 FIFO 时长时间等待。预检使用独立的只读能力通道或短生命周期 session；它不能提交 turn，也不能影响正在生成的 session。

## 安全边界

- 消息大小上限必须是常量并有测试，不允许改成无限缓冲。
- 结果仍需通过路径边界、普通文件、格式、尺寸、像素和完整解码校验后才能进入素材库。
- 日志不得记录图片 base64、ChatGPT 凭据或完整 app-server 响应。
- 内容策略、额度和结构化 item 失败继续按原错误处理，不伪装成连接问题。

## 测试与验收

- 新增超过默认 scanner 上限的结构化图片完成事件回归测试，证明 session 能读到 `savedPath` 并正常结束 turn。
- 新增超过 MediaLink 显式上限的消息测试，证明客户端安全失败且错误可识别。
- 新增“生成中并发预检”测试，证明预检不会等待生成 FIFO 到请求超时。
- 保留并运行现有 Codex session、provider、任务恢复和素材路径安全测试。
- 使用已生成的本地 PNG进行无额度导入验证；不得为了自动化测试再次触发真实 Codex 生图。
- 最终由用户单独确认后，才执行一次新的真实生图验收。

## 完成标准

Codex 返回大图片事件时 MediaLink 不再断线；任务以原 thread/turn/item 身份完成，图片经验证进入素材库。并发预检快速返回真实能力状态，不再误报 `context deadline exceeded`，且整个修复不改变现有生成工作流和供应商路由。
