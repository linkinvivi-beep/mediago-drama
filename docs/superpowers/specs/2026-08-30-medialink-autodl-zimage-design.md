# MediaLink：AutoDL 云端生图与多实例调度设计

日期：2026-08-30

状态：连接池与九 profile 产品范围已确认；七个 FLUX/混合 `精细控制-v2` 文件被用户标记为仍有问题，当前 digest/manifest 仅作观察记录，禁止用于 readiness 或提交

关联规格：`2026-08-30-medialink-codex-autodl-design.md`

## 1. 目标

在不改写 MediaGo Drama 核心工作流的前提下，为 MediaLink 增加第二条可见图片生成路线 `AutoDL · 云端生图`。人物、场景、道具、分镜、生成工作台和 Agent 继续使用现有统一生图入口；用户可以显式选择 Codex 或 AutoDL 云端生图，并在 AutoDL 路线中选择经过验证的 Z-Image、FLUX FP8、FLUX + Lustly 或 Z-Image → FLUX workflow profile。系统记住上次选择，但不会在故障时自动切换供应商或 workflow。

全部云端图片 profiles 与 MiniMax H3 共用可配置的 AutoDL 多实例连接池。不同实例可以并行运行；每个实例内部一次只运行一个 ComfyUI job，避免同一 GPU 上的图片与视频任务争抢显存。

## 2. 非目标

- 不新增独立的“图生图页面”，继续使用现有“提示词 + 可选参考图”交互。
- 不改写人物、场景、道具、分镜、素材、历史或文档关系模型。
- 不自动启动、关闭、租用、续费或删除 AutoDL 实例。
- 不把 ComfyUI 暴露到公网。
- 不在 Codex、Z-Image、FLUX 或 Lustly profiles 之间静默降级或改派。
- 不在首版合并多张参考图、丢弃多余参考图或猜测工作流节点。
- 不在没有单独确认时提交真实 `/prompt` 或消耗 Codex/GPU 额度。
- 不为此功能做无关供应商删除、目录重写或大面积内部重命名。

## 3. 产品路由

MediaLink 的可见图片路由为：

| 路由 ID | 显示名称 | 执行方式 | 参考图能力 |
| --- | --- | --- | --- |
| `codex.imagegen` | `Codex 生图` | Codex 内置 `$imagegen` | 沿用 Codex route 能力 |
| `autodl.image` | `AutoDL · 云端生图` | SSH 隧道后的 ComfyUI workflow profile | 由 profile 声明 0 或 1 张 |

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

实例连接后检查 `/system_stats`、`/object_info` 和 `/queue`，并分别验证已配置的九个图片 profiles 与 MiniMax H3 工作流。能力是“实例 + workflow profile”的验证结果，不能只依据历史日志、旧地址或另一个实例推断。

## 5. AutoDL 云端图片 workflow profiles

首版支持九个 profile kind：

| Kind | 用户工作流 | 触发条件 |
| --- | --- | --- |
| `zimage-t2i` | `Z-Image-Turbo-NSFW-BF16-v2-文生图.json` | 0 张参考图 |
| `zimage-i2i` | `Z-Image-Turbo-NSFW-BF16-v2-普通图生图.json` | 恰好 1 张参考图 |
| `flux-fp8-t2i` | `FLUX-FP8-普通文生图-质量-精细控制-v2.json` | 0 张参考图 |
| `flux-fp8-i2i` | `FLUX-FP8-普通图生图-精细控制-v2.json` | 恰好 1 张参考图 |
| `flux-lustly-adult-t2i` | `FLUX-FP8-Lustly-成人文生图-精细控制-v2.json` | 0 张参考图，必须显式选择成人 profile |
| `flux-lustly-adult-i2i` | `FLUX-FP8-Lustly-成人图生图-精细控制-v2.json` | 恰好 1 张参考图，必须显式选择成人 profile |
| `flux-lustly-adult-portrait` | `FLUX-FP8-Lustly-成人写实人像-精细控制-v2.json` | 0 张参考图，必须显式选择成人 profile |
| `flux-lustly-adult-fullbody` | `FLUX-FP8-Lustly-成人全身构图-精细控制-v2.json` | 0 张参考图，必须显式选择成人 profile |
| `zimage-flux-refine` | `Z-Image快速打样-FLUX高精重绘-精细控制-v2.json` | 0 张参考图；输出草图与成片 |

