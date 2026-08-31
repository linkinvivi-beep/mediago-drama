# MediaLink macOS arm64 发布检查清单

日期：2026-09-01

本清单区分静态代码检查、离线构建验证和需要用户单独授权的真实生成验收。不得把“代码已实现”写成“应用已构建或工作流已跑通”。

## 1. 产品边界

- [x] 产品名为 `MediaLink`。
- [x] Electron bundle ID 配置为 `app.medialink.desktop`。
- [x] 构建目标配置仅接受 `darwin-arm64`。
- [x] 预期产物名为 `MediaLink-<version>-macos-arm64.dmg` 和 `MediaLink-<version>-macos-arm64.zip`。
- [x] 可见视觉路线为 `Codex 生图`、`AutoDL · 云端生图`、`AutoDL · MiniMax H3`。
- [x] Codex 图片路线使用当前 Codex 登录与内置生图能力，不调用 OpenAI Images API。
- [x] MediaLink 全局数据目录为 `~/Library/Application Support/MediaLink`；项目内部 `.mediago-drama` 标记保持兼容，不执行旧数据迁移。

以上项目已通过源码、构建配置、产物元数据和本机启动冒烟交叉核对。

## 2. AutoDL 与工作流前提

- [ ] 至少配置一个已启动的 AutoDL 实例。
- [ ] 填写该实例当前有效的完整 SSH 登录指令。
- [ ] 确认 SSH 主机指纹；密码仅保存到 macOS Keychain。
- [ ] 填写该实例实际的 ComfyUI 远端 loopback 端口。端口不固定为 `6006`。
- [ ] 导入目标 ComfyUI 工作流，并确认提示词、参考图、尺寸、时长、种子与输出等语义映射。
- [ ] 在准备使用的每个实例上验证对应工作流版本后再启用。
- [ ] 多实例自动调度与高级手动指定都只使用已验证的“实例 + 工作流版本”组合。

工作流可以是当前或后续安装的 Z-Image、Qwen-Image-Edit、FLUX、MiniMax H3 或其他兼容图；MediaLink 不根据文件名或模型名硬编码节点。

## 3. 离线质量门

本机没有安装 Task CLI，因此使用对应的底层命令完成质量门；未连接 AutoDL、未调用 ComfyUI `/prompt`，也未运行任何真实工作流：

- [x] `go test ./... -count=1 -timeout=5m`（目录：`packages/core`）。
- [x] `go vet ./...`（目录：`services/server`）。
- [x] `go test ./... -count=1 -timeout=5m`（目录：`services/server`）。
- [x] `node scripts/build-server-target.mjs darwin-arm64`，成功构建服务器和两个 MCP sidecar。
- [x] `pnpm check`（目录：`apps/workspace`），0 条 lint/format 错误。
- [x] `pnpm test`（目录：`apps/workspace`），214 个测试文件、1406 个测试通过。
- [x] `pnpm build`（目录：`apps/workspace`）。
- [x] Electron staged package 契约测试通过，且 `electron-updater` 已内联到主进程 ASAR。
- [x] 提交前执行 `git diff --check`，没有空白错误。
- [x] 未跟踪的 `work/` 目录保持未修改、未暂存。

## 4. Apple Silicon 构建与安装检查

- [x] 从仓库根目录准备 arm64 sidecar 与资源。
- [x] 完成 `pnpm electron:build:darwin-arm64` 的编译、staging 与 arm64 DMG/ZIP 打包步骤。
- [x] 输出目录只生成 MediaLink arm64 `.app`、DMG 和 ZIP，没有 Windows、Linux 或 Intel Mac 产物。
- [x] DMG 以只读方式挂载到 `/private/tmp`，校验和验证通过，检查后正常卸载；没有覆盖现有应用。
- [x] release 目录中的临时应用启动 8 秒，服务器在随机 loopback 端口正常启动后退出；没有遗留 MediaLink/sidecar 进程。
- [x] 应用主可执行文件及三个 Go sidecar 经 `file` 确认为 `Mach-O 64-bit executable arm64`。
- [x] `Info.plist` 的 `CFBundleIdentifier` 为 `app.medialink.desktop`，显示名与可执行文件名均为 `MediaLink`。
- [x] 当前本地包明确配置为不签名、不公证；`codesign --verify --deep --strict` 和 `spctl --assess` 均未通过，不宣称可直接通过 Gatekeeper 分发。
- [x] 启动日志确认应用数据写入 `~/Library/Application Support/MediaLink`。本轮没有执行旧数据迁移或清理。

产物：

- `apps/workspace/release/MediaLink-0.1.0-beta.0-macos-arm64.dmg`，SHA-256 `2097cfeea37339ae46f08f0ad1e39cf2b18119b49bb4c38e643a1449a4d84719`。
- `apps/workspace/release/MediaLink-0.1.0-beta.0-macos-arm64.zip`，SHA-256 `46dbc3bc7287025e6d999b641c0a2858f9b84fd202e264ff4ce51f8c8d55bfba`。

## 5. 真实生成验收

状态：**未授权、未执行。**

- [ ] 获得用户对 Codex 图片额度和 AutoDL GPU 消耗的单独明确授权。
- [ ] 运行一个低成本测试图片任务，确认 Codex item、修订提示词、素材导入和人物/场景/道具关联。
- [ ] 运行一个用户批准工作流的 4 秒 H3 测试任务，记录唯一 ComfyUI `prompt_id`，验证断线后按原 ID 恢复，不重复提交。
- [ ] 验证输出进入素材库、分镜选择和分集预览。
- [ ] 测试结果使用独立测试资产，不替换已经确认的正式素材。

真实验收记录不得包含密码、SSH 私钥、完整带签名 URL、账户余额或其他凭据。

## 6. 当前结论

MediaLink 已通过本地离线质量门、macOS Apple Silicon 构建、DMG 只读挂载和临时启动冒烟。当前产物仍是未签名、未公证的开发验收包，不标记为公开发布版；真实 Codex/AutoDL 生成继续保持“未授权、未执行”。
