# MediaLink Codex 生图结果导入修复设计

日期：2026-09-02

## 问题

Codex 内置 `$imagegen` 已成功生成图片，但当前 Codex 会把结果保存到全局目录：

```text
~/.codex/generated_images/<thread-id>/<item-id>.<image-extension>
```

MediaLink 仍只接受任务私有 job 目录中的结果，因此把结构化返回的合法 `savedPath` 拒绝为 `Codex image path is outside Codex image job directory`。后端任务已经失败时，部分前端入口仍会持续显示“生成中”。

## 目标

- 安全导入 Codex 结构化结果指向的官方生成文件。
- 不允许读取任意本地路径，不降低现有符号链接、文件类型、大小和 MIME 校验。
- 后端进入完成或失败终态后，前端停止显示“生成中”。
- 已经成功生成但尚未导入的图片可复用，不再次调用生图。

## 方案

### 1. 受信任输出定位

保留现有任务 job 目录作为第一受信任根目录。若 `savedPath` 不在该目录中，仅允许回退到当前 Codex 根目录下的 `generated_images`：

```text
<codex-home>/generated_images/<thread-id>/<item-id>.<image-extension>
```

回退路径必须同时满足：

- `thread-id` 等于结构化结果的 `ThreadID`；
- 文件名主体等于结构化结果的图片 item ID；
- 扩展名属于现有支持的图片格式；
- 路径位于规范化后的 Codex 官方生成根目录内；
- 沿途不跟随符号链接；
- 目标是普通文件，且通过现有大小和 MIME 实检。

任一条件不满足时继续失败关闭，不导入文件。

### 2. Provider 数据流

`CodexImageProvider` 仍优先读取任务 job 目录。只有该校验明确返回“目录外”时，才根据本次 `ThreadID`、item ID 和 `savedPath` 验证官方目录。验证通过后只读取图片字节，后续仍复用现有素材事务和落库流程；不移动、不删除 Codex 原文件。

### 3. 状态收敛

前端以服务端任务终态为准。任务为 `completed`、`failed` 或 `cancelled` 时停止活动指示和轮询，并显示实际结果或错误；不得让已经失败的任务继续呈现“生成中”。

### 4. 已有结果恢复

修复安装后，针对本次已经生成的图片执行一次只读验证与现有素材导入流程，不新建 Codex imagegen turn，不消耗新的生图请求。若历史任务恢复接口不适合安全复用，则只报告现有文件位置，不直接改写历史任务。

## 测试与验收

- 先增加失败测试，复现官方 `generated_images/<thread>/<item>.png` 被拒绝。
- 验证匹配 thread/item 的官方输出能够导入。
- 验证错误 thread、错误 item、任意外部路径、符号链接、非图片和超限文件仍被拒绝。
- 验证任务进入失败或完成终态后前端停止“生成中”。
- 运行相关 Go、前端测试及构建检查。
- 打包安装 macOS Apple Silicon 应用，复核签名、架构和内置服务。

## 非目标

- 不改变 Codex 登录、配额或 imagegen 调用方式。
- 不调用 OpenAI Image API。
- 不重构生成任务架构。
- 不重新提交已经生成成功的图片。
- 不改动 AutoDL、ComfyUI 或视频生成链路。
