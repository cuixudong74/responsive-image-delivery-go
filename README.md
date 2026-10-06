# responsive-image-delivery-go

面向终端的响应式图片协商交付与预算优化库。整条链路在单进程内运行，不依赖任何外部服务：
源图存放在进程内的 `SourceStore`，客户端声明能力与网络条件，管线完成内容协商、
预算内规格选择、去重生成与缓存复用，并提供完整统计。

核心包：`delivery`。

## 快速开始

```go
store := delivery.NewSourceStore()
src, _ := store.Put("hero", pngBytes) // 本地样本或进程内生成的图片字节

p := delivery.NewPipeline(store, delivery.DefaultRegistry(), delivery.Options{
    Config:        delivery.DefaultConfig(),
    CacheMaxBytes: 64 << 20, // 派生缓存上限（字节），超出按 LRU 逐出
    MaxConcurrent: 4,        // 最大并发生成数
    MaxQueue:      64,       // 生成排队上限，超出拒绝
})

res, err := p.Serve(ctx, "hero", delivery.ClientCapabilities{
    AcceptFormats:  []string{"image/webp", "image/png"}, // 客户端可解码格式
    ScreenWidthCSS: 640,                                 // 屏宽（CSS 像素）
    DPR:            2,                                   // 设备像素比
    Network:        delivery.Network3G,                  // 网络条件
})
// res.Data 为交付字节；res.Selection 说明命中的规格、原因与预算内与否；
// res.Reused 表示本次是复用还是新生成。
```

## 内容协商规则

格式协商（`Negotiator.NegotiateFormat`）：

1. 客户端在 `AcceptFormats` 中按偏好声明可解码格式的 MIME 类型；
   空列表或含 `*/*`、`image/*` 表示接受任意格式。
2. 服务端把注册表中的格式按 `EfficiencyRank`（体积效率，越小越优）排序，
   在**客户端接受的格式**中选出效率最高者。内置注册表中
   `image/jpeg`（rank 1，体积更小的首选格式）优于 `image/png`（rank 2，通用回退格式）。
3. 客户端只接受非首选格式时，回退到其可解码的通用格式，
   结果标记 `ReasonFallbackFormat`。
4. 双方无共同格式时返回 `ErrNoCommonFormat`（`ReasonNoCommonFormat`），
   **绝不返回客户端无法解码的数据**。

规格选择（`Negotiator.Select`）：

1. 理想宽度 = `ScreenWidthCSS × DPR`（屏宽未知时取源图宽度）；
2. 候选阶梯 = `Config.Ladder.Widths × DPRs`，去重、截断到放大上限
   （默认不允许超过源图宽度），源图原始宽度始终在候选中；
3. 取不小于理想宽度的最小候选；若其估计体积超预算，向更小候选逐级降级；
4. 相同输入下选择结果唯一且可由 `Selection.Reason` 解释。

## 预算语义

- 预算按网络条件定义（`Budgets map[NetworkClass]Budget`），默认：
  save-data 50KB、2g 100KB、3g 250KB、4g/unknown 1MB。
- 选择阶段使用确定性的体积启发式估计（与像素数成正比，按格式/质量定系数）；
  交付后 `Result.Bytes` 为实际字节数，`Selection.WithinBudget` 记录选择时的预算判定。
- `Reason` 可区分以下情况：
  - `within_budget`：命中预算内最优规格；
  - `downgraded_for_budget`：理想规格超预算，降级到预算内较小规格；
  - `over_budget_floor`：**没有任何规格满足预算**，按既定策略交付最小规格，
    `WithinBudget=false`，绝不静默超预算；
  - `fallback_format` / `no_common_format`：格式回退 / 无共同格式。

## 缓存键与失效

缓存键 = `配置哈希 / 源图内容哈希(sha256) / 规格(w<宽>-<格式>-q<质量>)`。

- 派生结果只由源图内容、规格与配置决定；三者任一变化都会得到新键，
  旧键自然不再被命中，保证重新生成与基于新输入直接生成的结果一致。
- `Pipeline.InvalidateSource(src)` / `InvalidateConfig(oldCfg)` 按键前缀批量
  失效并回收内存；`SourceStore` 的读写以快照语义进行，并发替换期间
  读者要么看到完整旧版本、要么看到完整新版本，不会出现尺寸与内容错配。
- 生成结果先落在临时缓冲，成功后才提交缓存；取消、超时、失败不会留下
  会被当作有效结果的半成品。

## 并发、背压与资源上限

- **去重（singleflight）**：相同缓存键的并发请求只触发一次生成，
  其余请求等待并复用同一结果；等待者自身 ctx 取消不影响进行中的生成。
- **慢生成隔离**：不同键的生成相互独立，慢规格不阻塞其他规格
  （并发度仅受 `MaxConcurrent` 限制）。
- **背压**：并发生成数超过 `MaxConcurrent` 后排队，排队数超过 `MaxQueue`
  立即拒绝并返回 `ErrTooManyInFlight`（计入 `Stats.Rejected`）；
  等待可被 ctx 取消，不会死锁。
- **内存有界**：派生缓存按 `CacheMaxBytes` 以 LRU 逐出；源图与派生均为
  内存字节，无临时文件，占用不会无界增长。

## 可观测统计

`Pipeline.Stats().Snapshot()` 返回：

| 字段 | 含义 |
| --- | --- |
| `Requests` | 总请求数 |
| `CacheHits` | 复用已有/并发中结果的请求数 |
| `Generations` | 实际触发的生成次数（应 ≈ Requests − CacheHits − 失败） |
| `GenFailures` | 生成失败（含取消/超时丢弃）次数 |
| `OverBudget` | 交付结果超出预算（`over_budget_floor`）的请求数 |
| `Rejected` | 因容量上限被拒绝的请求数 |
| `Invalidations` | 失效的缓存条目数 |

每次请求的 `Result` 同时给出命中的规格（`Selection.Spec`）、是否复用
（`Reused`）、是否在预算内（`Selection.WithinBudget`）与原因（`Selection.Reason`），
可用于验证缓存与预算策略是否有效。

## 配置项

| 配置 | 位置 | 默认 | 说明 |
| --- | --- | --- | --- |
| `Ladder.Widths` | `Config` | 320…1920 | 候选宽度阶梯（CSS 像素） |
| `Ladder.DPRs` | `Config` | {1, 2} | 候选像素密度 |
| `Ladder.Quality` | `Config` | 80 | 编码质量（1-100） |
| `Ladder.MaxSourceUpscale` | `Config` | 0（不放大） | 允许的最大放大倍数 |
| `Budgets` | `Options` | 见上 | 各网络条件的体积预算 |
| `CacheMaxBytes` | `Options` | 0（不限） | 派生缓存字节上限，LRU 逐出 |
| `MaxConcurrent` | `Options` | 4 | 最大并发生成数 |
| `MaxQueue` | `Options` | 64 | 生成排队上限 |

## 确定性保证

缩放为固定顺序的 float64 双线性重采样，编码器（stdlib jpeg/png）输出确定，
缓存键由内容哈希与配置哈希构成：相同输入在重复生成、任意并行度与进程
重启后输出逐字节一致（由 `TestDeterministicAcrossRestartAndParallelism` 验证）。

## 测试

```sh
go test ./...          # 单元与端到端测试
go test -race ./...    # 竞态检测
```

覆盖：协商回退与无共同格式、预算内选择/降级/超预算兜底、确定性输出、
并发去重、慢生成隔离、源图更新失效与读写一致性、生成失败与取消不留半成品、
背压拒绝与恢复、统计计数。
