# MediaLink：AutoDL Z-Image 生图与多实例调度设计

日期：2026-08-30

状态：设计已确认，等待书面规格审阅

关联规格：`2026-08-30-medialink-codex-autodl-design.md`

## 1. 目标

在不改写 MediaGo Drama 核心工作流的前提下，为 MediaLink 增加第二条可见图片生成路线 `AutoDL · Z-Image`。人物、场景、道具、分镜、生成工作台和 Agent 继续使用现有统一生图入口；用户可以显式选择 Codex 或 AutoDL Z-Image，系统记住上次选择，但不会在故障时自动切换供应商。

Z-Image 与 MiniMax H3 共用可配置的 AutoDL 多实例连接池。不同实例可以并行运行；每个实例内部一次只运行一个 ComfyUI job，避免同一 GPU 上的图片与视频任务争抢显存。

## 2. 非目标

- 不新增独立的“图生图页面”，继续使用现有“提示词 + 可选参考图”交互。
- 不改写人物、场景、道具、分镜、素材、历史或文档关系模型。
- 不自动启动、关闭、租用、续费或删除 AutoDL 实例。
- 不把 ComfyUI 暴露到公网。
- 不把 Z-Image 失败静默降级为 Codex，也不把 Codex 失败静默改派到 Z-Image。
- 不在首版合并多张参考图、丢弃多余参考图或猜测工作流节点。
- 不在没有单独确认时提交真实 `/prompt` 或消耗 Codex/GPU 额度。
- 不为此功能做无关供应商删除、目录重写或大面积内部重命名。

## 3. 产品路由

MediaLink 的可见图片路由为：

| 路由 ID | 显示名称 | 执行方式 | 参考图能力 |
| --- | --- | --- | --- |
| `codex.imagegen` | `Codex 生图` | Codex 内置 `$imagegen` | 沿用 Codex route 能力 |
| `autodl.zimage` | `AutoDL · Z-Image` | SSH 隧道后的 ComfyUI | 首版 0 或 1 张 |

路由选择出现在所有现有图片生成入口中。上次选择作为现有生成偏好保存。失败、离线、额度不足或队列繁忙时，不自动切换图片路由。

## 4. AutoDL 多实例模型

### 4.1 实例配置

用户可以添加多个命名实例。每个实例保存以下非秘密信息：

- 稳定的 `instanceProfileId`
- 用户可编辑名称
- 从登录指令解析出的 SSH 主机、端口和用户名
- 可配置的 ComfyUI 远程端口，默认 `6006`
- 已确认的 SSH host fingerprint
- macOS Keychain opaque credential reference
- 启用状态
- 最近健康检查状态和时间
- 每个工作流 profile 在该实例上的验证结果

设置页接受标准 OpenSSH 登录指令，例如 `ssh -p 12345 root@example.com`。解析器只允许连接所需参数，不执行 shell；管道、重定向、命令替换、`ProxyCommand`、远程命令和其他可执行扩展必须拒绝。数据库保存解析结果而不是原始命令。

密码通过独立密码框写入 macOS Keychain，不得出现在数据库、API 响应、前端持久状态、进程参数、任务 runtime state 或日志中。用户替换登录指令时保留工作流 profile；主机、端口或 fingerprint 变化后必须重新扫描并明确确认。

### 4.2 隧道

每个在线实例拥有独立隧道：

```text
本机 127.0.0.1:<随机端口>
  -> 该实例 SSH 连接
  -> 远端 127.0.0.1:<该实例的 ComfyUI 端口>
```

本机监听和远端目标都限定为 loopback。多个实例可以同时建立不同的本地随机端口；远端 ComfyUI 端口不写死为 `6006`。

### 4.3 能力验证

实例连接后检查 `/system_stats`、`/object_info` 和 `/queue`，并分别验证已配置的 Z-Image 文生图、Z-Image 图生图、MiniMax H3 工作流。能力是“实例 + workflow profile”的验证结果，不能只依据历史日志、旧地址或另一个实例推断。

