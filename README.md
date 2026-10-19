# NovaEmby 插件

NovaEmby 的官方插件仓库：插件源码、Go 开发包、打包与发布工具。

NovaEmby 内置的官方仓库地址是本仓库的 `index.json`：

```
https://raw.githubusercontent.com/Mzxya/novaemby-plugins/main/index.json
```

`index.json.sig` 是它的签名，NovaEmby 用内置公钥验证。推送到 main 后由 GitHub Actions 自动维护这两个文件，**不要手改**。

## 目录

|路径|内容|
|--|--|
|`plugins/<插件ID>/`|一个插件：`manifest.json`、`main.go`，可选 `assets/`、`CHANGELOG/<版本>.md`。目录名必须等于插件 ID|
|`plugins/hello/`|示例插件，演示全部能力，写新插件时复制它|
|`sdk/`|Go 开发包（`github.com/Mzxya/novaemby-plugins/sdk`）|
|`tools/mkrepo/`|打包、维护 index.json、签名的工具|
|`.github/workflows/release.yml`|检查、测试、发布|
|`docs/插件开发教程.md`|完整教程|

## 写一个插件

1. 复制 `plugins/hello` 为 `plugins/<新插件ID>`。
2. 改 `manifest.json`（id、name、version、entry 里的文件名、events、routes、config）和 `main.go`。
3. 推送到 main。Actions 会编译 linux/amd64、linux/arm64，打包，建 Release，更新并签名 `index.json`。
4. 在 NovaEmby 的插件商店里安装。

修改插件后**把 manifest 的 version 加一**再推送；版本号没变时不会重新发布。

## 签名私钥

仓库 Settings → Secrets and variables → Actions 中的 `NOVA_PLUGIN_SIGN_KEY`（`novaemby_plugin_sign.pem` 的全部内容）。私钥绝不能提交到任何仓库。没有设置时照常发布，但仓库没有签名，NovaEmby 安装时要求确认风险。

详细说明见 [docs/插件开发教程.md](docs/插件开发教程.md)。
