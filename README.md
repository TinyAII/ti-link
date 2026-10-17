# TI 联机 · 官网与更新源

这个仓库只放官网和更新源，**没有源码**：

| 文件 | 用途 |
|---|---|
| `index.html` + `shots/` | 官网首页（GitHub Pages，<https://ti.tinyaii.cn/>） |
| `download.html` | 下载页：版本区别、下载渠道、**没有签名证书**的提示 |
| `latest.json` | 客户端「检测更新」拉的更新源 |

## 改官网

源码在本地仓库的 `site/`，改完推到这儿：

```bash
cd <项目根目录>/tmp/ghpages
GH_TOKEN=xxx python ../ghpush.py TinyAII/ti-link index.html download.html

# 想自定义提交信息就加这个环境变量（默认会自动拼）
GH_MSG="发版 1.1.0" python ../ghpush.py TinyAII/ti-link index.html download.html
```

多少文件都是**一条提交**。内容没变会自动跳过，不会打空提交。

## latest.json —— ⚠️ **不要手改**

它必须带 `sig` 签名（Ed25519，签的是 `version` + `url` + `note` 三个字段）。

客户端**强制验签**：验不过就当"这次没查成"，**静默地什么都不提示**。
也就是说 —— 手改一个字符，签名立刻失效，而且**从现象上完全看不出来**：
用户只会觉得"没有新版本"，没有人会报错，你也不会收到任何异常。

所以只能用签名工具产，不能手写：

```bash
cd <项目根目录>

# 产（私钥默认读 dist/update-signing-key.txt，不进 git）
go run ./tool/signlatest -version 1.1.0 -note "这版修了什么" > dist/latest.json

# 验（公钥就是客户端内置的那把，两边必须一致）
go run ./tool/signlatest -verify dist/latest.json \
  -pub "bOeOVn1KLvdbZHm2RrN2JiA1sU5v/fkKEF24L48uc0M="
```

或者直接 `bash scripts/release.sh 1.1.0 "这版修了什么"`，编译 → 打包 → 签名一条龙，
最后会打印上传清单。

### 字段含义

- `version` —— 比客户端自己的大才会提示。必须能**逐段解析成数字**
  （`1.10.0` 没问题；`1.1.0-beta` 这种客户端认不出 —— 那时宁可不提示，也不会推错）
- `url` —— 让用户去哪下载。必须在客户端白名单里，写别的域名会被拒
- `note` —— 一句话说明，可以空

## 规矩（别破）

- 官网**不加载任何第三方脚本**、不做访问统计、不放广告、不加评论区
- 这个软件永远免费，这个页面也不该出现任何变现入口
- 更新只做**提示**，不做自动下载替换（无签名 exe 自我替换会被杀软当木马）
- 下载页里写死了版本号、文件名、大小和 SHA256 —— **重新编过包就要回去改**，
  否则给出的校验值是错的，用户按它核对会以为文件被篡改