## 5. Z-Image 工作流 profiles

首版支持两个 profile kind：

| Kind | 用户工作流 | 触发条件 |
| --- | --- | --- |
| `zimage-t2i` | `Z-Image-Turbo-NSFW-BF16-v2-文生图.json` | 0 张参考图 |
| `zimage-i2i` | `Z-Image-Turbo-NSFW-BF16-v2-普通图生图.json` | 恰好 1 张参考图 |

每个 profile 包含：

- 稳定 ID、名称、kind 和版本
- 原始 ComfyUI API-format workflow JSON
- prompt、seed、宽高或尺寸、参考图、输出前缀的语义 manifest
- 所需节点与模型声明
- workflow 内容摘要

导入时拒绝顶层包含 UI `nodes` / `links` 而不是 API prompt map 的 JSON。业务代码不硬编码用户当前节点 ID；实例化只修改 manifest 明确绑定的字段。真实 JSON 尚未提供并不阻塞连接池和 profile 框架设计，但 Z-Image provider 验收前必须导入这两个文件，并在目标实例上完成无生成验证。

## 6. 提示词与参考图

- 未开启“优化提示词”时，用户原文直接注入选定 workflow。
- 开启时继续使用现有 Codex 文本优化器，不新增第三方文本 API。
- `codex.imagegen` 使用 Codex 图像定向优化指令。
- `autodl.zimage` 使用 Z-Image 定向优化指令，结果仍写入现有 `optimizedPrompt` 和历史字段。
- 0 张参考图自动选择 `zimage-t2i`。
- 1 张参考图自动选择 `zimage-i2i`，并上传到任务专属 ComfyUI 子目录。
- 超过 1 张参考图时在提交前明确拒绝；不得自动拼图、丢图或只取第一张。

## 7. 自动调度与手动指定

### 7.1 自动模式

默认从“已启用、在线、当前空闲、目标 workflow 已验证”的实例中选择节点。同等候选采用轮询，避免持续偏向同一实例。不同实例可并行运行；每个实例只有一个执行槽，Z-Image 和 H3 共用该槽。

没有兼容实例时，任务进入 `waiting_for_instance`，保留在队列中而不是失败或改用其他图片供应商。实例恢复或配置更新后重新参与调度。

### 7.2 高级手动指定

用户可以为单次或批次任务指定 `instanceProfileId`。指定实例离线、繁忙或缺少目标 workflow 时，任务进入 `waiting_for_selected_instance`；不得静默改派。

### 7.3 绑定与改派边界

- 在上传或 `/prompt` 提交前，若能证明远端尚未收到任务，自动模式可以释放 reservation 并选择另一个兼容实例。
- 开始上传后任务即绑定实例；最迟在提交前持久化 `instanceProfileId` 和 workflow snapshot。
- 获得 `prompt_id` 后必须持久化 `instanceProfileId + prompt_id`，后续查询不能换实例。
- `/prompt` 返回前断线属于 `submission_outcome_unknown`；不得改派或自动重提。
- 手动重试创建新 attempt，并明确关联旧 attempt。

## 8. 单任务数据流

```text
现有图片生成入口
  -> 显式选择 autodl.zimage
  -> 校验参考图数量并选择 profile kind
  -> 可选的 Z-Image 定向提示词优化
  -> 自动调度或验证手动指定实例
  -> 建立/复用该实例的 SSH 隧道
  -> 实时验证 ComfyUI 与 workflow
  -> 预留实例执行槽
  -> 可选上传唯一参考图
  -> 用 manifest 实例化 workflow
  -> /prompt 只提交一次
  -> 持久化 instanceProfileId、prompt_id、profile/version/snapshot
  -> 查询同一实例的 queue/history
  -> 下载并验证输出
  -> 导入现有 MediaLink 素材库和文档关系
  -> 释放实例执行槽
```

图片批量任务仍拆成独立 generation task。调度器可以把不同任务分散到多个实例，但同一任务只有一个远端身份。

## 9. 恢复、取消与错误

