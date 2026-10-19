// 给更新源 latest.json 签名 / 验签的小工具（发版时用）。
//
// 签名（发版）：
//
//	go run ./tool/signlatest -version 1.0.1 -note "改了什么"
//	    → 带 sig 的 JSON 打到标准输出，重定向到文件就行
//
// 验签（发版后自查）：
//
//	go run ./tool/signlatest -verify dist/latest.json -pub <客户端内置的公钥>
//	    → 用**客户端真正会用的那把公钥**验一遍，确认客户端会认 ✓
//
// 🩸 私钥来源（签名时，按优先级）：
//  1. `-key` 指定的文件
//  2. 环境变量 `P2PLINK_SIGN_KEY`（base64 的私钥）
//  3. 默认 `dist/update-signing-key.txt`（dist/ 是 gitignore 的 ⇒ 不进 git ✓）
//
// 🩸 签名规则必须跟客户端 `cmd/gui/update.go` 的 `signedFields` **一模一样**：
// 签的是 `version + "\n" + url + "\n" + note`，**不是**整个 JSON
// （JSON 的空白/字段顺序不固定 ⇒ 签原始字节必然对不上 ✗）
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

// latest 是那份 JSON 的形状（跟客户端的 updateInfo 对应）✓
type latest struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Note    string `json:"note"`
	Sig     string `json:"sig"`
}

// signed 是"被签的那串字节"—— 必须跟客户端的 signedFields 一致 ✓
func signed(l latest) []byte {
	return []byte(l.Version + "\n" + l.URL + "\n" + l.Note)
}

func main() {
	version := flag.String("version", "", "版本号，比如 1.0.1（签名时必填）")
	url := flag.String("url", "https://ti.tinyaii.cn/", "下载页地址（必须在客户端白名单里）")
	note := flag.String("note", "", "一句话说明（可以空）")
	keyFile := flag.String("key", "dist/update-signing-key.txt", "私钥文件（PRIVATE_KEY=... 那行）")
	verifyFile := flag.String("verify", "", "改成验签模式：指定要验的 latest.json")
	pubB64 := flag.String("pub", "", "验签用的公钥（base64）—— 一般传客户端内置的那把")
	flag.Parse()

	// ── 验签模式 ────────────────────────────────────────
	if *verifyFile != "" {
		if err := doVerify(*verifyFile, *pubB64); err != nil {
			fmt.Fprintln(os.Stderr, "✗ "+err.Error())
			os.Exit(1)
		}
		fmt.Println("✓ 验签通过 —— 客户端会认这份 latest.json")
		return
	}

	// ── 签名模式 ────────────────────────────────────────
	if strings.TrimSpace(*version) == "" {
		fmt.Fprintln(os.Stderr, "-version 必填（比如 1.0.1）")
		os.Exit(1)
	}
	priv, err := loadKey(*keyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读私钥失败:", err)
		os.Exit(1)
	}

	out := latest{Version: *version, URL: *url, Note: *note}
	out.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signed(out)))

	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "写 JSON 失败:", err)
		os.Exit(1)
	}
}

// doVerify 用指定公钥验一份 latest.json ✓
//
// 📌 为什么要能"指定公钥"：发版自查时必须用**客户端内置的那把**，
// 而不是私钥文件里记的那把 —— 后者是自己登记自己，验过了也说明不了什么 ✗
func doVerify(file, pubB64 string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("读不了 %s: %w", file, err)
	}
	var l latest
	if err := json.Unmarshal(raw, &l); err != nil {
		return fmt.Errorf("%s 不是合法 JSON: %w", file, err)
	}
	if strings.TrimSpace(l.Sig) == "" {
		return fmt.Errorf("%s 里没有 sig（没签名？）", file)
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("公钥格式不对（解出来 %d 字节）", len(pub))
	}
	sig, err := base64.StdEncoding.DecodeString(l.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("签名格式不对（解出来 %d 字节）", len(sig))
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), signed(l), sig) {
		return fmt.Errorf("签名对不上（公钥跟签名用的私钥不是一对？）")
	}
	return nil
}

// loadKey 取私钥：环境变量优先，其次读文件里的 PRIVATE_KEY= 那行 ✓
func loadKey(file string) (ed25519.PrivateKey, error) {
	raw := strings.TrimSpace(os.Getenv("P2PLINK_SIGN_KEY"))
	if raw == "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "PRIVATE_KEY=") {
				raw = strings.TrimSpace(strings.TrimPrefix(line, "PRIVATE_KEY="))
				break
			}
		}
		if raw == "" {
			return nil, fmt.Errorf("%s 里没找到 PRIVATE_KEY= 那行", file)
		}
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("私钥长度不对（%d，该是 %d）", len(key), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(key), nil
}