每个 profile 包含：

- 稳定 ID、名称、kind 和版本
- 原始 ComfyUI UI-format workflow JSON，以及由它编译出的 API prompt template
- prompt、seed、宽高或尺寸、参考图、LoRA 强度、denoise 和输出节点的语义 manifest
- 所需节点与模型声明
- workflow 内容摘要

ComfyUI 用户工作流是 UI-format JSON；应用导入时先验证 `nodes` / `links` 图并生成受控 API prompt template，保存原始 workflow digest、API template digest 和语义 manifest。业务代码不在 provider 中硬编码节点 ID；实例化只修改 manifest 明确绑定的字段。双阶段 profile 的 `Z草图` 标记为 secondary/draft，`FLUX成片` 标记为 primary/final，两张图都进入任务资产但默认返回成片。

默认自动 profile 仅按参考图数量选择 Z-Image 快速路线：0 张为 `zimage-t2i`，1 张为 `zimage-i2i`。FLUX、Lustly 和混合 profile 必须由用户显式选择；成人 profile 永不根据提示词自动启用，并要求所有人物明确为 21 岁以上成年人。

七个 FLUX/混合 profile 最终目标名称结尾为 `精细控制-v2`，但 2026-08-30 当前文件被用户明确标记为仍有问题。下表中的 v2 digest、节点和参数仅是当时的只读观察，不是已批准版本；应用必须把这些 records 标记为 `needs_revalidation`，不得满足 readiness、不得提交。用户提供修正版后重新读取并替换观察值。旧文件继续保留为 legacy，也不参与默认选择、实例 readiness 或 provider fallback。最终 v2 的 FLUX prompt manifest 预计分别绑定 `CLIPTextEncodeFlux.clip_l` 和 `CLIPTextEncodeFlux.t5xxl`，但以修正版再次验收为准。

### 5.1 2026-08-30 真实实例基线

当前 SSH 隧道 `127.0.0.1:6006` 已只读确认 ComfyUI `0.30.0`、RTX 5090、队列为空，九个 JSON 均存在且核心节点/模型可枚举。两个 Z-Image 值已实测；七个 v2 值是待修正候选快照，不能用于 readiness。原始 UI JSON SHA256：

| Kind | SHA256 |
| --- | --- |
| `zimage-t2i` | `ab070442514013c6264947432c9faae6350e9ea4cd15be0949997872b45efffe` |
| `zimage-i2i` | `f93feb7ebdc55566246af49e2b4a65f677b4893e637ca4072cbc1c936e8f4da1` |
| `flux-fp8-t2i` | `9970e8c3d92c4661a744b046d9f1b96208d875ad557af407f0ba89d656bc8419` |
| `flux-fp8-i2i` | `1d84021c7f0530d13d914bc982ccf2e8e75200ea433331331f27264a01884462` |
| `flux-lustly-adult-t2i` | `1ee1ab222cab32acfd6473708b15092356c7438b7fcbc6b58a2f9a903ba0bee8` |
| `flux-lustly-adult-i2i` | `1f0cbb187d4bb66e4edaab33b42d90aebd342457621bff1c093a21a42db092aa` |
| `flux-lustly-adult-portrait` | `80a5524712fe07dbc84d2c89558a66381a9bf03b8dba8380bf3dd84e9dcccc8c` |
| `flux-lustly-adult-fullbody` | `6c3a39a77b7a6a5a13e46c2e2788502be578982fb5727540dceb5e94faf9b4b0` |
| `zimage-flux-refine` | `cc2ba571bc0bc6c1e7d68b9d4a3b8f1302f999d01c15745867f40e62f6f6f8b2` |

实时节点/模型/输出基线如下；节点 ID 只属于 profile manifest，不进入通用 provider 业务代码：

