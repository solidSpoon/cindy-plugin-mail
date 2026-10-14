# ghost/ — Cindy 插件源码

这是 `mail-app-cli` 的 Cindy 插件（`.cindy` 能力包）源码目录。**不是**已安装插件目录，
修改这里不会影响已装版本，改完要重新打包并安装。

## 结构

```
ghost/
├── ghost.json                   身份卡（工具面、能力、命令词 $mail）
├── main.js                      电子脑：工具调用 → Node 工作进程
├── node/worker.cjs              本机进程：spawn 包内 mail-app-cli
├── bin/mail-app-cli             随包的 arm64 二进制（必须在打包前 chmod 755）
├── manual/operations/MANUAL.md  随包手册（工作流、坑表、排障）
├── locales/{en,zh-CN}.json      清单文案翻译
└── assets/icon.png              图标
```

## 刷新二进制后重新打包

CLI 逻辑改了之后，二进制要重新编译进包里，插件才会跟上：

```bash
cd ~/projects/mail-app-cli
go build -o ghost/bin/mail-app-cli .   # 只出 arm64；换架构要重打
chmod 755 ghost/bin/mail-app-cli
```

打包本身用 Forge 工具：`ghost_forge_pack({ dir: "…/ghost" })`（只出产物，不安装）。
确需直接安装/更新时用 `ghost_forge_install({ dir: "…/ghost" })`。

## 已知边界

- 二进制是 **arm64**，只在这台 Apple Silicon Mac 上有效。
- 不声明网络、凭证或设置页：插件零配置，一切走本机 Mail.app。
- 操作目录来自二进制的 `schema` action（实时读取，不硬编码）；二进制缺失时
  `list_tools` 会降级成静态列表并给出 warning。
- 危险写操作（move / archive / quarantine / 多封 patch / markRead）会经 `confirm`
  请用户点一次同意。