- 应用重启后按 `instanceProfileId + prompt_id` 恢复，绝不按“任意在线实例”搜索。
- 动态地址变化但仍为同一实例时，用户更新该 profile 的 SSH 指令并重新确认 fingerprint，原任务继续查询。
- 实例被销毁或 ComfyUI history 不再包含该 `prompt_id` 时，任务标记 `remote_task_lost`，不自动重生。
- 已有远端身份但暂时无法连接时标记 `waiting_reconnect`。
- 取消尚未提交的本地排队任务不会调用 ComfyUI。
- 对已经排入 ComfyUI 队列的任务，仅在 API 能按精确 `prompt_id` 删除时执行远端取消。
- 不调用可能中断其他实例任务或当前不相关任务的全局 `/interrupt`。
- OOM、缺失节点、缺失模型、工作流验证失败、上传失败、输出缺失和文件校验失败分别保留稳定错误码。

下载的图片必须通过类型、最大文件大小、最大尺寸、最大像素量和完整解码验证，再进入已有资产导入流程。远端路径和原始 ComfyUI 响应不得直接作为公开素材 URL。

## 10. 设置与任务界面

设置页新增 AutoDL 实例列表，可添加、编辑、启用、停用和测试实例。每张实例卡显示解析后的地址、远程 ComfyUI 端口、fingerprint 状态、连接状态、当前任务及三个工作流能力状态。密码始终以“已保存/未保存”呈现，不能回显。

Z-Image workflow 设置支持导入、配置语义映射、选择目标实例验证、显示版本和失败原因。生成设置默认显示自动分配；高级设置提供实例选择。任务历史显示自动/手动调度、绑定实例、profile 版本、`prompt_id`、等待原因和恢复状态。

## 11. 测试与验收

### 11.1 无消耗自动测试

- OpenSSH 指令解析白名单和恶意参数拒绝。
- 多实例 tunnel 生命周期、不同远程端口和 fingerprint 变化。
- Keychain 成功、失败、替换、清除及日志脱敏。
- API-format workflow、manifest、节点、模型和输出验证。
- 0/1/多参考图分流。
- 自动轮询、实例级串行、跨实例并行和手动固定实例等待。
- 提交前安全改派、提交结果未知禁止改派。
- `instanceProfileId + prompt_id` checkpoint、断线和重启恢复。
- 精确取消、远程任务丢失、OOM 和输出校验。
- 结果进入原有素材库并保持人物、场景、道具和分镜关系。

所有自动测试使用 fake SSH、fake Keychain 和 fake ComfyUI，不提交真实任务。

### 11.2 真实环境分阶段验收

第一阶段只连接当前 AutoDL 实例，不生成：

1. 解析当前 SSH 指令并建立 loopback tunnel。
2. 检查 ComfyUI API、节点和模型。
3. 取得并验证两个真实 Z-Image JSON，确认 manifest 映射。
4. 若有两个实例，确认可同时连接、能力独立且不串号。

第二阶段会消耗 GPU，必须再次取得用户明确确认：

1. 生成一张 Z-Image 文生图并导入对应素材关系。
2. 用一张现有素材生成一张 Z-Image 图生图。
3. 验证两个实例可并行处理两个独立图片任务。
4. 在不重复提交的前提下验证一次断线或应用重启恢复。

## 12. 完成标准

- 所有现有图片入口都可显式选择 Codex 或 AutoDL Z-Image。
- Z-Image 根据 0/1 张参考图选择经过验证的真实 workflow profile。
- 用户可维护多个动态 AutoDL 实例，密码只进入 macOS Keychain。
- 自动模式在兼容空闲实例间分配，手动模式严格等待指定实例。
- 不同实例并行、单实例串行，图片和视频不会在同一 GPU 上并发抢占。
- 每个 ComfyUI task 只提交一次，并绑定准确的实例与 `prompt_id`。
- 结果进入现有素材、文档、历史和预览流程。
- 没有无关的大面积重构，且在单独授权前不发生真实付费生成。
