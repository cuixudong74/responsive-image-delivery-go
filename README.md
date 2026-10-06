# responsive-image-delivery-go

进程内响应式图片交付库：根据客户端能力与网络条件做内容协商与预算感知
的派生规格选择，带确定性生成、并发去重缓存、失效与背压。整条链路只在
本进程内运行，不依赖任何外部服务，仅使用 Go 标准库。

```go
svc := delivery.NewService(delivery.DefaultConfig(), nil)
svc.Sources().Put("hero", pngBytes) // 源图（jpeg/png/rimg 编码均可）

resp, err := svc.Deliver(ctx, delivery.Request{
    SourceID: "hero",
    Caps: delivery.ClientCapabilities{
        AcceptFormats:    []delivery.Format{delivery.FormatRIMG, delivery.FormatJPEG},
        ScreenWidth:      360,   // 图片占据的布局宽度（CSS px）
        DevicePixelRatio: 3,
        EffectiveType:    "4g",  // slow-2g / 2g / 3g / 4g
        SaveData:         false,
        MaxBytes:         0,     // >0 时覆盖推导出的字节预算
    },
})
// resp.Data                 协商后的派生图片字节
// resp.Selection            可解释的选型结果（规格、预算、原因、说明）
// resp.Reused / Coalesced   结果来自缓存 / 合并等待，还是新生成
```

## 内容协商规则

- 客户端通过 `AcceptFormats` 声明自己能解码的全部格式。
- 服务端按体积效率固定排序选择：`rimg`（进程内现代格式，最小）>
  `jpeg`（通用回退）> `png`（无损，最大）。
- 客户端不支持现代格式时自动回退到通用格式 `jpeg`；**永远不会返回
  客户端未声明的格式**。交集为空时返回 `ReasonNoCommonFormat` 错误。
- `rimg` 是本库实现的确定性有损格式（YCbCr 4:2:0 亚采样 + 量化 +
  行内差分 + flate），只能在本进程内解码，与"整条链路进程内"的范围
  一致；它作为"现代格式"角色参与协商与预算比较。

## 规格选择与预算语义

- 目标物理宽度 = `ceil(ScreenWidth * DevicePixelRatio)`；`ScreenWidth<=0`
  表示不约束，选最大候选宽度。
- 候选阶梯取 `Config.Widths` 中**能覆盖目标宽度的最小宽度**作为主选，
  永不放大超过源图宽度；高度按源图宽高比推出。
- 预算 `Budget{MaxBytes, MaxQuality}` 的推导优先级：
  `Caps.MaxBytes` 覆盖 > `SaveData`（`BudgetSaveData` + `QualitySaveData`）
  > `BudgetsByEffectiveType[EffectiveType]` > `BudgetNormal`。
- 生成后按实际字节数校验预算：超预算则沿阶梯向更小的规格逐级回退，
  第一个落入预算的规格胜出（`ReasonWithinBudget`）。
- **没有任何规格满足预算时**，返回最小规格并标记
  `ReasonOverBudgetSmallest`、`WithinBudget=false`，绝不静默返回超预算
  结果。每次选择的决策路径记录在 `Selection.Explanation`。
- 相同输入（源图、能力、配置）下选择唯一且确定。

## 缓存键与确定性

缓存键 = `(源图内容hash, 源图版本, 宽, 高, 格式, 质量, 配置ID)`：

- 派生结果只由源图内容、目标规格与相关配置决定；重复生成、改变并行度、
  进程重启后输出**逐字节一致**（纯 Go 双线性缩放 + 确定性编码器）。
- 配置中影响输出的字段（宽度阶梯、质量）折叠为 `ConfigID` 进入键，
  配置变化自然使旧结果不可达。
- 取消与超时（`ReasonCanceled` / `ReasonTimeout`）不会写入任何半成品；
  后续请求会重新生成。

## 并发去重

相同缓存键的并发请求只触发一次生成（singleflight）：领导者生成，
其余请求等待并共享结果（统计为 `Coalesced`）。不同键的生成互不阻塞，
慢生成不会拖住其他规格的请求。缓存为字节上限 LRU（`CacheMaxBytes`），
内存占用有界。

## 失效与一致性

- `SourceStore.Put` 原子替换源图：已解析旧快照的请求看到完整旧版本，
  新请求看到新版本，不会出现尺寸与内容错配的混合结果。
- 键内含内容 hash 与版本号，源图更新后旧结果**不可达**；
  `Service.Invalidate(id)` 额外立即回收旧派生物占用的内存。
- 重新生成的结果与基于新输入直接生成的结果逐字节一致（有测试保证）。

## 资源上限与背压

- `MaxConcurrentGenerations`：并行生成上限（信号量）。
- `MaxQueue`：等待生成槽位的请求上限，超出立即以 `ReasonQueueFull`
  拒绝（可区分），不会无限排队。
- `GenerationTimeout`：单次生成超时（<=0 表示不限）。
- 高负载 + 取消场景下无死锁（有测试），缓存与解码缓存均有界。

## 可观测统计

`svc.Stats().Snapshot()` 给出累计计数：请求数、新生成数、缓存命中、
合并等待、拒绝、失败、超预算交付数。`svc.Stats().Recent(n)` 返回最近
n 条事件（命中的规格、复用/新生成、是否落在预算内、字节数、原因），
可用于验证缓存与预算策略是否有效。

## 错误原因一览

| ReasonCode | 含义 |
|---|---|
| `within_budget` | 正常交付，落入预算 |
| `over_budget_smallest` | 无规格满足预算，按策略返回最小规格并标记 |
| `no_common_format` | 客户端声明的格式与服务端无交集 |
| `source_not_found` | 源图不存在 |
| `queue_full` | 生成等待队列已满，按策略拒绝 |
| `generation_failed` | 源图无法解码或编码失败 |
| `canceled` | 请求上下文被取消 |
| `timeout` | 生成超时 |

## 配置项

见 `delivery.Config`：`Widths`（候选宽度阶梯）、`QualityDefault` /
`QualitySaveData`、`BudgetNormal` / `BudgetSaveData` /
`BudgetsByEffectiveType`、`MaxConcurrentGenerations`、`MaxQueue`、
`CacheMaxBytes`、`GenerationTimeout`、`StatsCapacity`。

## 测试

```sh
go test ./...        # 全部测试
go test -race ./...  # 竞态检测（较慢）
```

覆盖：协商回退与能力一致性、预算选择与超预算策略、确定性输出
（重复/并行度/重启）、并发去重与慢生成隔离、源图更新与失效、
损坏源图、取消、超时、队列满拒绝与高负载无死锁。