| Kind | 关键节点 ID:class | 必需模型 | 采样基线 | 输出角色 |
| --- | --- | --- | --- | --- |
| `zimage-t2i` | `28:UNETLoader`, `30:CLIPLoader`, `29:VAELoader`, `27:CLIPTextEncode`, `33:ConditioningZeroOut`, `11:ModelSamplingAuraFlow`, `13:EmptySD3LatentImage`, `3:KSampler`, `8:VAEDecode`, `34:SaveImage` | `z_image_turbo_bf16_nsfw_v2.safetensors`, `qwen_3_4b.safetensors`, `ae.safetensors` | UI seed `616984537854174`/randomize；8 steps，CFG 1，`res_multistep/simple`，denoise 1 | `34` primary/final |
| `zimage-i2i` | `28:UNETLoader`, `30:CLIPLoader`, `29:VAELoader`, `27:CLIPTextEncode`, `33:ConditioningZeroOut`, `11:ModelSamplingAuraFlow`, `35:LoadImage`, `36:VAEEncode`, `3:KSampler`, `8:VAEDecode`, `34:SaveImage` | 同上 | UI seed 0/randomize；8 steps，CFG 1，`res_multistep/simple`，denoise 0.65 | `34` primary/final |
| `flux-fp8-t2i` | `30:CheckpointLoaderSimple`, `6:CLIPTextEncodeFlux`, `33:ConditioningZeroOut`, `27:EmptySD3LatentImage`, `31:KSampler`, `8:VAEDecode`, `9:SaveImage` | `flux1-dev-fp8.safetensors` | seed `972054013131368`/fixed；24 steps，CFG 1，`euler/simple`，denoise 1 | `9` primary/final |
| `flux-fp8-i2i` | `30:CheckpointLoaderSimple`, `6:CLIPTextEncodeFlux`, `33:ConditioningZeroOut`, `36:LoadImage`, `37:VAEEncode`, `31:KSampler`, `8:VAEDecode`, `9:SaveImage` | `flux1-dev-fp8.safetensors` | seed `972054013131368`/fixed；24 steps，CFG 1，`euler/simple`，denoise 0.35 | `9` primary/final |
| `flux-lustly-adult-t2i` | FLUX T2I v2 基线 + `36:LoraLoaderModelOnly` (0.5) | `flux1-dev-fp8.safetensors`, `flux_lustly-ai_v1.safetensors` | seed `972054013131368`/fixed；28 steps，CFG 1，`euler/simple`，denoise 1 | `9` primary/final |
| `flux-lustly-adult-i2i` | `30:CheckpointLoaderSimple`, `36:LoraLoaderModelOnly` (0.5), `37:LoadImage`, `38:VAEEncode`, `6:CLIPTextEncodeFlux`, `33:ConditioningZeroOut`, `31:KSampler`, `8:VAEDecode`, `9:SaveImage` | 同上 | seed `972054013131368`/fixed；28 steps，CFG 1，`euler/simple`，denoise 0.45 | `9` primary/final |
| `flux-lustly-adult-portrait` | FLUX T2I v2 基线 + `36:LoraLoaderModelOnly` (0.5); `27` 为 `832×1216` latent | 同上 | seed `972054013131368`/fixed；28 steps，CFG 1，`euler/simple`，denoise 1 | `9` primary/final |
| `flux-lustly-adult-fullbody` | FLUX T2I v2 基线 + `36:LoraLoaderModelOnly` (0.5); `27` 为 `768×1344` latent | 同上 | seed `972054013131368`/fixed；28 steps，CFG 1，`euler/simple`，denoise 1 | `9` primary/final |
| `zimage-flux-refine` | Z 支路 `28:UNETLoader`, `30:CLIPLoader`, `29:VAELoader`, `27:CLIPTextEncode`, `33:ConditioningZeroOut`, `11:ModelSamplingAuraFlow`, `13:EmptySD3LatentImage`, `3:KSampler`, `8:VAEDecode`, `34:SaveImage`; shared `47:StringConstantMultiline`; IMAGE 边界 `45:VAEEncode`; FLUX 支路 `38:CheckpointLoaderSimple`, `43:LoraLoaderModelOnly` (0), `35:CLIPTextEncodeFlux`, `40:ConditioningZeroOut`, `39:KSampler`, `36:VAEDecode`, `37:SaveImage` | `z_image_turbo_bf16_nsfw_v2.safetensors`, `qwen_3_4b.safetensors`, `ae.safetensors`, `flux1-dev-fp8.safetensors`, `flux_lustly-ai_v1.safetensors` | Z seed `616984537854174`/fixed、8 steps、CFG 1、`res_multistep/simple`、denoise 1；FLUX seed `972054013131368`/fixed、24 steps、CFG 1、`euler/simple`、denoise 0.30；LoRA 0 | `34` draft/secondary; `37` final/primary |

