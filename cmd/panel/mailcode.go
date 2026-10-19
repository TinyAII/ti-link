// 邮箱验证码：生成、限流、校验。
//
// 🩸 **码只存在内存里，不落盘** —— 这是故意的，不是偷懒：
//
//	· 它只用 5 分钟，而面板是个长期跑着的进程 ⇒ 存盘换不来什么
//	· 存盘就多一份"能被读走的东西"。6 位数字的哈希在有文件的人面前
//	  一秒钟就能彩虹出来 ⇒ 落盘反而给人一个假的安心 ✓
//	· 少一次写盘 = 少一种"写坏了"的可能
//	代价：面板重启，已经发出去的码全作废（用户重新要一个就行，代价很小）✓
//
// 🩸 另一半是这个文件里的**限流**。没有它，这个接口就是一台**给别人发垃圾邮件的机器** ——
// 而且是拿我们的 QQ 邮箱发，发多了号会被封 ✗
// 四道闸：重发间隔 / 每邮箱每小时 / 每 IP 每小时 / 一个码试错次数。
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"
	"sync"
	"time"
)

const (
	codeTTL       = 5 * time.Minute  // 码多久过期
	codeResendGap = 60 * time.Second // 同一个邮箱隔多久才能再要一次
	codeMaxTries  = 5                // 一个码最多试几次（到了就作废，防暴力猜）
	codePerEmail  = 5                // 每个邮箱每小时最多发几封
	codePerIP     = 20               // 每个 IP 每小时最多发几封
	codeLen       = 6                // 位数（纯数字，好念好抄）
)

type mailCodeEntry struct {
	hash   []byte // 存的是 HMAC，不是明文
	exp    time.Time
	tries  int
	sentAt time.Time
}

type mailCodeStore struct {
	key []byte // HMAC 密钥（跟 CSRF 共用面板那把 panel.key）

	mu      sync.Mutex
	m       map[string]*mailCodeEntry // key = 规范化之后的邮箱
	perMail *limiter                  // 每邮箱每小时发几封
	perIP   *limiter                  // 每 IP 每小时发几封
}

func newMailCodeStore(key []byte) *mailCodeStore {
	return &mailCodeStore{
		key:     key,
		m:       map[string]*mailCodeEntry{},
		perMail: newLimiter(codePerEmail, time.Hour),
		perIP:   newLimiter(codePerIP, time.Hour),
	}
}

func (s *mailCodeStore) hash(email, code string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(email))
	mac.Write([]byte{0}) // 分隔符：不然 ("ab","c") 和 ("a","bc") 算出同一个值
	mac.Write([]byte(code))
	return mac.Sum(nil)
}

// beginSend 就是"要发一封"这件事的全部判定 —— 查限流、生成码、记一笔。
//
// 🩸 这三件事**必须在同一把锁里**做完。拆成 canSend() + markSent() 的话，
// 两个并发请求会双双通过检查、双双计数 ⇒ "每小时 5 封"变成"想发多少发多少" ✗
//
// 返回 (code, why)：why == "" 才是成功，否则是给用户看的中文原因。
func (s *mailCodeStore) beginSend(email, ip string, now time.Time) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// ① 重发间隔：刚点过"获取验证码"就再点，不该真发第二封
	if e := s.m[email]; e != nil {
		if left := codeResendGap - now.Sub(e.sentAt); left > 0 {
			return "", fmt.Sprintf("刚发过了，%d 秒后可以再发", int(left.Seconds())+1)
		}
	}
	// ② 每邮箱每小时
	if ok, _ := s.perMail.allow(email, now); !ok {
		return "", "这个邮箱要得太频繁了，过一小时再试"
	}
	// ③ 每 IP 每小时（防有人换着邮箱刷）
	if ok, _ := s.perIP.allow("ip:"+ip, now); !ok {
		return "", "请求太频繁了，过一会儿再试"
	}

	code, err := randomCode()
	if err != nil {
		// 拿不到随机数就别发 —— 绝不能"随手糊一个"（弱码比不发更糟）✗
		return "", "服务器忙，稍后再试"
	}
	s.m[email] = &mailCodeEntry{
		hash:   s.hash(email, code),
		exp:    now.Add(codeTTL),
		sentAt: now,
	}
	return code, ""
}

// revoke 把一个刚发出去的码吊销掉。
//
// 📌 什么时候用：**码存下了、但邮件没发出去**的时候。
// 不吊销的话，用户拿不到码、却已经占掉一次 60 秒重发间隔和一小时额度
// —— 他会一直点"获取验证码"，而每次都被"刚发过了"挡回来，看着像坏了 ✗
func (s *mailCodeStore) revoke(email string) {
	s.mu.Lock()
	delete(s.m, email)
	s.mu.Unlock()
}

// verify 校验一个码。**成功即作废**（单次使用）✓
func (s *mailCodeStore) verify(email, code string, now time.Time) (bool, string) {
	if code == "" {
		return false, "请填验证码"
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	e := s.m[email]
	if e == nil {
		return false, "还没获取验证码，或者它已经用过了"
	}
	if now.After(e.exp) {
		delete(s.m, email)
		return false, "验证码过期了，重新获取一个"
	}
	if e.tries >= codeMaxTries {
		delete(s.m, email)
		return false, "试错次数太多了，重新获取一个"
	}
	e.tries++
	if !hmac.Equal(e.hash, s.hash(email, code)) {
		return false, "验证码不对"
	}
	delete(s.m, email) // 用过就作废，不能拿同一个码登两次
	return true, ""
}

// cleanup 清掉过期/用过的记录，以及长期不动的限流桶。
func (s *mailCodeStore) cleanup(now time.Time) int {
	s.mu.Lock()
	n := 0
	for k, e := range s.m {
		if now.After(e.exp) {
			delete(s.m, k)
			n++
		}
	}
	s.mu.Unlock()
	// 限流桶也要扔（不然"每个来过的邮箱/IP 都留一条"是慢性内存泄漏）
	idle := 2 * time.Hour
	n += s.perMail.cleanup(now, idle)
	n += s.perIP.cleanup(now, idle)
	return n
}

// randomCode 造一个 6 位数字码。
//
// 📌 用 crypto/rand 而不是 math/rand：验证码是**能当成凭据用的**东西
// （拿到它就能登进那个账号），可预测的伪随机等于没有 ✗
func randomCode() (string, error) {
	max := big.NewInt(1)
	for i := 0; i < codeLen; i++ {
		max.Mul(max, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", codeLen, n.Int64()), nil
}