## 6. 提示词与参考图

- 未开启“优化提示词”时，用户原文直接注入选定 workflow。
- 开启时继续使用现有 Codex 文本优化器，不新增第三方文本 API。
- `codex.imagegen` 使用 Codex 图像定向优化指令。
- `autodl.image` 按 workflow family 使用 Z-Image 或 FLUX 定向优化指令，结果仍写入现有 `optimizedPrompt` 和历史字段。
- 未显式选择 profile 时，0 张参考图选择 `zimage-t2i`，1 张参考图选择 `zimage-i2i`。
- 显式 profile 必须满足其 0/1 张参考图约束；唯一参考图上传到任务专属 ComfyUI 子目录。
- 超过 1 张参考图时在提交前明确拒绝；不得自动拼图、丢图或只取第一张。

## 7. 自动调度与手动指定

### 7.1 自动模式

默认从“已启用、在线、当前空闲、目标 workflow 已验证”的实例中选择节点。同等候选采用轮询，避免持续偏向同一实例。不同实例可并行运行；每个实例只有一个执行槽，全部图片 profiles 和 H3 共用该槽。

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
  -> 显式选择 autodl.image
  -> 选择/解析 profile kind 并校验参考图数量
  -> 可选的 profile-family 定向提示词优化
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

设置页新增 AutoDL 实例列表，可添加、编辑、启用、停用和测试实例。每张实例卡显示解析后的地址、远程 ComfyUI 端口、fingerprint 状态、连接状态、当前任务及各 workflow profile 能力状态。密码始终以“已保存/未保存”呈现，不能回显。

云端图片 workflow 设置支持九个 profile 的导入、语义映射、实例验证、版本/digest 和失败原因。生成设置默认显示自动分配；高级设置提供 workflow profile 与实例选择。任务历史显示自动/手动调度、绑定实例、profile 版本、`prompt_id`、等待原因和恢复状态。

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
3. 取得并验证九个真实图片 JSON，确认 manifest 映射、模型枚举与 digest。
4. 若有两个实例，确认可同时连接、能力独立且不串号。

第二阶段会消耗 GPU，必须再次取得用户明确确认：

1. 生成一张 Z-Image 文生图并导入对应素材关系。
2. 用第一张结果生成一张 Z-Image 图生图。
3. 验证两个实例可并行处理两个独立图片任务。
4. 在不重复提交的前提下验证一次断线或应用重启恢复。

## 12. 完成标准

- 所有现有图片入口都可显式选择 Codex 或 AutoDL 云端生图。
- AutoDL 默认根据 0/1 张参考图选择 Z-Image 快速 profile，并允许显式选择其余七个真实 profiles。
- 用户可维护多个动态 AutoDL 实例，密码只进入 macOS Keychain。
- 自动模式在兼容空闲实例间分配，手动模式严格等待指定实例。
- 不同实例并行、单实例串行，图片和视频不会在同一 GPU 上并发抢占。
- 每个 ComfyUI task 只提交一次，并绑定准确的实例与 `prompt_id`。
- 结果进入现有素材、文档、历史和预览流程。
- 没有无关的大面积重构，且在单独授权前不发生真实付费生成。
